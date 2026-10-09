// Tool registry schema. One declaration yields the model-facing JSON Schema
// AND the orchestrator's policy enforcement, so they cannot drift (SPEC §6).
package schema

import "struct"

// #InterruptPolicy decides what happens to an in-flight call on barge-in
// (SPEC §4.4).
#InterruptPolicy: "cancel" | "detach" | "uninterruptible"

// #Scope gates who may invoke a tool. "person" requires speaker identification
// to have succeeded (SPEC §5).
#Scope: "household" | "person" | "guest"

#JSONType: "string" | "number" | "integer" | "boolean" | "array" | "object"

#Param: {
	name:         =~"^[a-z][a-z0-9_]*$"
	type:         #JSONType
	description!: string & !=""
	required:     bool | *false
	enum?: [...string]
	if type == "array" {
		items!: #JSONType
	}
}

#Tool: {
	name!:        =~"^[a-z][a-z0-9_]*$"
	description!: string & !=""

	// "confirmation" is the orchestrator's: it carries the nonce a confirmed
	// call is redeemed with, and is offered on every confirmable tool.
	params: [...#Param & {name: !="confirmation"}] | *[]

	on_interrupt: #InterruptPolicy | *"cancel"
	scope:        #Scope | *"household"

	// Milliseconds. Surfaced to the model as a tool result on expiry (SPEC §7).
	timeout_ms: int & >=100 & <=120000 | *10000

	requires_confirmation: bool | *false

	// Calls whose arguments match any entry need the person's yes even when
	// the tool as a whole does not: "unlock the front door" is the same
	// service call as "turn on the kitchen lights". An entry matches when
	// every string parameter it names has that value (ADR-0038).
	confirm_when?: [...close({
		for p in params if p.type == "string" {(p.name)?: string & !=""}
	}) & struct.MinFields(1)]

	// Latency hint lets the model decide whether to speak before results land.
	latency: "fast" | "slow" | *"fast"

	// A tool that cannot be cancelled must be confirmable: otherwise a barge-in
	// strands a side effect the user has no way to stop.
	if on_interrupt == "uninterruptible" {
		requires_confirmation: true
	}

	// Person-scoped tools must say what happens to an unidentified speaker,
	// or an unknown voice silently inherits someone's permissions (SPEC §5).
	if scope == "person" {
		unknown_speaker!: "deny" | "guest_fallback"
	}

	// Slow tools must outlive the default timeout, or the model is told they
	// failed while they are still working.
	if latency == "slow" {
		timeout_ms: int & >=10000
	}
}

// ---------------------------------------------------------------- the registry

tools: [string]: #Tool

tools: {
	speak: {
		name:        "speak"
		description: "Say something to the user. Runs concurrently with other tools; the model chooses when and whether to speak."
		latency:     "fast"
		params: [
			{name: "text", type: "string", description: "What to say.", required: true},
			{name: "mode", type: "string", description: "queue appends after current speech; preempt cancels it; interject ducks and cuts in.", enum: ["queue", "preempt", "interject"]},
		]
	}

	end_session: {
		name:        "end_session"
		description: "Close the conversation. Call when the task is complete rather than waiting for silence."
	}

	remember: {
		name:            "remember"
		description:     "Store a durable fact about the current speaker."
		scope:           "person"
		unknown_speaker: "deny"
		params: [
			{name: "fact", type: "string", description: "The fact to retain.", required: true},
			{name: "shareable", type: "boolean", description: "Whether other household members may see this."},
		]
	}

	media_search: {
		name:         "media_search"
		description:  "Search the media library."
		latency:      "slow"
		timeout_ms:   20000
		on_interrupt: "detach" // Cheap, harmless, and often still wanted.
		params: [
			{name: "query", type: "string", description: "Free-text search.", required: true},
			{name: "limit", type: "integer", description: "Maximum results."},
		]
	}

	// ------------------------------------------------- Home Assistant adapter
	// The one real tool of SPEC §14 item 5. Minimal on purpose: enough surface
	// to act on the home, not a mirror of HA's service catalogue (ADR-0027).

	ha_call_service: {
		name:        "ha_call_service"
		description: "Act on the home through a Home Assistant service: lights, switches, covers, media players, scripts. Target an entity_id from ha_find_entities, or an area_id. Returns the entities whose state changed; an empty list means nothing matched or nothing needed to change."
		// The request is committed the moment it is sent. Cancelling would not
		// undo it and would leave the model not knowing what the home did, so
		// the call runs to completion and keeps its result (SPEC §4.4).
		on_interrupt: "detach"
		// Opening the house to whoever is at the door, or switching the alarm
		// off, is not undone by "never mind" (ADR-0038). A garage cover opens
		// with the same cover.open_cover as a blind, so it cannot be told
		// apart here.
		confirm_when: [
			{domain: "lock", service: "unlock"},
			{domain: "lock", service: "open"},
			{domain: "alarm_control_panel", service: "alarm_disarm"},
		]
		params: [
			{name: "domain", type: "string", description: "Service domain, e.g. light, switch, script.", required: true},
			{name: "service", type: "string", description: "Service within the domain, e.g. turn_on, turn_off, toggle.", required: true},
			{name: "entity_id", type: "string", description: "Entity to act on, e.g. light.kitchen. Required unless area_id is given."},
			{name: "area_id", type: "string", description: "Area to act on, e.g. kitchen. Required unless entity_id is given."},
			{name: "data", type: "object", description: "Extra service fields, e.g. {\"brightness_pct\": 50}."},
		]
	}

	ha_get_state: {
		name:        "ha_get_state"
		description: "Read one entity's current state and attributes from Home Assistant."
		params: [
			{name: "entity_id", type: "string", description: "Entity to read, e.g. sensor.living_room_temperature.", required: true},
		]
	}

	ha_find_entities: {
		name:        "ha_find_entities"
		description: "Find Home Assistant entities by domain and name. Call this first when you do not know an entity_id; never guess one."
		params: [
			{name: "domain", type: "string", description: "Only entities in this domain, e.g. light."},
			{name: "name", type: "string", description: "Only entities whose name or id contains this text, e.g. kitchen."},
		]
	}
}
