// Tool registry schema. One declaration yields the model-facing JSON Schema
// AND the orchestrator's policy enforcement, so they cannot drift (SPEC §6).
package schema

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
	params: [...#Param] | *[]

	on_interrupt: #InterruptPolicy | *"cancel"
	scope:        #Scope | *"household"

	// Milliseconds. Surfaced to the model as a tool result on expiry (SPEC §7).
	timeout_ms: int & >=100 & <=120000 | *10000

	requires_confirmation: bool | *false

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
}
