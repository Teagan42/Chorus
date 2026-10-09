# 0038. Hold a call for the person's yes, and redeem its nonce from the log

- **Status:** accepted
- **Source:** SPEC §6, §8 · ADR-0011, ADR-0027, ADR-0037

ADR-0011 settled that confirmation is enforced by the orchestrator and
phrased by the model. Nothing enforced it. `requires_confirmation` was
generated into the registry and read by nobody, and ADR-0027 left the calls
that need it most to this decision: `ha_call_service` unlocks the front door
with the same arguments it uses to turn on the kitchen lights. Now a call
that needs a yes is held until the person has answered, and the log decides
whether it may run.

```go
import (
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/registry"
)

// Unlocking the front door waits for a yes; the kitchen lights do not.
func frontDoorWaits() bool {
	ha := registry.Specs["ha_call_service"]
	return ha.NeedsConfirmation(`{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`) &&
		!ha.NeedsConfirmation(`{"domain":"light","service":"turn_on","entity_id":"light.kitchen"}`)
}

// A model calling straight back with its nonce has asked nobody.
func selfConfirmed(st journal.State, nonce, args string) bool {
	return st.Redeemable(nonce, "ha_call_service", args) == journal.RefusedNotAnswered
}
```

**Which calls are held.** Every call to a tool declared
`requires_confirmation`. On any other tool, a call whose arguments match one
of its `confirm_when` entries. An entry names string parameters and the
values that make a call dangerous. `ha_call_service` holds three:

- `lock.unlock`
- `lock.open`
- `alarm_control_panel.alarm_disarm`

The match ignores case, because Home Assistant folds it. CUE rejects an entry
that names a parameter the tool does not take, since such a gate would never
match.

Arguments that cannot be read are held as well. A gate that opens on broken
JSON is not a gate.

**Held means not run.** The call gets a `tool_result` with the outcome
`confirmation_required`. Its result carries a fresh nonce and says what to do
with it. A `confirmation_requested` event records the nonce against the call.
The session then asks the model again, as it does after any tool result
(ADR-0037). The model reads the outcome and phrases the question itself.

**The log decides whether a nonce is good.** The model calls again with the
same arguments plus `confirmation`. The session then replays the conversation
and asks the derived state, `State.Redeemable`, whether the nonce may run
this call. It may when all of these hold:

- It was handed to a call to the same tool with the same arguments, compared
  without the nonce and with keys sorted.
- It has not run anything yet.
- The person has said exactly one thing since it was handed out. The call is
  in the turn of their answer.

A `confirmation_given` event spends the nonce before the call runs, so a
replay reaches the same verdict. If that write fails, the call does not run,
because a nonce the log has not spent could be spent twice. The tool is
invoked with the nonce stripped, since the nonce belongs to the orchestrator.

**A refused nonce gets the reason and a fresh nonce.** A refusal is one of
`unknown`, `used`, `args_changed`, `not_answered` or `expired`. The refused
nonce is spent: a nonce gets one try. Left open, it would count the next
answer too. A model that switched from the front door to the back door
before anyone answered could then open the front door on a yes to the
back-door question. Two cases matter most:

- **A model that calls straight back with its own nonce** is refused as
  `not_answered`, every time, until the round cap ends the turn.
- **A yes to the front door used on the back door** is refused as
  `args_changed`.

**The audit is derived.** `State.Confirmations` keeps each held call:

- its nonce
- the first thing said after it, and who said it when they were identified
- the call that redeemed it

Answering "did the person actually say yes" is a replay.

**The model is told.** schemagen adds the `confirmation` parameter to every
confirmable tool, and a sentence to its description saying how to finish a
held call. A tool may not declare a parameter of that name itself.

## Alternatives rejected

- **The orchestrator asks the question and listens for "yes".** It would
  own the dialogue and need its own idea of what counts as a yes in every
  phrasing. SPEC §6 gives the wording to the model.
- **Decide in the session from what it remembers.** The session keeps no
  authoritative state (SPEC §8). A verdict replay cannot reproduce is not an
  audit.
- **Spend the nonce on the redeeming `tool_called`.** That event is written
  before the check, so the check would see its own call spend the nonce. A
  separate event also says what happened.
- **Let a nonce live for a time window instead of one answer.** A window
  either expires during a real pause or outlives the conversation's topic.
  "The next thing they said" is what a question is answered by.

## Forecloses

- **Whether the answer was a yes is the model's call.** A model that redeems
  on "what's the weather" is not stopped. The audit shows it, with the words
  it redeemed on.
- **The answer is not required to come from the person who asked.** It is
  not required to come from an identified voice either. A "yes" is shorter
  than any take ADR-0029 measured thresholds on, so requiring a voiceprint
  match would refuse the household's own answers. The log records who
  answered whenever identification said.
- **Garage covers are not held.** `cover.open_cover` opens the garage and the
  living-room blinds alike. Telling them apart needs the entity's
  `device_class`, which means a lookup before dispatch, and that is not built.
- **The description sentence and the result's note are unmeasured.** The
  slow-tool hint was measured on two models; these were not. The models tier
  gets `TestARealModelAsksBeforeItUnlocksTheFrontDoor`, but it has not run
  where this was written, which has no Ollama endpoint.
- **The review UI does not show confirmations yet.** Its demo row is still
  invented data.
