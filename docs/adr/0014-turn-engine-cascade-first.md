# 0014. Define a turn-engine interface and implement the cascade first

- **Status:** accepted
- **Source:** SPEC §12, §11

The turn engine is an interface emitting `SpeechDelta`, `ToolCall`,
`ToolResult`, and `TurnEnd`. Phase 1 implements it as the streaming cascade. A
realtime speech-to-speech provider becomes an adapter mapping its event stream
onto ours — which is the second reason `speak` is a tool rather than a template
quirk ([ADR-0003](0003-speak-is-a-tool.md)).

Target is 700 ms wake-word-to-first-audio. Assist on local models runs 1.5–3 s
and feels bad; realtime S2S is ~300 ms and feels alive. Phase 1 is the plain
cascade with honest measurements — TTS first chunk and LLM prefill are expected
to dominate, not STT.

## Alternatives rejected

Building the S2S path early. It is not built until the cascade works end to
end: the adapter's shape is only knowable once the cascade's event stream is
real. Speculative execution — LLM prefill on STT partials, discarded if the
user keeps talking — is a later *measured* optimization, not a phase-1 guess.
