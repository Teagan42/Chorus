# 0063. The yes is the asker's, and a guest's yes runs nothing

- **Status:** accepted
- **Source:** SPEC §5, §6 · ADR-0038, ADR-0041, ADR-0049

Refines ADR-0038 and ADR-0041, which it does not supersede: a held call,
its nonce, the log's verdict and the cover's class all stand. ADR-0038's
Forecloses section admitted that the answer was not required to come from
the person who asked, nor from an identified voice. So anyone in the room
could say yes to someone else's unlock: a dinner guest the household never
enrolled, or a television the gate let through because nothing was
playing. Now the log says who asked, and the yes must be theirs; a voice
that matched nobody confirms nothing. ADR-0041 also left the generic
`homeassistant.turn_on` ungated on the entities whose own services are
held. Now it is held by what it acts on.

```go
import (
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/registry"
)

// Alice's yes to Teagan's question, and a dinner guest's, are refused.
func notTheAskers(st journal.State, nonce, args string) bool {
	r := st.Redeemable(nonce, "ha_call_service", args)
	return r == journal.RefusedWrongPerson || r == journal.RefusedGuest
}

// The front door under the generic service is held as lock.unlock is.
func frontDoorHoweverItIsCalled() bool {
	ha := registry.Specs["ha_call_service"]
	lock := func() (registry.Target, error) { return registry.Target{Domain: "lock"}, nil }
	return ha.NeedsConfirmationFor(`{"domain":"homeassistant","service":"turn_on","entity_id":"lock.front_door"}`, lock)
}
```

**The asker is derived, not recorded.** Each held call's confirmation
carries `AskedBy`: the speaker the log attributes the last thing heard
before the call to, through the one function the session and the reducer
share (`journal.Attribute`, ADR-0049). No event gained a field; the turn's
speaker was already in the log. A fresh nonce handed out on a refusal keeps
the first asker, whoever's turn the refused try was made in. Alice's
refused yes to Teagan's question does not make it Alice's question.

**The answer is attributed the same way.** `AnsweredBy` is now who the log
attributes the first utterance after the question to, and `AnswerMatch`
how that voice matched. `State.Redeemable` adds two refusals to ADR-0038's
five:

- `guest`: the answer's `speaker_match` is `below_threshold` or
  `ambiguous`. The voice was judged and is nobody the household enrolled,
  so it is a guest's whoever asked, and a guest's yes runs nothing.
- `wrong_person`: the asker was identified and the answer is attributed to
  someone else.

A refused nonce is spent and a fresh one handed out, as before. The model
is told the situation and nobody's name: "someone else answered", or "a
voice the household has not enrolled". Who else is in the room is not the
model's to know.

**A household nothing identifies works as it did.** A `speaker_match` of
`nobody_enrolled`, or none at all, leaves the answer with the current
speaker (SPEC §5): with nobody enrolled there is no one to tell apart, and
a voice nothing judged is no evidence of anyone else (ADR-0031). So a
house without the speaker-ID sidecar, and a one-off embedder failure in a
house with it, read the next thing said as the answer, as ADR-0038 did.
Teagan asked, something she said was not judged, and it is still hers.

**The generic services are held by their target.** A `confirm_when` entry
may name `target_domain` beside `target_class`; both are read by the
executor, as `session.Classifier` now answers with a `registry.Target`,
the entity id's domain beside its `device_class`, in the same request as
before. `homeassistant.turn_on`, `turn_off` and `toggle` are held on a
`lock` or an `alarm_control_panel` by domain, and on a cover classed a way
in by class, as the domain's own services are. ADR-0041 left `turn_on` off
because Home Assistant 2026.10 does nothing with it on a cover. The gate no
longer reads Home Assistant's dispatch table: a question costs one
sentence, and a version that starts honouring the call costs a door.
`valve.open_valve` is held outright. A target whose domain nothing read is
held, as one whose class could not be read already was.

## Alternatives rejected

- **Require a voiceprint match on every yes.** ADR-0038 feared a "yes" too
  short to match. The rule refuses only a voice that was judged and
  matched nobody; a voice nothing judged keeps the asker, so a household
  without speaker identification is not locked out of its own door.
- **Make the fresh nonce the answerer's question.** Then Alice saying yes
  twice would unlock what Teagan asked, and the rule would hold for one
  round only.
- **Record `asked_by` on `confirmation_requested`.** The reducer already
  attributes the asking turn; a second copy of the speaker could disagree
  with the first, and a log from before the field replays with none.
- **Fold the entity's domain into its classes.** `target_class` means
  `device_class`, and a `binary_sensor` classed `lock` is not a lock. Two
  fields keep the two facts apart.
- **Hold `script.*`, `button.press`, `switch.*` and `automation.trigger`.**
  Nothing in the call or the entity says what they do; a garage opener on
  a relay and the porch light are the same `switch.turn_on`. Holding every
  switch teaches the household to say yes without listening (ADR-0041),
  and the fix is in Home Assistant: wrap the relay as a cover with its
  class.

## Forecloses

- **A guest cannot confirm their own request.** A guest asking to unlock
  the door is held, their yes is refused, and only a household member's
  yes runs it. Whether a guest should be able to ask at all is ADR-0030's
  open question.
- **A household member may answer a question nobody owned.** When the
  asker was a guest, or nobody was identified, any identified yes runs the
  call. The answer is still never a guest's.
- **An unjudged yes is the asker's.** A house whose embedder fails on one
  utterance redeems on it, as ADR-0049 attributes it. A house that wants a
  voiceprint on every yes needs the sidecar up.
- **A yes too short to match is refused.** The model asks again with a
  fresh nonce; the person answers in a full sentence. How often that
  happens is in the log as `AnswerMatch`.
- **Scripts, buttons, switches and automations stay open**, and so do
  `valve.toggle` and `valve.set_valve_position`.
- **A generic service on a lock asks even where Home Assistant would do
  nothing.** The question is the cost of not reading the dispatch table.
