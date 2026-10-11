# 0072. A wake several satellites heard is answered by the loudest, and a press beats a voice

- **Status:** accepted · refines [ADR-0022](0022-one-live-session-per-conversation.md) and [ADR-0058](0058-a-rejected-wake-ships-in-its-own-corpus-and-a-repeat-is-evidence-on-its-first-ask.md)
- **Source:** SPEC §4.5, §9.3, §11 · ADR-0006, ADR-0015, ADR-0022, ADR-0030, ADR-0049, ADR-0058, ADR-0060, ADR-0064, ADR-0069, ADR-0073

Two satellites in earshot of one person is ordinary, and nothing chose
between them. Each link's listener armed on its own wake frame
(`internal/listen/listen.go`), so:

- **An identified person was answered by the later satellite.** Whichever
  finished STT and speaker ID last called `Supervisor.Open`, claimed the
  person's conversation and ended the other's session as `migrated`
  (`internal/session/session.go`, ADR-0022). The farther device, whose
  quieter audio might decode slower, could be the one that answered.
- **A guest, or a house with speaker ID off, was answered twice.**
  `Conversations.Open` gives every unidentified wake a fresh conversation, so
  both satellites opened a session and both answered.
- **A detached call could act twice.** `ha_call_service` is `detach`
  (ADR-0027): a first turn cut by the migration still finished, and the
  second satellite then made the same call for the same ask.

## Decision

**The host arbitrates, with no firmware or wire change.** `listen.Arbiter`
holds the house's one open contest. A wake frame opens it, and every wake
frame from another satellite that arrives within the window joins it. Arrival
is the host's injected clock, never the device's, so links whose clocks skew
or whose frames jitter are compared on one timeline. A frame arriving once the
window has passed on that clock opens the next contest, even before the old
contest's timer has been read.

```go
import "github.com/teagan42/chorus/internal/listen"

// The office heard Teagan's "Hey Eddie" fainter than the kitchen did.
func officeEntry() listen.Entry { return listen.Entry{Satellite: "office", Energy: 0.04} }
```

**The loudest processed channel wins.** Each entry carries the RMS of the
satellite's channel 0 over the 600 ms before its wake frame
(`DefaultArbitrationSpan`, about the length of "Hey Eddie"), measured from
per-chunk energies the listener keeps while idle. It keeps numbers, never
audio, so idle audio is still nobody's (SPEC §3.2). The frame and the audio
before it share one TCP stream, so the stretch is the device's own order and
needs no cross-link timing. A tie goes to the earlier arrival, then to the
inventory's order, then to the name: never to chance, so the same arrivals
always pick the same winner.

**A push-to-talk press beats any spoken wake.** The Voice PE's centre button
calls the same `on_wake_word` hook as `micro_wake_word`, with the name
`button` (`bridge.WakeButton`). A press says which device was meant, so it
outranks energy. It still enters the contest and waits out the window like
any wake, so a spoken wake on another satellite a moment later loses to it
instead of opening a contest of its own. Two presses fall back to energy.

**Nobody waits when nobody else can enter.** The arbiter knows which
satellites are linked (`daemon.link` and `unlink`). A contest is decided as
soon as every linked satellite has entered, at once for a house of one, and
when a link that has not entered drops. Only a wake with another satellite
linked and silent waits out the window.

**The window is 200 ms, and it is spent while the person is still
talking.** The window has to cover two things. One is the spread between two
devices' detections: each `micro_wake_word` fires when its own
probability crosses its cutoff, a fainter copy a few frames later, and the
frame then waits for the next 32 ms send. The other is Wi-Fi jitter between
two links. Each is tens of milliseconds, and 200 ms covers both with room to
spare. It costs §11's 700 ms budget nothing in the ordinary case. The
listener holds what the mic sends during the contest, and the winner replays
it through the endpointer and STT in order, so the ask is heard whole from
its first word. Its end comes no sooner than its speech plus the endpointer's
quiet (200 ms for Smart Turn, 800 ms for energy alone), which is always longer
than the window. Measured in the chorusd rig on the virtual clock:

| Case | Wait added to the session opening |
|---|---|
| One satellite linked | 0 ms; no timer armed |
| Both satellites heard it | 0 ms; decided on the second frame |
| Another linked and silent, ask streamed at 32 ms a chunk | 0 ms (opened 1.056 s after the wake, arbitration on and off) |
| Another linked and silent, ask arriving in one burst | 200 ms, the window exactly |

The burst row is a radio stall that lands a whole ask at once; it is the
bound. `CHORUS_WAKE_ARBITRATION_MS` sets the window, and `0` builds no arbiter,
which is the behaviour before this record.

**A loser stays dark and silent, and says who won.** It opens no session, so
nothing lights the ring (ADR-0056). It drops what it held, so its first
utterance is nobody's turn, and its wake is spent. It journals `wake_rejected`
to its device log with the new reason `arbitrated`, `lost_to` naming the
winner, and `energy_dbfs` and `winner_energy_dbfs`, so whether energy picks
the nearer device can be checked from the log. The event needs audio (its
schema `has_audio`), so it carries what the loser held while the contest ran,
under retention like any clip. A new reason on an existing event, not a new
kind: the review UI and the harvester already fold every kind (ADR-0064's
reasoning).

**A wake over a live session still votes.** A satellite already in a session,
or confirming a wake, ignores a second wake as before (ADR-0030), but it still
enters the contest at its level. So when Alan says "Hey Eddie" again in the
kitchen, the office down the hall loses rather than taking his conversation
by migration. A satellite saying an announcement nobody may answer does not
enter, as it never took a wake.

**Arbitration covers simultaneous wakes only.** A deliberate re-wake on
another satellite after the window is its own contest. For the same person
within two minutes it is migration, unchanged (SPEC §4.5, ADR-0022).

## Neither is a hard negative

`harvest.HardNegative` reads a rejection's fields, and `harvest.Negatives`
returns only what it passes. ADR-0058's corpus ships every stage-two
rejection unless a reviewer discards it, so a wrong row would ship unread.

- **`arbitrated`** is the wake word said nearer another device. As a negative
  it would teach the model to miss the word.
- **A press** (`wake_word` `button`) never fired the wake model, so whatever
  became of it (silence, a guest) is no evidence about the model. Every
  rejection now records the name its frame carried as `wake_word`. That
  lifts ADR-0073's foreclosed note that a failed button wake is journalled
  as a wake word's.

Both stay in the device log and on its review page, each with a note in place
of a verdict: *wake lost to kitchen* with both levels, or *a button press*.
Browse draws no rejected-wake tick for an arbitrated wake, since none was
rejected at stage two; a press that failed confirmation still gets its tick.

The ops metrics count no wake outcomes (ADR-0068), so there is nothing for the
new reason to flow through, and nothing parallel is added.

## Alternatives rejected

- **Decide on STT or speaker-ID confidence.** Both arrive only once the first
  utterance ends, a second or more after the wake, and a loser would by then
  have decoded and embedded a whole utterance for nothing. Energy is in hand
  at the frame.
- **The second channel's level.** It skips AGC on the Satellite1, which would
  make levels comparable, but what it carries is the firmware's setting and
  differs by device (SPEC §3.2), so it cannot be compared across a mixed house
  either.
- **Arbitrate in the session layer, at `Supervisor.Open`.** By then both
  listeners have confirmed, and the loser has already decoded, embedded and
  stored the ask. A guest's two sessions never meet there either, since they
  share no conversation.
- **Decide a press at once.** A spoken wake on another satellite a moment
  later would then open a contest of its own and win it, and two devices would
  answer.
- **A grace after a decided contest, losing every late frame to its winner.**
  A second person waking a second satellite a breath later would be silenced.
  A wake past the window is a new contest: arbitration is for simultaneous
  wakes only.
- **A device-side wake score on the wire.** It would be a better proxy, but it
  is a firmware and protocol change; SPEC §9.3's pre-roll is the place for it.

## Forecloses

- **Channel 0 is AGC'd on both devices** (SPEC §3.2), which narrows the gap
  between near and far. The near device still wins on speech-to-noise, but a
  far device in a quiet room can beat a near one beside the dishwasher. The
  recorded levels are how to tell.
- **A frame later than the window from the first is a contest of its own.** A
  link stalled for longer than that still answers a wake the other already
  answered.
- **A muted or announcing satellite is still waited for** if it is linked,
  so a wake beside it pays up to one window, ordinarily hidden behind the ask.
- **`session_opened` does not record `wake_word`.** Only rejections do, as the
  corpus needs.
- **The held audio is bounded by the wake window** (`DefaultWakeWindow`); a
  verdict later than 20 s of audio drops the rest.
