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
			{name: "announced", type: "boolean", description: "Opened with no wake word, to say an announcement: a timer going off, or something asked to be said in this room (SPEC §4)."},
			{name: "room", type: "string", description: "Where the device is, as the inventory names it: what the model is told it is speaking from, so \"the lights\" can mean this room's (SPEC §5). Empty when the inventory names no room."},
		]
	}
	wake_rejected: {
		name:            "wake_rejected", actor: "device"
		description:     "Stage-one activation failed server-side confirmation. Hard negative for wake-word retraining."
		has_audio:       true
		training_signal: true
		fields: [
			{name: "reason", type: "string", description: "Which gate rejected it.", required: true, enum: ["no_speech", "low_confidence", "unknown_speaker"]},
			{name: "second_audio_ref", type: "string", description: "The same span from the XMOS's second, lighter-processed output, so retraining can target either stream (SPEC §9.3). Empty when the device streams one channel."},
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
			{name: "second_audio_ref", type: "string", description: "The same span from the XMOS's second, lighter-processed output (SPEC §8). Empty when the device streams one channel."},
			{name: "speaker_match", type: "string", description: "How the voice matched the household (SPEC §5). below_threshold and ambiguous make the utterance a guest's, whoever spoke before it. Empty when nothing judged the voice (no speaker identification, or the embedder failed) and in logs from before it was recorded: the current speaker keeps the turn.", enum: ["identified", "below_threshold", "ambiguous", "nobody_enrolled"]},
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
		description:     "Speech was cut short, usually by a barge-in; reason says what cut it. Carries the exact split between heard and unheard text. An interjected cut is a pause: the unheard text plays after the interjection, recorded under the same call_id (ADR-0056)."
		has_audio:       true
		training_signal: true
		fields: [
			{name: "spoken_text", type: "string", description: "What the user heard.", required: true},
			{name: "unspoken_text", type: "string", description: "Generated but never played.", required: true},
			{name: "frames_played", type: "integer", description: "DAC frame count at cut.", required: true},
			{name: "call_id", type: "string", description: "The speak call that was cut. Empty in logs from before it was recorded."},
			{name: "reason", type: "string", description: "Why it was cut. Only barge_in is the person interrupting; tts_unavailable and playback_unconfirmed are the voice or the device failing, which nobody chose and no preference pair may be cut from (ADR-0051). interjected is an interject call cutting in, after which the rest resumes (ADR-0056). Empty in logs from before it was recorded.", enum: ["barge_in", "preempted", "interjected", "session_closed", "migrated", "tts_unavailable", "playback_unconfirmed"]},
		]
	}
	speech_discarded: {
		name:            "speech_discarded", actor: "speaking"
		description:     "Speech generated but never played, because a barge-in, or whatever reason names, emptied the queue first. Distinct from truncation: nothing was heard."
		training_signal: true
		fields: [
			{name: "unspoken_text", type: "string", description: "Generated but never played.", required: true},
			{name: "call_id", type: "string", description: "The speak call the text belonged to, so a reader can set aside a line nobody's turn chose, such as a canned one. Absent in logs written before ADR-0051."},
			{name: "reason", type: "string", description: "Why it was dropped. tts_unavailable and playback_unconfirmed are the voice or the device failing, not the person (ADR-0051).", required: true, enum: ["barge_in", "preempted", "session_closed", "migrated", "tts_unavailable", "playback_unconfirmed"]},
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
		description: "What the model is told it remembers, from this turn on: the speaker's own memories and what others shared, and their recent conversations, chosen by relevance when there are more than fit. Recorded when it changes, so a replay asks the model with what it was given (SPEC §5)."
		fields: [
			{name: "person", type: "string", description: "Whose memories these are: the identified speaker. Empty for a guest, who is told nothing: recorded when a guest follows someone who was told something, so the guest's turn does not inherit it (SPEC §5)."},
			{name: "memories_json", type: "string", description: "The memories as a JSON array of {id, person, fact, shareable}, newest first. An empty array means nothing is remembered.", required: true},
			{name: "summaries_json", type: "string", description: "The person's recent conversations as a JSON array of {conversation_id, at, text}, newest first. Empty in logs from before conversations were summarized."},
			{name: "ranked_by", type: "string", description: "The embedding model that chose these by relevance to what was just said, when there were more than a turn is told. Empty when they are simply the newest: too few to choose from, no embedding model, or ranking failed (ADR-0044)."},
		]
	}
	conversation_summarized: {
		name:              "conversation_summarized", actor: "session"
		description:       "The conversation ended and the model summarized it for the identified people in it, to be told in their later conversations. Recorded in full, as a completion is, because replay cannot regenerate it (SPEC §5, §8)."
		requires_versions: true
		fields: [
			{name: "people_json", type: "string", description: "Whom the summary is kept for: every identified person who spoke, as a JSON array of ids.", required: true},
			{name: "summary", type: "string", description: "What the model wrote. Empty when it failed."},
			{name: "error", type: "string", description: "Why there is no summary, or why it was not kept. Empty when it was."},
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
	model_failed: {
		name:        "model_failed", actor: "thinking"
		description: "The model could not answer the turn: it was unreachable, its stream broke, or it went quiet past its deadline. No model is left to reason with, so a canned line says so instead (SPEC §7, ADR-0051)."
		// A failure is attributed to the configuration that failed.
		requires_versions: true
		fields: [
			{name: "reason", type: "string", description: "unavailable: the ask never started. failed: the stream broke partway. timed_out: nothing came for longer than the deadline, and the ask was given up on.", required: true, enum: ["unavailable", "failed", "timed_out"]},
			{name: "error", type: "string", description: "What the engine reported.", required: true},
			{name: "canned_call_id", type: "string", description: "The speak call that said the canned line. Empty when none was said: one has already been said this turn, or the session is ending."},
		]
	}
	speech_failed: {
		name:        "speech_failed", actor: "speaking"
		description: "A speak call's audio failed short of the person interrupting: the voice could not render it, or the device never confirmed playing it. The truncation or discard it caused names the same reason, so it is never mistaken for a barge-in (SPEC §7, ADR-0051)."
		fields: [
			{name: "call_id", type: "string", description: "The speak call whose audio failed.", required: true},
			{name: "reason", type: "string", description: "tts_unavailable: the voice failed to open or render. playback_unconfirmed: the device stopped reporting playback before the end.", required: true, enum: ["tts_unavailable", "playback_unconfirmed"]},
			{name: "error", type: "string", description: "What the voice or the device reported. Empty when it said nothing."},
			{name: "canned_call_id", type: "string", description: "The speak call that said the canned line from audio rendered ahead of time. Empty when none was said."},
		]
	}
	presence_changed: {
		name:        "presence_changed", actor: "device"
		description: "The satellite's own presence sensor changed: the Satellite1's mmWave radar, read over the native API. Recorded in the device's log, since presence belongs to the room, not to a conversation (ADR-0050)."
		fields: [
			{name: "state", type: "string", description: "What the sensor says. unknown is the native API dropping or the sensor having no reading yet: presence was not seen to end, but it can no longer be vouched for.", required: true, enum: ["present", "absent", "unknown"]},
			{name: "sensor", type: "string", description: "The entity it came from, by object id, e.g. room_presence."},
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
		description: "Session ended. The conversation outlives it when the reason is a migration: the person moved device, so this session closes and a resumed one opens (SPEC §4.5). An announcement nobody was asked to answer closes as announced once it has been said."
		fields: [
			{name: "reason", type: "string", description: "Why it ended.", required: true, enum: ["model_ended", "silence_timeout", "device_lost", "migrated", "announced", "error"]},
			{name: "satellite", type: "string", description: "Device whose stream ended. Pairs the close with its open.", required: true},
		]
	}
	announcement_made: {
		name:        "announcement_made", actor: "session"
		description: "Something was said that nobody in this conversation asked for: a timer going off, or someone in another room asking for it to be said here. Recorded in the conversation it was said in, ahead of the speak call that says it (SPEC §4)."
		fields: [
			{name: "text", type: "string", description: "What was to be said.", required: true},
			{name: "call_id", type: "string", description: "The speak call that says it.", required: true},
			{name: "source", type: "string", description: "Why it was said.", required: true, enum: ["timer", "request"]},
			{name: "timer_id", type: "string", description: "The timer that went off. Empty for a request."},
			{name: "requested_by", type: "string", description: "Who asked for it to be said, or who set the timer. Empty for a guest."},
			{name: "from_satellite", type: "string", description: "Where it was asked for, or where the timer was set."},
			{name: "from_conversation", type: "string", description: "The conversation that asked for it, or that set the timer."},
			{name: "start_conversation", type: "boolean", description: "Whoever is in the room may answer with no wake word."},
		]
	}

	// Timers belong to the house, not the conversation that set them: they
	// outlive its session, and the daemon (ADR-0045).
	timer_started: {
		name:        "timer_started", actor: "tool"
		description: "A timer was set. Recorded in the household's own log, which the daemon replays at startup to know what is running, so a timer outlives the session that set it and the process (ADR-0045)."
		fields: [
			{name: "timer_id", type: "string", description: "What timer_cancel takes, e.g. t_3f9c2a10.", required: true},
			{name: "seconds", type: "integer", description: "How long it was set for.", required: true},
			{name: "fires_at", type: "string", description: "When it goes off, RFC 3339 in UTC.", required: true},
			{name: "satellite", type: "string", description: "Where it was set, which is where it goes off.", required: true},
			{name: "label", type: "string", description: "What it is for, e.g. oven. Empty when unnamed."},
			{name: "announcement", type: "string", description: "What the model asked to be said when it goes off. Empty says it from the label."},
			{name: "person", type: "string", description: "Who set it. Empty for a guest."},
			{name: "conversation_id", type: "string", description: "The conversation that set it.", required: true},
			{name: "call_id", type: "string", description: "The timer_start call that set it.", required: true},
		]
	}
	timer_cancelled: {
		name:        "timer_cancelled", actor: "tool"
		description: "A running timer was cancelled before it went off."
		fields: [
			{name: "timer_id", type: "string", description: "The timer cancelled.", required: true},
			{name: "conversation_id", type: "string", description: "The conversation that cancelled it.", required: true},
			{name: "call_id", type: "string", description: "The timer_cancel call that cancelled it.", required: true},
		]
	}
	timer_finished: {
		name:        "timer_finished", actor: "session"
		description: "A timer went off, and whether anybody was told. A timer nobody heard is a failure the household felt, so it is recorded as one (SPEC §7)."
		fields: [
			{name: "timer_id", type: "string", description: "The timer that went off.", required: true},
			{name: "outcome", type: "string", description: "announced: said on its satellite. unannounced: its satellite was not connected, or would not say it. missed: it came due while the daemon was down, too long ago to be worth saying.", required: true, enum: ["announced", "unannounced", "missed"]},
			{name: "conversation_id", type: "string", description: "The conversation it was announced in. Empty unless announced."},
			{name: "error", type: "string", description: "Why it was not announced. Empty when it was."},
		]
	}
}
