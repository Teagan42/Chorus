package schema

// A gate on a parameter the tool does not take never matches, so the call it
// was meant to stop runs unconfirmed.
tools: front_door: {
	name:        "front_door"
	description: "Lock or unlock the front door."
	params: [{name: "action", type: "string", description: "lock or unlock"}]
	confirm_when: [{verb: "unlock"}]
}
