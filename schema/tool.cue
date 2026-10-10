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

	// Two names are the orchestrator's. "confirmation" carries the nonce a
	// confirmed call is redeemed with, and is offered on every confirmable
	// tool. "acknowledgement" is what to say while a slow tool works, and is
	// required on every slow one (ADR-0039).
	params: [...#Param & {name: !="confirmation" & !="acknowledgement"}] | *[]

	on_interrupt: #InterruptPolicy | *"cancel"
	scope:        #Scope | *"household"

	// Milliseconds. Surfaced to the model as a tool result on expiry (SPEC §7).
	timeout_ms: int & >=100 & <=120000 | *10000

	requires_confirmation: bool | *false

	// Calls whose arguments match any entry need the person's yes even when
	// the tool as a whole does not: "unlock the front door" is the same
	// service call as "turn on the kitchen lights". An entry matches when
	// every string parameter it names has that value (ADR-0038), and, when
	// it names target_class, the thing the call acts on has one of those
	// classes, as its executor reads them (ADR-0041).
	confirm_when?: [...close({
		for p in params if p.type == "string" {(p.name)?: string & !=""}
		target_class?: [string & !="", ...string & !=""]
	}) & struct.MinFields(1)]

	// Latency hint lets the model decide whether to speak before results land.
	latency: "fast" | "slow" | *"fast"

	// A deferred tool is declared, so its policy and docs exist, but the
	// model is not offered it: nothing runs it yet (SPEC §14, ADR-0060).
	deferred: bool | *false

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

// _waysIn are the cover device classes that let someone into the house when
// they open. Home Assistant sets them per entity ("Show as" in its UI).
_waysIn: ["door", "garage", "gate"]

tools: {
	speak: {
		name:        "speak"
		description: "Say something to the user. Runs concurrently with other tools; the model chooses when and whether to speak."
		latency:     "fast"
		params: [
			{name: "text", type: "string", description: "What to say.", required: true},
			{name: "mode", type: "string", description: "queue appends after current speech; preempt cancels it; interject pauses it, cuts in, then resumes it.", enum: ["queue", "preempt", "interject"]},
		]
	}

	end_session: {
		name:        "end_session"
		description: "Close the conversation. Call when the task is complete rather than waiting for silence."
	}

	// Memory belongs to the person it is about, so an unidentified voice can
	// neither add to nor erase anyone's (SPEC §5, ADR-0040).
	remember: {
		name:            "remember"
		description:     "Keep a lasting fact about the person you are speaking with, to recall in later conversations. Use it when they ask you to remember something, or tell you a preference that will still hold tomorrow."
		scope:           "person"
		unknown_speaker: "deny"
		params: [
			{name: "fact", type: "string", description: "The fact, as one plain sentence that makes sense on its own later, e.g. Takes oat milk in coffee.", required: true},
			{name: "shareable", type: "boolean", description: "True only when they say the rest of the household may know it."},
		]
	}

	forget: {
		name:            "forget"
		description:     "Forget something you remember about the person you are speaking with, so it is never recalled again. Name it by the id shown beside it in what you remember."
		scope:           "person"
		unknown_speaker: "deny"
		params: [
			{name: "memory_id", type: "string", description: "The id of the memory to forget, e.g. m_3f9c2a10.", required: true},
		]
	}

	// Timers belong to the house: anyone may set one, and it goes off on the
	// satellite it was set on, conversation or no (ADR-0045). Setting and
	// cancelling are done the moment they are called, so a barge-in keeps
	// the result rather than leaving the model unsure what the house did.
	timer_start: {
		name:         "timer_start"
		description:  "Start a countdown timer on this satellite. When it ends, the announcement is said out loud here, whether or not anyone is talking to you then. Returns the timer's id."
		on_interrupt: "detach"
		params: [
			{name: "seconds", type: "integer", description: "How long, in seconds, e.g. 720 for twelve minutes.", required: true},
			{name: "label", type: "string", description: "What it is for, in a word or two, e.g. oven."},
			{name: "announcement", type: "string", description: "What to say when it ends, as one short sentence, e.g. The oven timer is done."},
		]
	}

	timer_cancel: {
		name:         "timer_cancel"
		description:  "Cancel a running timer before it ends. Name it by the id timer_start or timer_list gave; call timer_list first when you do not know it."
		on_interrupt: "detach"
		params: [
			{name: "timer_id", type: "string", description: "The id of the timer to cancel, e.g. t_3f9c2a10.", required: true},
		]
	}

	timer_list: {
		name:        "timer_list"
		description: "List the household's running timers: each one's id, label, the satellite it was set on, and the seconds left."
	}

	// An announcement is a session with no wake word (SPEC §4): said on
	// another satellite, and, with start_conversation, answerable there.
	announce: {
		name:         "announce"
		description:  "Say something out loud on other satellites with nobody waking them, e.g. Dinner is ready. Name a room, or leave it out to say it in every room but this one. Set start_conversation to listen there for an answer afterwards with no wake word."
		on_interrupt: "detach"
		params: [
			{name: "text", type: "string", description: "What to say, as it should be heard, e.g. Dinner is ready, come down.", required: true},
			{name: "room", type: "string", description: "Where to say it: a room or satellite name, e.g. kitchen. Leave it out for every room but this one."},
			{name: "start_conversation", type: "boolean", description: "Listen for an answer afterwards, as if the person there had said the wake word."},
		]
	}

	media_search: {
		name:         "media_search"
		description:  "Search the media library."
		latency:      "slow"
		timeout_ms:   20000
		on_interrupt: "detach" // Cheap, harmless, and often still wanted.
		// Media is deferred (SPEC §14): nothing searches a library yet.
		deferred: true
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
		// off, is not undone by "never mind" (ADR-0038). A garage opens with
		// the same cover.open_cover as a blind, so those entries hold only
		// the covers Home Assistant classes as a way in (ADR-0041).
		confirm_when: [
			{domain: "lock", service: "unlock"},
			{domain: "lock", service: "open"},
			{domain: "alarm_control_panel", service: "alarm_disarm"},
			for s in ["open_cover", "toggle", "set_cover_position"] {
				{domain: "cover", service: s, target_class: _waysIn}
			},
			// homeassistant.toggle reaches cover.toggle; turn_on does not
			// support covers.
			{domain: "homeassistant", service: "toggle", target_class: _waysIn},
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
