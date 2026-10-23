// Model provider capabilities. Every provider declares what it can do, and
// what the architecture needs of each role is declared beside them, so
// chorusd refuses a configured pipeline that cannot work before it hears a
// satellite (SPEC §10, ADR-0013, ADR-0102).
package schema

// roles are the seams a provider serves, in the order a turn meets them
// (SPEC §10). embedding is memory recall's (ADR-0044), outside the cascade.
roles: ["stt", "endpointing", "speaker_id", "embedding", "llm", "tts"]

#Role: or(roles)

// #Language is an ISO 639-1 code, as STT_LANGUAGE is read.
#Language: =~"^[a-z]{2}$"

// #Source cites what a declaration or a need was read from.
#Source: =~"^(SPEC §[0-9.]+|ADR-[0-9]{4})(, (SPEC §[0-9.]+|ADR-[0-9]{4}))*$"

#Provider: {
	name!:        =~"^[a-z][a-z0-9_]*$"
	role!:        #Role
	description!: string & !=""

	// A cancelled request returns at once. A barge-in, a deadline and a
	// discarded speculation all cancel; a provider that ignores it holds the
	// turn past the interrupt (SPEC §4.3, ADR-0095).
	cancels!: bool

	if role == "llm" {
		// The model calls tools natively. speak is a tool, so an engine
		// without them says nothing (SPEC §4.1).
		tool_calls!: bool
		// Words arrive as the model writes them, not as one reply.
		streams_text!: bool
		// A call's arguments arrive as the model writes them, so a speak
		// call's words reach the voice before the call closes (SPEC §4.1).
		streams_tool_arguments!: bool

		// Streamed arguments are a tool call's: without calls there are none.
		if streams_tool_arguments {
			tool_calls: true
		}
	}
	if role == "stt" {
		// What the ear hears. Empty is whatever its server's model hears,
		// which nothing knows before the server answers.
		languages!: [...#Language]
	}
}

providers: [R=#Role]: [N=string]: #Provider & {role: R, name: N}

providers: {
	llm: {
		ollama: {
			description:            "Qwen3 on Ollama's /api/chat, the default turn engine (ADR-0037). A tool call arrives whole, as a native JSON object."
			cancels:                true
			tool_calls:             true
			streams_text:           true
			streams_tool_arguments: false
		}
		vllm: {
			description:            "Qwen3 on vLLM's /v1/chat/completions (ADR-0077). A call dispatches when its arguments close, and a speak call's words stream out of them as they arrive."
			cancels:                true
			tool_calls:             true
			streams_text:           true
			streams_tool_arguments: true
		}
	}
	stt: {
		speaches: {
			description: "Parakeet TDT 0.6B v2 behind speaches' /v1/audio/transcriptions, the default ear (ADR-0024). English only: it accepts a language and ignores it."
			cancels:     true
			languages: ["en"]
		}
		openai: {
			description: "Any server with OpenAI's /v1/audio/transcriptions (ADR-0081). It hears what its model hears."
			cancels:     true
			languages: []
		}
		wyoming: {
			description: "A Wyoming ASR server over TCP, such as Home Assistant's whisper (ADR-0081). It hears what its model hears."
			cancels:     true
			languages: []
		}
	}
	tts: {
		kokoro: {
			description: "Kokoro-82M behind Kokoro-FastAPI, the default voice, rendered at 24 kHz and resampled by the host (ADR-0023)."
			cancels:     true
		}
		openai: {
			description: "Any server with OpenAI's /v1/audio/speech, asked for WAV (ADR-0081)."
			cancels:     true
		}
		wyoming: {
			description: "A Wyoming TTS server over TCP, such as Home Assistant's piper (ADR-0081)."
			cancels:     true
		}
	}
	speaker_id: speakerid: {
		description: "TitaNet-L on ONNX Runtime, the sidecar in sidecars/speakerid (ADR-0029)."
		cancels:     true
	}
	endpointing: smartturn: {
		description: "Smart Turn v3.2, the sidecar in sidecars/smartturn (ADR-0036)."
		cancels:     true
	}
	embedding: ollama: {
		description: "An embedding model on Ollama's /api/embed, for memory recall by relevance (ADR-0044)."
		cancels:     true
	}
}

// #Need is what the architecture asks of a role's provider. A required one
// the provider lacks refuses the pipeline at startup; a degraded one is said
// at startup, with what it costs the household.
#Need: {
	level!:       "required" | "degraded"
	consequence!: string & !=""
	source!:      #Source
}

// needs is keyed by role and then by a capability that role declares, so a
// need nothing can declare is refused here rather than never failing.
needs: {
	llm: close({
		cancels?:                #Need
		tool_calls?:             #Need
		streams_text?:           #Need
		streams_tool_arguments?: #Need
	})
	stt: close({
		cancels?:   #Need
		languages?: #Need
	})
	tts: close({cancels?: #Need})
	speaker_id: close({cancels?: #Need})
	endpointing: close({cancels?: #Need})
	embedding: close({cancels?: #Need})
}

// Every role's provider must stop when asked: the supervisor owns every
// request's lifetime, and one that outlives its turn is the bug class the
// architecture exists to avoid (SPEC §4, §4.3).
_cancels: #Need & {
	level:       "required"
	consequence: "a barge-in or a deadline cannot stop it, so the turn waits on work nobody is waiting for"
	source:      "SPEC §4.3"
}

needs: {
	llm: {
		cancels: _cancels
		tool_calls: {
			level:       "required"
			consequence: "speak is a tool, so the model could say nothing and act on nothing"
			source:      "SPEC §4.1, ADR-0003"
		}
		streams_text: {
			level:       "degraded"
			consequence: "nothing is said until the model has written its whole reply, so every answer waits out its slowest sentence"
			source:      "SPEC §11"
		}
		streams_tool_arguments: {
			level:       "degraded"
			consequence: "a speak call's words reach the voice only once the call closes, so first audio waits for the model to write the whole call; CHORUS_LLM_PROVIDER=vllm streams them"
			source:      "SPEC §4.1, SPEC §11, ADR-0077"
		}
	}
	stt: {
		cancels: _cancels
		languages: {
			level:       "required"
			consequence: "speech in any other language is transcribed as if it were one the ear knows, so every turn answers words nobody said; CHORUS_STT_PROVIDER=wyoming or openai with a Whisper hears others"
			source:      "SPEC §10"
		}
	}
	tts: cancels:         _cancels
	speaker_id: cancels:  _cancels
	endpointing: cancels: _cancels
	embedding: cancels:   _cancels
}
