package schema

// Slow tool with a sub-default timeout: reported as failed while still working.
tools: deep_search: {
	name:        "deep_search"
	description: "Exhaustive library search."
	latency:     "slow"
	timeout_ms:  2000
}
