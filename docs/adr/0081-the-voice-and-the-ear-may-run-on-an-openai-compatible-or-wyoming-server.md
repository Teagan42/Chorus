# 0081. The voice and the ear may run on an OpenAI-compatible or Wyoming server

- **Status:** accepted · refines [ADR-0013](0013-local-first-model-stack.md), [ADR-0023](0023-kokoro-renders-at-24-khz-the-host-resamples.md) and [ADR-0032](0032-record-the-ear-and-the-voice-as-named-versions.md)
- **Source:** SPEC §3.2, §4.2, §4.5, §8, §10, §13 · ADR-0013, ADR-0023, ADR-0024, ADR-0032, ADR-0068, ADR-0077

SPEC §10 names Kokoro for the voice and Parakeet on speaches for the ear,
and chorusd could run nothing else. A household leaving Home Assistant's
Assist already runs Wyoming's whisper and piper, and one with a GPU box or
a cloud key already runs something answering OpenAI's audio API.
`CHORUS_TTS_PROVIDER` and `CHORUS_STT_PROVIDER` choose, as
`CHORUS_LLM_PROVIDER` does for the turn engine (ADR-0077). Kokoro and
speaches stay the defaults, unchanged.

```go
import (
	"github.com/teagan42/chorus/internal/provider/openai"
	"github.com/teagan42/chorus/internal/provider/wyoming"
)

// Home Assistant's piper add-on as the voice, Groq's Whisper as the ear.
func aMixedHouse() (*wyoming.Synth, *openai.Transcriber, error) {
	voice, err := wyoming.NewSynth(wyoming.SynthConfig{Address: "homeassistant.local:10200", Voice: "en_US-lessac-medium"})
	if err != nil {
		return nil, nil, err
	}
	ear, err := openai.NewTranscriber(openai.TranscriberConfig{BaseURL: "https://api.groq.com/openai", Model: "whisper-large-v3-turbo"})
	return voice, ear, err
}
```

## The providers

| Provider | Voice | Ear | Readiness asks |
|---|---|---|---|
| `kokoro` / `speaches` (defaults) | `KOKORO_URL` | `STT_URL` | `/health` |
| `openai` | `OPENAI_TTS_URL`, `_MODEL`, `_VOICE`, `_API_KEY` | `OPENAI_STT_URL`, `_MODEL`, `_API_KEY` | `/v1/models`, with the key |
| `wyoming` | `WYOMING_TTS_ADDR`, `WYOMING_TTS_VOICE` | `WYOMING_STT_ADDR`, `WYOMING_STT_MODEL` | `describe`, wanting `info` |

`STT_LANGUAGE` is sent to whichever ear is chosen, when set.

**OpenAI's audio API** (`internal/provider/openai`) covers OpenAI, Groq,
LocalAI, whisper.cpp's server, openedai-speech and speaches' own TTS. The
voice asks for `wav`, not `pcm`: OpenAI's raw PCM is 24 kHz by fiat and
other servers' is whatever their model renders at, unannounced, while a
WAV header says. A base URL is accepted with or without its `/v1`.

**Wyoming** (`internal/provider/wyoming`) is Home Assistant's protocol:
JSON header lines and binary payloads over TCP, a connection per
request. The ear streams the utterance as 64 ms `audio-chunk`s and waits
for `transcript`; the voice sends `synthesize` and collects chunks until
`audio-stop`. Data is written inline in the header, which every protocol
version reads, and read inline or after it. `tcp://` is accepted on an
address because Home Assistant's settings show one that way.

## What is recorded

Every model and voice is named in the environment and none is defaulted,
as `OLLAMA_MODEL` is not: the journal records them against every event
(SPEC §8), and a server's default is not written down anywhere it can
see. OpenAI's voice records `model/voice` (ADR-0032). Piper's voice id
already names its model, so Wyoming's records the voice, with `/speaker`
when one is chosen. Wyoming's ear has the server choose its model, so
`WYOMING_STT_MODEL` is not sent: it is the household's word for what it
started the server with, and wrong if they disagree.

## Resampling

These voices render at 22.05, 24 or 44.1 kHz, known only once audio
arrives. `internal/provider/pcm` resamples any rate to the device's 16 kHz
with a polyphase windowed sinc, its kernels built per clause: 33 taps at
the input rate as ADR-0023's measured, widened in proportion when
downsampling further, normalised per phase. Rates needing over 4096
phases are refused as a misread header. Kokoro keeps its own exact 3:2
kernels. The device-format WAV writer speaches had moved there too, so
both ears upload the same bytes.

## Alternatives rejected

- **Speaches' TTS route in place of Kokoro-FastAPI.** It is one server
  of many on OpenAI's API, so the `openai` voice reaches it; Kokoro's
  pinned image and its measured quirks (ADR-0023) stay as they are.
- **Asking the Wyoming server its model with `describe` at startup.**
  chorusd builds its providers without touching the network
  (`buildProviders` in `cmd/chorusd/config.go`), and a
  server restarted with another model would still be recorded under the
  old one.

## Forecloses

- A cloud ear or voice sends the household's speech off the LAN. ADR-0013
  stays the default; choosing otherwise is the household's, and the key
  is never logged or quoted in an error.
- Neither generic voice streams: each clause is a whole render, as
  Kokoro's is, and the satellite's clause chunking keeps audio flowing.
