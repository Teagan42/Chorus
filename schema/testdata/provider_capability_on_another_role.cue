package schema

// A voice declaring tool calls: tts has no word for them, so the claim
// would be read by nothing.
providers: tts: piper: {
	description: "Piper over HTTP, claiming the turn engine's capability."
	cancels:     true
	tool_calls:  true
}
