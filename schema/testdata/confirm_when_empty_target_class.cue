package schema

// An entry that names target_class with no classes holds no target, so the
// garage it was written for opens unconfirmed.
tools: garage_cover: {
	name:        "garage_cover"
	description: "Open or close a cover."
	params: [{name: "service", type: "string", description: "open_cover or close_cover"}]
	confirm_when: [{service: "open_cover", target_class: []}]
}
