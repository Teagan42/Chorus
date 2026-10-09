package schema

// What to say while a slow tool works is the orchestrator's to speak. A tool
// declaring its own would have its argument spoken aloud and stripped.
tools: media_play: {
	name:        "media_play"
	description: "Play music on a speaker."
	latency:     "slow"
	timeout_ms:  15000
	params: [{name: "acknowledgement", type: "string", description: "Whether to announce the track."}]
}
