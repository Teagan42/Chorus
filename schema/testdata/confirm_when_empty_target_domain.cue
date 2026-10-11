package schema

// An entry that names target_domain with no domains holds no target, so the
// lock it was written for is switched unconfirmed.
tools: generic_service: {
	name:        "generic_service"
	description: "Turn an entity on or off."
	params: [{name: "service", type: "string", description: "turn_on or turn_off"}]
	confirm_when: [{service: "turn_on", target_domain: []}]
}
