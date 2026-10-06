package schema

// Person-scoped with no unknown-speaker policy: an unknown voice would
// silently inherit someone's permissions.
tools: whoami: {
	name:        "whoami"
	description: "Report who the speaker is."
	scope:       "person"
}
