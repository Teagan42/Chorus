# 0006. Key the conversation on the person; key the audio stream on the device

- **Status:** accepted · unretrofittable (SPEC §15.4)
- **Source:** SPEC §4.5

A wake word opens a session, audio streams continuously while it is open, and
server-side semantic endpointing (Smart Turn v2 over STT partials, not the big
LLM) decides turn boundaries so "turn off the… uh… kitchen lights" works. The
conversation belongs to the identified person; the audio stream belongs to the
satellite.

Separating the two makes device migration fall out for free: the same person
waking a different satellite within ~2 minutes resumes the same logical
conversation.

Close is model-decided through an `end_session` tool with a ~20 s silence
backstop, because the model knows a task is finished and a timer does not.

## Forecloses

Conversation state keyed by device or by socket. Retrofitting person-keying
would rewrite every journal row's identity.
