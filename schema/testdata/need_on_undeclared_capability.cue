package schema

// A need on a capability its role does not declare: no voice can meet it,
// so every pipeline would be refused, or none, depending on how it is read.
needs: tts: streams_tool_arguments: {
	level:       "degraded"
	consequence: "the voice cannot stream a call's arguments"
	source:      "SPEC §11"
}
