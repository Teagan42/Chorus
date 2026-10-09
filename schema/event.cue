// Event taxonomy. The journal is the runtime's source of truth, not
// instrumentation, so an unvalidated event type silently corrupts training
// data (SPEC §8).
package schema

// #Actor is which supervisor child produced the event (SPEC §4).
#Actor: "session" | "listening" | "thinking" | "speaking" | "tool" | "device"

#Event: {
	name!:        =~"^[a-z][a-z0-9_.]*$"
	actor!:       #Actor
	description!: string & !=""

	// Speculative work must be distinguishable from committed work or replay
	// lies about what the system actually did (SPEC §11).
	speculative: bool | *false

	// Events carrying audio reference a blob rather than inlining it.
	has_audio: bool | *false

	// Training-signal events are what the DPO harvester reads (SPEC §9.1).
	training_signal: bool | *false

	fields: [...#Param] | *[]

	// Anything marked as a training signal must record the prompt and model
	// versions in effect, or the pair cannot be attributed to a configuration.
	if training_signal {
		requires_versions: true
	}
	requires_versions: bool | *false
}

events: [string]: #Event

events: {
	session_opened: {
		name:        "session_opened", actor: "session"
		description: "Wake word confirmed; a session begins. A resumed one joins a conversation already in progress on another device (SPEC §4.5)."
		fields: [
			{name: "satellite", type: "string", description: "Device that heard it.", required: true},
			{name: "speaker_id", type: "string", description: "Identified person, empty when unknown."},
			{name: "wake_confidence", type: "number", description: "Stage-two confirmation score."},
			{name: "resumed", type: "boolean", description: "Joined an existing conversation rather than starting one."},
		]
	}
	wake_rejected: {
		name:            "wake_rejected", actor: "device"
		description:     "Stage-one activation failed server-side confirmation. Hard negative for wake-word retraining."
		has_audio:       true
		training_signal: true
		fields: [
			{name: "reason", type: "string", description: "Which gate rejected it.", required: true, enum: ["no_speech", "low_confidence", "unknown_speaker"]},
		]
	}
	utterance_transcribed: {
		name:        "utterance_transcribed", actor: "listening"
		description: "Final STT result for one utterance."
		has_audio:   true
		fields: [
			{name: "text", type: "string", description: "Transcript.", required: true},
			{name: "speaker_id", type: "string", description: "Per-utterance speaker match."},
			{name: "embedding_json", type: "string", description: "The utterance's speaker embedding as a JSON array of numbers, stored whether or not it matched anyone (SPEC §5). Empty when the embedder was unavailable."},
		]
	}
	speech_started: {
		name:        "speech_started", actor: "speaking"
		description: "The DAC played the first frame of a turn's speech. Recorded once per turn, when the device reports it, so its wall clock is when the household first heard the answer (SPEC §11)."
		fields: [
			{name: "call_id", type: "string", description: "The speak call whose audio played first.", required: true},
			{name: "wait_ms", type: "integer", description: "Milliseconds from when the person stopped speaking to this frame: the endpointer's trailing silence counts, as the household waits through it. Empty when the ask carried no stop time."},
		]
	}
	speech_spoken: {
		name:        "speech_spoken", actor: "speaking"
		description: "Audio the user actually heard, bounded by DAC-reported playback position."
		has_audio:   true
		fields: [
			{name: "text", type: "string", description: "Text corresponding to played audio.", required: true},
			{name: "frames_played", type: "integer", description: "DAC frame count at completion.", required: true},
			{name: "call_id", type: "string", description: "The speak call this audio played, so the dialogue the model is told puts it where the model said it. Empty in logs from before it was recorded."},
		]
	}
	speech_truncated: {
		name:            "speech_truncated", actor: "speaking"
		description:     "Barge-in cut speech short. Carries the exact split between heard and unheard text."
		has_audio:       true
		training_signal: true
		fields: [
			{name: "spoken_text", type: "string", description: "What the user heard.", required: true},
			{name: "unspoken_text", type: "string", description: "Generated but never played.", required: true},
			{name: "frames_played", type: "integer", description: "DAC frame count at cut.", required: true},
			{name: "call_id", type: "string", description: "The speak call that was cut. Empty in logs from before it was recorded."},
		]
	}
	speech_discarded: {
		name:            "speech_discarded", actor: "speaking"
		description:     "Speech generated but never played, because a barge-in emptied the queue first. Distinct from truncation: nothing was heard."
		training_signal: true
		fields: [
			{name: "unspoken_text", type: "string", description: "Generated but never played.", required: true},
			{name: "reason", type: "string", description: "Why it was dropped.", required: true, enum: ["barge_in", "preempted", "session_closed", "migrated"]},
		]
	}
	tool_called: {
		name:        "tool_called", actor: "thinking"
		description: "Model dispatched a tool; emitted when its JSON closed, not at end of message."
		fields: [
			{name: "tool", type: "string", description: "Tool name.", required: true},
			{name: "call_id", type: "string", description: "Correlates with the result.", required: true},
			{name: "args_json", type: "string", description: "Serialized arguments.", required: true},
		]
	}
	tool_result: {
		name:        "tool_result", actor: "tool"
		description: "Tool completed, failed, or timed out. Failures are results the model reasons about (SPEC §7)."
		fields: [
			{name: "call_id", type: "string", description: "Matches the call.", required: true},
			{name: "outcome", type: "string", description: "How it ended. confirmation_required means the call never ran: it needs the person's yes first (SPEC §6).", required: true, enum: ["ok", "error", "timed_out", "cancelled", "detached", "confirmation_required"]},
			{name: "result_json", type: "string", description: "Serialized result."},
		]
	}
	confirmation_requested: {
		name:        "confirmation_requested", actor: "session"
		description: "A call that needs the person's yes was held, and the model was handed a nonce to call again with once they agree (SPEC §6)."
		fields: [
			{name: "call_id", type: "string", description: "The call that was held.", required: true},
			{name: "nonce", type: "string", description: "Redeemable once, by the same call, in the turn of the next thing the person says.", required: true},
			{name: "presented", type: "string", description: "The nonce the call carried, which this refusal spends: a nonce gets one try. Empty when it carried none."},
			{name: "refused", type: "string", description: "Why the nonce the call carried was not accepted. Empty when it carried none.", enum: ["unknown", "used", "args_changed", "not_answered", "expired"]},
		]
	}
	confirmation_given: {
		name:        "confirmation_given", actor: "session"
		description: "A held call came back with its nonce after the person answered, and ran. What they said is the utterance the nonce was redeemed after (SPEC §6)."
		fields: [
			{name: "call_id", type: "string", description: "The call that ran.", required: true},
			{name: "nonce", type: "string", description: "The nonce it redeemed.", required: true},
		]
	}
	memory_recalled: {
		name:        "memory_recalled", actor: "session"
		description: "What the model is told it remembers, from this turn on: the speaker's own memories and what others shared. Recorded when it changes, so a replay asks the model with what it was given (SPEC §5)."
		fields: [
			{name: "person", type: "string", description: "Whose memories these are: the identified speaker.", required: true},
			{name: "memories_json", type: "string", description: "The memories as a JSON array of {id, person, fact, shareable}, newest first. An empty array means nothing is remembered.", required: true},
		]
	}
	model_completed: {
		name:        "model_completed", actor: "thinking"
		description: "Model finished a completion. Recorded in full, not just the request, because replay cannot regenerate it (SPEC §8)."
		// Replay attributes a completion to the configuration that produced it.
		requires_versions: true
		fields: [
			{name: "completion_json", type: "string", description: "Raw completion as returned.", required: true},
			{name: "finish_reason", type: "string", description: "Why generation stopped.", required: true, enum: ["stop", "length", "tool_calls", "error"]},
		]
	}
	barge_in_detected: {
		name:        "barge_in_detected", actor: "listening"
		description: "Interruption passed the detection gate. Timing is milliseconds into TTS playback, not wall clock, so replay reproduces the cut."
		has_audio:   true
		fields: [
			{name: "tts_position_ms", type: "integer", description: "Playback offset at detection.", required: true},
		]
	}
	barge_in_rejected: {
		name:        "barge_in_rejected", actor: "listening"
		description: "Candidate interruption failed the detection gate. Tuning corpus for SPEC §4.3."
		has_audio:   true
		fields: [
			{name: "stage", type: "string", description: "Gate that rejected it.", required: true, enum: ["vad", "speaker_id", "partial_length"]},
		]
	}
	session_closed: {
		name:        "session_closed", actor: "session"
		description: "Session ended. The conversation outlives it when the reason is a migration: the person moved device, so this session closes and a resumed one opens (SPEC §4.5)."
		fields: [
			{name: "reason", type: "string", description: "Why it ended.", required: true, enum: ["model_ended", "silence_timeout", "device_lost", "migrated", "error"]},
			{name: "satellite", type: "string", description: "Device whose stream ended. Pairs the close with its open.", required: true},
		]
	}
}
