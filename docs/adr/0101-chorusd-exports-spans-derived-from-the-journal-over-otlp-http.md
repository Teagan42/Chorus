# 0101. chorusd exports spans derived from the journal over OTLP/HTTP

- **Status:** accepted · refines [ADR-0068](0068-chorusd-serves-health-readiness-and-metrics-derived-from-the-journal-on-loopback.md)
- **Source:** SPEC §8, §13, §14 · ADR-0007, ADR-0019, ADR-0068

ADR-0007 and ADR-0019 promised OpenTelemetry alongside the journal, so a
trace backend could explore what the review UI does not show. ADR-0068
deferred it for three reasons. Spans need pairs, not single events. They
need a trace context the journal does not carry. And the SDK's OTLP
exporter would bring a gRPC dependency tree. Each has an answer, and
`chorusd` now exports.

```go
import (
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/otel"
)

// A conversation's spans are a function of its log, live or read later.
func spansOf(log []journal.Event) []otel.Span { return otel.Spans(log, otel.Options{}) }
```

## Spans are a reader of the log

`internal/otel` folds journal events into spans, one event at a time, as
the harvester folds them into pairs. It never writes, so a span cannot
disagree with what was journalled (ADR-0007). `chorusd` feeds it from a
`journal.Observer`, and `Observe` now takes more than one, so the metrics
and the export each hear every append. `otel.Spans` folds a whole log the
same way. The household's Thursday, fed through one builder as it happened
with every conversation interleaved, yields exactly the spans each log
yields read alone, ids and all. A test holds that.

**The trace context is derived, not carried.** A conversation is one
trace. Its trace id is a hash of the conversation id. A span's id is a hash
of the conversation, the seq of the event that opened it, and its role. So
nothing new is journalled, a log read twice yields the same trace, and a
child can name its parent before the parent has ended. Every span carries
`chorus.conversation_id` and `chorus.seq`, which lead back to the event in
the review UI.

| Span | Opened by | Ended by | Parent |
|---|---|---|---|
| `chorus.session` | `session_opened` | its `session_closed`, paired by `satellite` | none: each session is a root, so a migration is two roots in one trace (SPEC §4.5) |
| `chorus.turn` | `utterance_transcribed` | the turn's last event, once the next utterance or the session's close shows it is over | the open session |
| `chat <model>` (client) | the utterance, or the completion or tool result the follow-up ask was made after | `model_completed` or `model_failed` | the turn |
| `execute_tool <tool>` | `tool_called` | its `tool_result` | the ask that called it, or the turn or session when none did: an announcement, a canned line |
| `detached <tool>` | a `timed_out` result | the late result of the work that went on (ADR-0060) | the call it outlived |
| `chorus.speculation` (client) | `ran_ms` before its discard | `speculation_discarded` (ADR-0095) | the session: it guessed at the next utterance |
| `chorus.summary` (client) | the close it followed | `conversation_summarized` (ADR-0043) | the session closed last |

Some timing is read, not just copied. The journal records a completion, not
its request, so an ask starts at the event it was made after. A kept
speculative ask starts `asked_early_ms` earlier. A turn starts when the
person stopped speaking, `speech_started`'s `wait_ms` before the first
frame, so the head of each turn is the household's wait (SPEC §11). An ask
the next utterance cut before its completion ends with the turn, marked
`chorus.cut`.

Point events are span events. `speech_started`, `speech_spoken`,
`speech_truncated`, `speech_failed` and both barge-in kinds go on the speak
call they belong to; the confirmations go on the call they held or ran;
`memory_recalled`, `speech_discarded` without a call, and `audio_dropped`
go on the turn, or on the session outside one; `announcement_made` goes on
the session. The device and
house logs (`presence_changed`, `wake_rejected`, the timers) are in no
conversation and in no trace. Every kind in the taxonomy is listed, the
skipped ones explicitly, so a new kind fails a test until someone decides
what a trace shows of it.

Failures are status ERROR naming why: `model_failed` with its reason and
error, a tool's `error` or `timed_out`, `speech_failed`, a session closed
by `error` or `device_lost`. A barge-in, a cancelled call and a held call
are not failures.

Attributes follow OpenTelemetry's GenAI conventions where they fit:
`gen_ai.operation.name`, `gen_ai.request.model`,
`gen_ai.response.finish_reasons`, `gen_ai.tool.name`,
`gen_ai.tool.call.id` and `gen_ai.conversation.id`. `session.id` is the
conversation and `user.id` is a turn's speaker, which Langfuse groups by.
The rest are `chorus.*`, named for the journal field they come from.

## The words stay home unless asked for

A trace backend is one more place a home's speech would be kept, and
ADR-0068 kept the counters free of words for the same reason. By default a
span says who, where, which tool, how long and how it went. It never says
the transcript, what was spoken or left unspoken, a tool's arguments or
result, the completion, a summary or a memory. `CHORUS_OTEL_WORDS=on` adds
them: `chorus.transcript`, `chorus.speech.text`,
`chorus.speech.unspoken_text`, `gen_ai.tool.call.arguments`,
`gen_ai.tool.call.result`, `chorus.completion_json`, `chorus.summary`,
`chorus.memories_json`, `chorus.summaries_json`. Speaker ids, satellite
names and error messages are sent either way.

## OTLP/HTTP protobuf, from vendored protos

The wire is OTLP/HTTP with protobuf bodies, the encoding every collector,
Jaeger, Tempo and Langfuse accept on port 4318. The four trace protos are
vendored at opentelemetry-proto v1.9.0 in `proto/opentelemetry`, as the
ESPHome API's are, and `buf generate` makes their messages beside it. No
service stubs are generated, so neither gRPC nor the SDK enters `go.mod`.
`google.golang.org/protobuf` was already there. `task proto:otlp`
re-vendors at `OTLP_PROTO_VERSION` and regenerates, and `task commit:check`
counts `proto/opentelemetry/` as vendored code.

`gRPC` is refused, and so is `http/json`. OTLP's JSON is not protobuf's
JSON mapping: ids are hex, not base64, and enums are numbers. Writing it
would mean encoding by hand what protobuf already encodes.

## Configuration is the standard variables

It is configured by the variables every SDK reads, so a collector's own
setup instructions apply as written:

| Variable | Meaning |
|---|---|
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | the full URL, used as written |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | the collector's base URL; `/v1/traces` is added |
| `OTEL_EXPORTER_OTLP_HEADERS`, `…_TRACES_HEADERS` | `key=value` pairs, percent-encoded, the second winning |
| `OTEL_EXPORTER_OTLP_PROTOCOL`, `…_TRACES_PROTOCOL` | `http/protobuf`, or unset |
| `OTEL_EXPORTER_OTLP_TIMEOUT`, `…_TRACES_TIMEOUT` | one export's bound in ms, 10 000 by default |
| `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | the resource; `chorusd` by default, with `service.version` |
| `OTEL_SDK_DISABLED=true`, `OTEL_TRACES_EXPORTER=none` | off |
| `CHORUS_OTEL_WORDS` | `on` sends the words; `off` or unset does not |

With no endpoint nothing is exported. ADR-0068 turned the ops listener on
by default because an endpoint nobody remembered to enable watches nothing.
An export has nowhere to go until it is told where. A variable that is set
but unusable stops startup and names itself, beside the rest of what
`validate` reports.

## Sending

`Appended` puts the event on a queue of 4096 and returns. With the queue
full, the event is dropped and counted, so the writer is never held up
(ADR-0068). One goroutine builds the spans. It sends at once when a session
closes, so a conversation shows up when it ends. Otherwise it sends every
5 s, or once 512 spans have ended. Each send is bounded by the timeout on
the injected timers.

OTLP says which answers are worth retrying: 429, 502, 503, 504, a timeout
and a broken connection. After one of those, spans are held and sent at the
next tick, up to 4096; past that the oldest are dropped. Any other refusal
drops the batch. So does a partial success's `rejected_spans`. The
collector going away is logged once, and so is it coming back.

`chorusd` stops the export after everything that journals has returned, so
the last closes and the summaries written after them are sent. No send is
cancelled partway, not even by the daemon stopping: a batch the collector
took while this side gave up on it would be held and sent twice. A send in
flight finishes within its timeout, and the final one is bounded by 5 s, as
the ops listener's close is. Spans still open
die with the process, as they would in a crash. `otel.Spans` ends a log's
open spans where they were last touched and marks them `chorus.unfinished`.
A live builder does the same to a conversation it lets go: it holds at most
256 conversations and 256 open calls in each.

`/metrics` gains:

- `chorus_otel_spans_exported_total`
- `chorus_otel_spans_dropped_total{reason}`, where reason is `rejected` or
  `backlog`
- `chorus_otel_events_dropped_total`

## Alternatives rejected

- **The OpenTelemetry SDK.** It wants spans started and ended by
  instrumented code, with random ids. Feeding it journal events would mean
  overriding its id generator per span and back-dating every start. It
  also brings gRPC through `otlptracehttp`'s dependency on the collector
  protos' generated stubs.
- **Instrumenting the session, listener and speaker.** That is ADR-0068's
  rejected second source of truth, made worse: spans timed at call sites
  would disagree with the log they claim to describe.
- **Random trace ids carried in the journal.** That journals a new field
  on every event, to tell a trace backend what it can derive.
- **A batch exporter over Postgres instead of a live one.** Nothing would
  show until someone ran it. `otel.Spans` is the batch form, and a backfill
  command over it is cheap to add when a household wants one.

## Forecloses

- **A span is only as fine as the journal.** No span shows STT decoding,
  TTS rendering or a model's time to first token, because none is
  journalled. Adding one means journalling the event first.
- **Ask timing is inferred.** An ask that started late, after the event it
  is anchored on, reads as longer than it was.
- **Ids are stable forever.** Changing the hash or the roles renames every
  span already exported, so a backend would show a re-read log as a second
  trace.
- **Restarts lose open spans.** Session survival across restart is deferred
  (SPEC §14), and the export is no different.
