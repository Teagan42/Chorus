# 0068. chorusd serves health, readiness and metrics derived from the journal, on loopback

- **Status:** accepted
- **Source:** SPEC §8, §13, §7, §11, §14 · ADR-0007, ADR-0019, ADR-0035, ADR-0051, ADR-0060, ADR-0065

`chorusd` started no HTTP server. Nothing reported health, readiness or
metrics, and compose could not tell whether the daemon was alive. A dead
STT, TTS, LLM or Postgres, or a satellite that dropped off the Wi-Fi, went
unseen until someone noticed the house had stopped answering. SPEC §8
promised OpenTelemetry alongside the journal, and none existed. Now
`chorusd` serves `/healthz`, `/readyz` and `/metrics` on one ops listener.
Its counters are derived from the journal as it is written, and
OpenTelemetry is deferred. This refines ADR-0007 and ADR-0019 without
superseding either: the log is still the one record, and only the "OTel
alongside" consequence waits.

```go
import "github.com/teagan42/chorus/internal/ops"

// A sidecar chorusd starts without degrades readiness and never fails it.
var smartTurn = ops.Check{Name: "smart_turn", Required: false}
```

## The listener

`CHORUS_OPS_LISTEN` names the address. It is `127.0.0.1:6056` by default,
and `off` serves nothing. It is on by default because an endpoint nobody
remembered to enable watches nothing. It is loopback by default for the
review UI's reasons (`cmd/reviewui`, PR #90). Nothing on it asks who is
asking, and its counters say when the house is talking. Sessions by the
hour are occupancy by the hour, so serving that to the LAN is a choice. In
compose, `chorusd` listens on `:6056` inside the container, so a scraper on
the compose network reaches it, and the port is published on the host's
loopback only.

The listener opens in `start`, beside the satellites' port. A port that
will not bind fails startup, naming the variable. It closes with the
daemon: `run` waits for it, and open requests get five seconds on the
injected timers. A server that dies later is logged, and the daemon runs on
unwatched, since the house still answers.

## /healthz

`/healthz` calls nothing. It answers 200 while the readiness loop has begun
a round within three intervals (45 s) on the injected clock. Before the
first round it answers 503 `starting`, and after three missed rounds 503
`stalled`. That loop runs on the same timers and clock as everything else
the daemon schedules. A daemon whose accept loop fails returns from `run`
and exits, so "the process answers" already covers that loop.

The distroless image has no shell and no curl. So `chorusd -healthcheck`
asks the running daemon's `/healthz` on loopback and exits 0 or 1, and
compose's healthcheck runs the binary itself.

## /readyz

One JSON document lists each dependency's last probe and each inventory
satellite's links. Required means what `validate` already requires at
startup. The optional checks are present only when configured, as
`buildProviders` already treats them:

| Check | Required | Probe |
|---|---|---|
| `postgres` | yes | the pool's `Ping` |
| `stt` | yes | `GET STT_URL/health` (speaches) |
| `tts` | yes | `GET KOKORO_URL/health` (Kokoro-FastAPI) |
| `llm` | yes | `GET OLLAMA_URL/api/version` |
| `speaker_id` | no | `GET SPEAKERID_URL/health` (`sidecars/speakerid`) |
| `smart_turn` | no | `GET SMARTTURN_URL/health` (`sidecars/smartturn`) |
| `home_assistant` | no | `GET HASS_URL/api/` with the bearer token |

Each probe asks for reachability only. The sidecars' `/health` routes
already existed for the compose healthchecks. Every check is probed at
once, every 15 s, and each probe is given up on after 3 s on the injected
timers. A request reads the last round, so a hung sidecar never holds it.
Each dependency going down, and coming back, is logged once.

- A required dependency not up answers **503 `not_ready`**.
- An optional one down, or a satellite whose audio link or native API is
  not connected, answers **200 `degraded`**. The house still answers
  without them, and restarting `chorusd` would not bring a satellite back.
- Otherwise it answers **200 `ready`**.

```json norun
{
  "status": "degraded",
  "checked_at": "2026-10-08T07:42:00Z",
  "dependencies": [
    {"name": "postgres", "required": true, "state": "up", "since": "2026-10-08T06:00:00Z", "checked_at": "2026-10-08T07:42:00Z"},
    {"name": "speaker_id", "required": false, "state": "down", "since": "2026-10-08T07:41:45Z",
     "checked_at": "2026-10-08T07:42:00Z", "error": "GET http://speakerid:8890/health: answered 502 Bad Gateway"}
  ],
  "satellites": [
    {"name": "kitchen", "room": "kitchen", "audio_link": "connected", "native_api": "connected", "last_frame_at": "2026-10-08T07:41:59.968Z"},
    {"name": "office", "room": "office", "audio_link": "disconnected", "native_api": "disconnected"}
  ]
}
```

A satellite's audio link state comes from the daemon's own link
bookkeeping. Its last frame comes from a handler that stamps every frame the
link delivers, ahead of the stack. Its native API state comes from a
wrapper on the native dialer: held from a dial, dropped at the
connection's first close. None of this touches the session, the listener
or `native.go`.

## /metrics

`/metrics` serves the Prometheus text format, through
`github.com/prometheus/client_golang` (v1.23.2, fetched through the proxy).

**From the journal.** A `journal.Observer` hears each append after the
store answers. These counters are derived only from events already
journalled, so they cannot disagree with the log (ADR-0007):

| Series | From |
|---|---|
| `chorus_sessions_opened_total` | `session_opened` |
| `chorus_sessions_closed_total{reason}` | `session_closed` |
| `chorus_barge_ins_detected_total` | `barge_in_detected` |
| `chorus_barge_ins_rejected_total{stage}` | `barge_in_rejected` |
| `chorus_model_failures_total{reason}` | `model_failed` (ADR-0051) |
| `chorus_speech_failures_total{reason}` | `speech_failed` (ADR-0051) |
| `chorus_tool_calls_total{tool,outcome}` | `tool_result`, named by its `tool_called` |
| `chorus_first_audio_seconds` | `speech_started`'s `wait_ms` (ADR-0035) |
| `chorus_audio_dropped_total{reason}` | `audio_dropped` (ADR-0065) |
| `chorus_journal_append_errors_total{kind}` | appends that did not land |

`tool_result` names only its call, so the observer holds each call's tool,
by conversation and call id, until its result. A call that timed out is
held on, because its detached work reports again (ADR-0060). Each result
counts under its own outcome, so that call counts once as `timed_out` and
once as `detached`. At most 4096 calls are held, oldest let go first, and a
result whose call is not held counts as `tool="unknown"`. The histogram's
buckets straddle SPEC §11's 700 ms target.

**From the blob store.** Neither of these is a journal event, so neither
is a second record of one:

- `chorus_blob_bytes_written_total` comes from a meter on the directory
  store, counted at commit.
- `chorus_blob_writes_declined_total` comes from the disk guard's new
  `OnDecline`, counted as each write is declined. Retention's
  `audio_dropped` record of the same clip waits until its conversation
  ends.

**Read at scrape time from readiness:**

- `chorus_satellites_connected`
- `chorus_satellite_audio_link_connected{satellite}`
- `chorus_satellite_native_api_connected{satellite}`
- `chorus_satellite_last_frame_timestamp_seconds{satellite}`
- `chorus_dependency_up{dependency,required}`
- `chorus_ready`

Beside them are `chorus_build_info{version}` and the client's Go runtime and
process collectors.

## OpenTelemetry stays deferred

It does not fit the observer cleanly. Counters need one event at a time.
Spans need pairs: `session_opened` with its close, `tool_called` with its
result, a turn with its first frame. They also need a trace context the
journal does not carry. A span would be a reader of the log, like the
harvester, and is better written as one. That reader can come later
without touching the append path. The SDK and an OTLP exporter would also
pull in a gRPC dependency tree larger than everything in `go.mod` today,
for a trace backend no one here runs. SPEC §8 now says what is true, and
§14 lists the export as deferred.

## Alternatives rejected

- **Probe on the request.** A sidecar hung on a load would hang
  `/readyz`, and every scrape would load every dependency.
- **Count at the call sites** in the session, listener and speaker. That
  would be a second source of truth beside the journal (ADR-0007), and it
  touches the actors the journal already records.
- **Fail readiness on a disconnected satellite.** A satellite off the
  Wi-Fi is not chorusd's fault. An orchestrator that restarts the
  unready would restart a healthy daemon for it.
- **Bind every interface by default.** It is the reverse of the review
  UI's decision, for the same data.
- **Write the text format by hand.** `client_golang` was reachable, and
  the histogram, escaping and content negotiation are its job.

## Forecloses

- **Counters reset at a restart.** Prometheus's `rate` and `increase`
  absorb it. A dashboard that wants history since forever reads the
  journal.
- **The observer runs on the writer's goroutine.** It must stay a few
  counter increments, since a slow observer slows every append.
- **No Host allow-list.** Unlike the review UI, a page that re-points its
  domain at a browser on the chorusd host could read the counters and the
  readiness document. Neither holds audio or words, and binding elsewhere
  is the operator's choice.
- **Liveness does not see a wedged session, pruner or native loop.**
  `/healthz` says the scheduler runs. A stuck actor shows in the journal's
  silence, and the review UI and the counters read that.
- **Native API state is dial to close.** A connection the device stopped
  answering reads as held until `hold` notices.
