# 0003. Treat model output as a concurrent action stream, with `speak` as a tool

- **Status:** accepted · unretrofittable (SPEC §15.3)
- **Source:** SPEC §4.1, §12

Model output is a stream of actions the model opens and closes at will, not a
sequence of messages: speak, stay silent, speak twice, call three tools in
parallel, speak again after results. The orchestrator imposes no order. Tool
calls dispatch the instant their JSON closes, and speech deltas stream to TTS as
emitted.

`speak` is a declared tool so it is schema-addressable, but it holds no
privileged position. That framing is the only one that survives the
speech-to-speech adapter (§12), where speech is one event among many rather
than the model's return value.

```go
import "github.com/teaganglenn/chorus/internal/registry"

// speak is an ordinary registry entry, policy and all.
func speakIsDeclared() bool { return registry.Specs["speak"].Name == "speak" }
```

## Consequences

Inline `content` from templates that emit `content` and `tool_calls` together is
recorded as an implicit `speak`, so the journal has one representation of
speech regardless of template shape.
