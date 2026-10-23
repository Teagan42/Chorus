package schema

// An ear naming its language in prose: STT_LANGUAGE is an ISO 639-1 code,
// so "english" never matches it.
providers: stt: whisper: {
	description: "faster-whisper over HTTP."
	cancels:     true
	languages: ["english"]
}
