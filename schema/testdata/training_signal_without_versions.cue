package schema

// Training signal must record versions; asserting false must conflict.
events: mystery: {
	name:             "mystery"
	actor:            "session"
	description:      "Unattributable training signal."
	training_signal:  true
	requires_versions: false
}
