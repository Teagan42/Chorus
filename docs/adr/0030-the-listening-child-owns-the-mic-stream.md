# 0030. The Listening child owns the mic stream, one per link

- **Status:** accepted
- **Source:** SPEC §4, §4.3, §4.5, §5, §9.3, §3.2 · ADR-0002, ADR-0004, ADR-0015, ADR-0024, ADR-0025

Every seam for hearing existed and nothing joined them: `internal/bridge`
delivers mic PCM and wake words, `internal/stt` turns PCM into partials and
a final, `internal/identity` turns PCM into a speaker, and `internal/session`
consumes a `Wake`, a `Transcript` and a `Candidate`. `internal/listen` is
the join. It is written as the Listening child SPEC §4 already names — the
session has carried `"listening"` in `Children()` since the supervisor
landed — rather than as a pipeline stage, because its work is concurrent
with the other children by nature: a partial is a barge-in candidate only
while the Speaking child is alive, and an utterance that ends while the
Thinking child is running is the next turn, not an error. A stage machine
would need an escape hatch for each of those; a child needs none (ADR-0002).

## Shape

One listener per satellite link, the link's `bridge.Handler`, owned by the
link's `Serve` lifetime; the session stays the supervisor's. The listener's
states are what it holds, not an enum: armed by a wake, confirming on the
first utterance, live with a session, idle otherwise. Idle audio is dropped —
the mic streams continuously (§3.2) and none of it is ours without a wake.
Hardware mute is authoritative (CONTRIBUTING §7): under it, audio is dropped,
an open utterance ends without a transcript, a pending wake is spent, and
nothing is buffered across the mute.

The session opens on the first utterance, not on the wake frame. Two reasons.
The wire carries no pre-roll — `bridge.TypeWake` is the word alone, and
`Hello` has no pre-roll field — so there is nothing to confirm a wake against
until someone has spoken. And the conversation is keyed on the person
(ADR-0006): opening with an unknown person would give every wake a fresh
guest conversation and lose migration, where opening after the first
utterance's embedding has resolved resumes the right one.

```go
import "github.com/teagan42/chorus/internal/listen"

// A rejected wake belongs to the device, not to a conversation it never
// opened: the audio stream is the device's (SPEC §4.5).
func rejectionLog() string { return listen.DeviceConversation("kitchen") }
```

## Wake confirmation: stages two and three run, stage one is stubbed

SPEC §9.3's three stages, on the first utterance (ADR-0015):

- **Stage two, contains speech**: the utterance's final decode is empty, or
  the wake window (`DefaultWakeWindow`, the session's silence backstop in
  bytes) expires with no speech at all. Either is `wake_rejected` with
  reason `no_speech`, the audio stored, no session.
- **Stage three, household member**: the resolver answers `BelowThreshold`
  or `Ambiguous`, which is `wake_rejected` with reason `unknown_speaker`.
  `NobodyEnrolled`, no resolver, and a resolver that failed all pass: the
  fresh install must answer someone, and a sidecar that is down must not
  silence the house (SPEC §5, ADR-0025).
- **Stage one, re-score the pre-roll**: not run. Nothing on the wire is a
  pre-roll, so there is no segment to score and no score to record;
  `session_opened` carries no `wake_confidence` and nothing invents one.
  When the firmware sends a pre-roll, the rejection is `wake_rejected` with
  reason `low_confidence` — the enum already holds it — and the score becomes
  `session.Wake.Confidence`. Whether the pre-roll is a frame from the device
  or a rolling buffer on the host is a firmware decision this record does not
  make.

Rejections go to a per-device log, `listen.DeviceConversation`, because the
journal keys every event on a conversation and a rejected wake opened none.
The reducer already folds `wake_rejected` as no state, so replaying that log
is empty by design.

Stage three as specified is in tension with SPEC §5's guest context: a guest
cannot wake the house once anyone is enrolled, only chime into a session a
member opened. That is what §9.3 says and what the enum anticipates; it is
recorded here as the open question it is, not resolved.

## The endpointer seam and the first implementation's limits

SPEC §4.5 names Smart Turn v2 over STT partials. That model is not wired,
so `listen.Endpointer` is a seam — `Feed(pcm) Boundary`, `Reset()` — and
`listen.Energy` fills it: an utterance starts on the first chunk at or above
`DefaultSpeechEnergy` (−40 dBFS RMS) and ends after `DefaultSilence` (800 ms)
of consecutive quiet. Both are bytes of device audio, not durations, for the
same reason `stt.DefaultPartialEvery` is (ADR-0024): a stalled uplink then
produces no boundary rather than a spurious one, and a test drives the
endpointer by writing audio, with no clock. A `DefaultLeadIn` of 300 ms
before the first loud chunk is prepended, so an onset the threshold missed
still reaches STT.

The limits are the ones a level detector has. It knows nothing about words,
so "turn off the… uh… kitchen lights" with a pause past 800 ms is two
utterances; it cannot tell the television from a person, which the gate's
speaker stage does instead; and both constants are unmeasured against a real
room — the `vad`-stage `barge_in_rejected` records and the utterance blobs
are what measures them. `Feed` is called from the bridge read loop, so a
model-backed endpointer must run its inference off that loop and report on a
later `Feed`; one chunk of lag is 32 ms.

## Hand-off: inline audio, one goroutine per utterance, turns in order

`OnMic` runs on the bridge read loop and must not stall it. Audio is appended
inline — `stt.Utterance.Write` is a lock and an append, built for exactly
that call site (ADR-0024) — and the endpointer is arithmetic. Everything slow
runs on a goroutine the utterance owns: the final decode, the embedder, the
blob store, and the turn.

The turn is the hard part, because `Session.Heard` runs the whole turn and
the session has one Thinking child, so turns must run one at a time and in
the order spoken. There is no queue to bound: each utterance holds a ticket
and waits on the previous utterance's before it is heard, and the ticket
closes on every exit, so an aborted utterance never stalls the one behind
it. The backlog that can form while a turn is wedged is bounded by the mic
itself — an utterance cannot arrive faster than it is spoken, 32 KB/s — and
a wedged turn is the defect the session's tool timeouts and silence backstop
exist to prevent. A count-bounded channel that dropped utterances was
rejected because the journal has no kind for "heard and never transcribed",
so the drop could only be silent, and silent loss of what the person said is
the failure the log exists to make impossible. If a kind for it is ever
added, the bound can be, too.

Candidates are offered while the Speaking child is live and only then:
`Session.Children()` is the truth about that, and a `barge_in_detected` with
nothing playing would record a cut that never happened. One candidate per
partial, each with its own blob (`barge_in_rejected` is audio-bearing), the
DAC position from `Played` frames less the speech's own base (below), the
speaker from the resolver on the audio so far, and the RMS so far as energy;
the gate decides and journals
(ADR-0004). After a detection the utterance offers no more.

## The embedding on the trace record

SPEC §5 stores the embedding on every trace record; ADR-0025 left the field
for this change. It is `embedding_json` on `utterance_transcribed`: the
vector as a JSON array of numbers in a string field, absent — not empty —
when the embedder was unavailable. A string because `journal.Record.Fields`
is `map[string]string` and the schema's `#Param` types are JSON scalars;
inline rather than a blob beside `audio` because the size does not justify
indirection — 192 float32s print to about 1.7 KB, and a household's few
hundred utterances a day is a few hundred KB, against ~115 MB/day of audio
(SPEC §8) — and because the clustering it exists for is a SQL question over
Postgres JSONB (`fields->>'embedding_json'`), which a blob per event would
turn into a file walk. Nothing generated changed: schemagen renders required
fields and the kind table only, so the field is documented in the CUE and
here.

## Alternatives rejected

Opening the session on the wake frame and flipping attribution on the first
utterance. Loses migration for every wake and journals a `session_opened`
that stage two or three then has to undo, which the schema has no event for.

Wrapping the listener around the satellite so one handler sees `Played`.
`satellite.Config` has no `OnPlayed` hook, so the listener tracks its own copy
of the position from the same frames under the same monotonic rule.

## The origin a candidate's position is measured from

`PLAYED` counts the whole connection, so the cumulative count is not an
answer to "how far into this speech was the person cut off". `Candidate.PositionMS`
has to share an origin with `speech_truncated`'s `frames_played` or a pair
cannot be reproduced from the record, and after the first utterance of a
connection the two numbers would otherwise disagree by everything spoken
before.

`Satellite.SpeechBase` is that origin: the position the newest utterance's
frames count from, which the satellite already subtracted privately, now
read by `listen.Config.Playback`. It is kept after the utterance ends rather
than cleared, so a partial that arrives just behind the cut still measures
against the speech it interrupted. The two sides hold separate locks, so the
subtraction is clamped at zero.
