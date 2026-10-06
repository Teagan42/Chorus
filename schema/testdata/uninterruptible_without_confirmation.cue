package schema

// An uninterruptible tool that cannot be confirmed strands side effects.
tools: unlock_door: {
	name:                 "unlock_door"
	description:          "Unlock the front door."
	on_interrupt:         "uninterruptible"
	requires_confirmation: false
}
