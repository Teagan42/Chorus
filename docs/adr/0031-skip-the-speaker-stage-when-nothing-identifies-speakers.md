# 0031. Skip the gate's speaker stage when nothing identifies speakers

- **Status:** accepted
- **Source:** SPEC §4.3, §5 · ADR-0004, ADR-0030 · commit `91a5c0d`, `0f67b1b`

Refines ADR-0004, which it does not supersede: the stacked gate stands, and
this records what its second stage does when there is no speaker identifier
to stack on.

## Context

The daemon's composition exposed a gap. `session.Gate` demanded a known
speaker from every candidate, and the Listening child fills `SpeakerID` from
its resolver, which `cmd/chorusd` builds only when `SPEAKERID_URL` is set
(ADR-0030). Without it every candidate carried an empty id, every candidate
failed at `speaker_id`, and the household could not interrupt the assistant
at all. The daemon logged the fact and nothing else could be done about it
short of running the sidecar.

SPEC §4.3's own reasoning rules that out as the resting state. The speaker
stage is "the high-value filter", not the only one, and the clause weighs
the errors: a wrong stop is cheap, because the model keeps talking, while a
slow stop feels broken. A gate that cannot stop at all is the slow stop
taken to its limit.

## Decision

The gate carries an explicit mode, set by the composition root and nowhere
else: `Gate.SpeakerIDUnavailable`. When it is set the `speaker_id` stage is
skipped and energy and partial length still gate. When it is not set the
stage behaves exactly as ADR-0004 describes: an unknown voice, the
television included, fails it. `cmd/chorusd` sets the mode from whether a
resolver was configured, which is the same condition under which the
listener attributes speakers at all.

```go
import "github.com/teaganglenn/chorus/internal/session"

// The zero value runs the stage, so a gate nobody configured stays strict;
// the composition root opts out, never the candidate.
func gateWithoutSpeakerID(household []string) session.Gate {
	return session.Gate{MinEnergy: 0.01, MinWords: 2, Household: household, SpeakerIDUnavailable: true}
}
```

The mode is never inferred from an empty `SpeakerID` at candidate time. An
unidentified voice while identification is running *is* the television
case, and it is also what the listener yields when the sidecar is up but a
call to it failed (ADR-0030: a resolver that fails yields a guest). Both
must still fail the stage, so only the configuration can switch it off.

## Consequences

A household that runs without speaker identification can be interrupted by
the television, or by anyone loud enough for long enough. That is the
configuration they chose, the daemon says so at startup in those words, and
enrolling someone with the sidecar up restores the filter. The `vad` and
`partial_length` rejections are still journalled in that mode, so the two
remaining stages keep their tuning corpus.

`barge_in_detected` says nothing about which stages ran. A detection with
the speaker stage skipped is indistinguishable in the log from one that
passed it, which the tuning corpus will eventually want to tell apart.
Adding a field is a schema change and is not made here; it is the
follow-up, and until then the startup log line is the only record of the
mode.

## Alternatives rejected

A household-wide "allow unknown speakers" flag, independent of whether
identification is configured. It would stay on with the sidecar present and
quietly undo ADR-0004's filter where it is most valuable; the gap was a
missing capability, not a preference, so the switch belongs to the
capability.

A confidence threshold on the candidate, admitting an unidentified voice
above some energy. Energy is already the first stage; raising it for the
unidentified case would be a second VAD with a different name, and would
still refuse a quiet member of the household with the sidecar down.

Inferring the mode from an empty id at candidate time. Simplest to write,
and wrong: it would admit the television whenever the resolver failed to
name it, which is exactly the case the stage exists for.

## Forecloses

Nothing structural. The mode is a bool because there are two states today;
a third, such as a stage that runs but admits `NobodyEnrolled`, would make
it an enum, which is a rename rather than a redesign.
