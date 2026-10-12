# 0077. The turn engine may run on vLLM, and a speak call's words stream out of its arguments

- **Status:** accepted · refines [ADR-0013](0013-local-first-model-stack.md) and [ADR-0046](0046-a-turn-that-calls-nothing-has-its-answer-spoken-from-its-content.md)
- **Source:** SPEC §4.1, §4.2, §4.4, §10, §11, §12, §13, §14 · ADR-0003, ADR-0013, ADR-0014, ADR-0037, ADR-0046, ADR-0051, ADR-0068, ADR-0071

SPEC §10 puts Qwen3 on vLLM because vLLM parses tool calls as they stream,
which §4.1 needs; §14 said the seam ran on Ollama and vLLM was not wired
up. `internal/provider/vllm` is a second `session.Engine`, on vLLM's
OpenAI-compatible `/v1/chat/completions`. `CHORUS_LLM_PROVIDER=vllm`
chooses it. Ollama stays the default, and keeps serving the embedding
model either way.

```go
import "github.com/teagan42/chorus/internal/provider/vllm"

// Qwen3 on the household's GPU box, with thinking off, as chorusd builds it.
func onTheGPUBox() (*vllm.Engine, error) {
	return vllm.New(vllm.Config{BaseURL: "http://10.0.0.20:8000", Model: "Qwen/Qwen3-32B-AWQ"})
}
```

## The server

vLLM 0.9 or later, started with its tool parser on. Qwen3 writes calls
in Hermes's `<tool_call>` tags, so the parser is `hermes`. Without
`--enable-auto-tool-choice` the server refuses `tool_choice: auto`, and
the turn fails before it starts.

```sh norun
vllm serve Qwen/Qwen3-32B-AWQ \
  --enable-auto-tool-choice --tool-call-parser hermes \
  --reasoning-parser qwen3 \
  --max-model-len 32768 \
  --api-key "$VLLM_API_KEY"
```

`--reasoning-parser qwen3` only matters with thinking on: it moves the
reasoning into its own field, which ADR-0046's rule needs. `--api-key` is
optional; chorusd sends `VLLM_API_KEY` as a bearer token when it is set.
Readiness probes `/health`, which vLLM serves without the key (ADR-0068).
docker-compose has no LLM service, Ollama's or vLLM's (SPEC §13): vLLM
needs a GPU the compose stack's CPU images do not assume, so a household
runs it where its GPU is and gives chorusd the URL.

## What is sent

What the model is told is the same as on Ollama, from the same code. The
persona, the system message, the dialogue derived from the log, and the
tool declarations moved out of the Ollama package into
`internal/provider/chat`. The Ollama engine's behaviour, tests and hashes
are unchanged. Both endpoints take OpenAI's function shape for tools, so
the declarations marshal to the same bytes. The prompt and tool-schema
versions an event records are therefore the same on either engine, and
only the model differs. `internal/contexts` already holds their text
(ADR-0071), and a trace from one engine is comparable to the other's.

Only the framing differs:

- A call's arguments are a JSON string, not an object.
- A tool result names the call it answers by `tool_call_id`, where
  `/api/chat` names its tool.

Each request asks for `stream: true`, `tool_choice: auto` and
`chat_template_kwargs: {"enable_thinking": <VLLM_THINKING>}`. The
summary at a conversation's end is one unstreamed ask with no tools, as
on Ollama (ADR-0043).

## Streaming

vLLM streams a call as deltas keyed by the call's index. The first delta
carries the id and the name, and the rest carry fragments of the argument
string, split wherever the tokenizer split them. The decoder handles them
like this:

- **A call dispatches the moment its arguments are a whole JSON object.**
  An object that has closed cannot be extended, so nothing after it can
  change the call. That moment is the instant §4.1 asks for, and it comes
  before the next call starts or the turn ends. A call whose arguments
  never close is acted on as written when the next index starts or the
  turn finishes, since vLLM's parsers stream one call at a time. Broken
  JSON goes as written and fails as a result (ADR-0037). No arguments at
  all go as `{}`.
- **A speak call's words stream out of its arguments while they arrive.**
  On every fragment the decoder rereads the `text` argument's string so
  far and sends the words that are new. It holds back anything it cannot
  yet decode whole: an escape cut in half, a high surrogate waiting for its
  low half, or a rune whose bytes have not all come. Speech therefore
  starts before the call closes, let alone the turn. The last words go
  with the delta marked last, taken from the closed JSON, so the whole
  call is the record. A call cut off mid-words, by `length` or by the next
  call, ends its utterance where the words stopped. On Ollama, by
  contrast, every speak call arrives whole (ADR-0037).
- **A mode written after the words began does not move them.** The speech
  channel places an utterance by its first delta (SPEC §4.2). A model that
  writes `{"text": ..., "mode": "interject"}` has started speaking before
  its mode arrives, and its words are queued. The other choice was to hold
  the words until the mode came or the call closed. That would lose
  streaming whenever the mode comes second or is left out, and `mode` is
  optional. A late `preempt` or `interject` is kept in the completion, as
  the model wrote it. A mode written before the words is honoured from the
  first delta. An out-of-enum mode is queue, as on Ollama.

The completion the journal keeps is the message vLLM would have returned
unstreamed: content, reasoning, and each call with its arguments as
written (SPEC §8).

## Ending

The turn ends on the chunk that carries `finish_reason`. `stop`, `length`
and `tool_calls` are what the journal's enum allows besides `error`, and
pass through. Any other reason, such as `abort`, ends the turn as a
failure (ADR-0051), and so do these:

- `[DONE]` before a finish reason;
- a stream that stops;
- vLLM's in-band `{"error": ...}` on a 200 it has already sent.

Each such failure first closes any open utterance, then reports the error
and the partial completion. The HTTP client has no timeout, so a stream
that stalls is the session's deadline to give up on (SPEC §4.5).

A barge-in cancels the turn's context, which abandons the request. The
decoder then closes the utterance and reports the end on a context that
cannot be cancelled, as `session.Engine` requires. A turn the model
finished reports its end that way too. Without that, an `end_session`
that closes the session as the stream ends could drop the turn's last
`model_completed`.

## Thinking is off by default

Qwen3 reasons before its first word. Measured on qwen3:14b on Ollama,
that took 3.5–4.2 s before the first call, against §11's ~700 ms to first
audio. `VLLM_THINKING` defaults to false, sent as `enable_thinking`. A
household that wants the reasoning sets it true and pays the wait.

The cost falls on ADR-0046. Content is spoken only from a turn that kept
its reasoning in its own field, so with thinking off a Qwen3 that answers
in content rather than calling speak is not heard. ADR-0046 kept that
case unheard because content without a thinking field could be reasoning:
qwen3:4b, with thinking off, wrote 2159 characters of it there. Whether
Qwen3 on vLLM, with the template's switch off, still writes reasoning as
content is not measured. Until it is, the rule stays as ADR-0046 wrote it,
and the prompt still asks for the tool.

## What vLLM does not serve here

A vLLM process serves one model. Recall's embedding model (ADR-0044)
therefore stays on Ollama: `OLLAMA_EMBED_MODEL` on `OLLAMA_URL`, which
validation asks for under `vllm` only when the embedding model is set.
Without either, recall tells a turn the newest memories, as it already
does without an embedding model.

The review UI's Replay builds its engines from the same variables, so a
vLLM household re-runs its turns on vLLM. A turn recorded under an Ollama
model name re-runs only once the reviewer names a model vLLM serves.

## Alternatives rejected

- **Ollama's own OpenAI-compatible endpoint.** It is the same engine
  behind another wire, and SPEC §10's reason for vLLM is the engine's tool
  parsing, not the wire.
- **Buffer a speak call to its closing brace, as Ollama delivers it.**
  Simpler, and it would keep ADR-0037's note that an ask made while
  speech plays knows its words. But a paragraph's first words would wait
  for its last, which is exactly what vLLM was chosen to avoid.
- **A streaming JSON parser library.** The arguments are a flat object
  of a few hundred bytes. Rereading them from the start on each fragment
  costs nothing, and keeps no tokenizer state between fragments to go
  wrong.
- **Serve embeddings from a second vLLM process.** It is possible, but it
  is a second GPU process for one call per turn, and nothing about the
  embedder seam needs it now.

## Forecloses

- **A follow-up ask does not see streamed speech that is still playing.**
  The session records a streamed speak call when its first delta arrives,
  before its words are known, and its words reach the log only when
  playback settles them. A follow-up asked while "Turning off the lights"
  still plays is not told it, and may say it again. On Ollama every speak
  arrives whole, so the words are recorded with the call. Recording the
  words when the last delta arrives is the session's change to make.
- **Nothing here is measured against a real server.** The streams in
  `testdata` are written in the shape vLLM's OpenAI server and its Hermes
  parser send. The models tier (`-vllm-url`) logs time to first words, the
  completion, and so whether Qwen3 writes `mode` before `text`. It was not
  run where this was written, which has no GPU.
- **SPEC §10's provider capability declarations are still not built.**
  Nothing checks at build time that a configured engine streams tool
  calls; this engine does by construction.
