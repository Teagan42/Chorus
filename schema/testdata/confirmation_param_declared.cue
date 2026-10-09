package schema

// The nonce argument is the orchestrator's. A tool declaring its own would
// have its argument stripped before it ran, or mistaken for a nonce.
tools: unlock_door: {
	name:                  "unlock_door"
	description:           "Unlock the front door."
	requires_confirmation: true
	params: [{name: "confirmation", type: "string", description: "Whether to unlock."}]
}
