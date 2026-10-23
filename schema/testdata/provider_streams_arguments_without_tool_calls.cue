package schema

// An engine that streams a call's arguments but makes no calls: there is
// nothing for the arguments to belong to.
providers: llm: llamacpp: {
	description:            "llama.cpp's server, claimed to stream arguments it cannot call with."
	cancels:                true
	tool_calls:             false
	streams_text:           true
	streams_tool_arguments: true
}
