# 0041. Hold a cover that lets someone in, by the class Home Assistant gives it

- **Status:** accepted
- **Source:** SPEC §6 · ADR-0027, ADR-0038

Refines ADR-0038, which it does not supersede: a held call, its nonce and
the log's verdict stand. ADR-0038 left one gap open. `cover.open_cover`
opens the garage and the living-room blinds alike, so the garage was not
held. Telling them apart needs the cover's `device_class`, which Home
Assistant keeps on the entity. Now the session reads it before a cover
call is dispatched, and a garage, a gate or a door waits for a yes.

```go
import (
	"github.com/teaganglenn/chorus/internal/registry"
	"github.com/teaganglenn/chorus/internal/session"
)

// The same call, held for the garage and not for the blinds.
func onlyTheGarageWaits() bool {
	ha := registry.Specs["ha_call_service"]
	open := `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`
	is := func(class string) registry.Classes {
		return func() ([]string, error) { return []string{class}, nil }
	}
	return ha.NeedsConfirmationOf(open, is("garage")) && !ha.NeedsConfirmationOf(open, is("blind"))
}

// What the session asks a tool that can say what a call acts on.
var _ session.Classifier
```

**An entry can name target classes.** A `confirm_when` entry may carry
`target_class`, a non-empty list. It matches a call whose arguments match,
and whose target has one of those classes. CUE rejects an empty list, since
it would hold nothing. `ha_call_service` holds four such calls on a cover
classed `door`, `garage` or `gate`:

- `cover.open_cover`
- `cover.toggle`, which opens a shut garage
- `cover.set_cover_position`, which opens one partway
- `homeassistant.toggle`, which reaches `cover.toggle`

Home Assistant's cover documentation describes those three classes as the
doors, garage doors and gates that give access to an area. `homeassistant.turn_on` is not listed,
because Home Assistant 2026.10 answers it for a cover with "does not support
entities" and does nothing. The demo instance `internal/hass/models_test.go`
stands up was checked by hand for this.

**The executor reads the class.** A tool that can say what a call acts on
implements `session.Classifier`. The session asks it only when an entry
naming classes has matched the arguments, and at most once per call. The
kitchen lights and the front door cost no extra request. For
`ha_call_service`, the class is the `device_class` attribute of the entity
the call names, read with `GET /api/states/<entity_id>`. A cover with no
class, like the demo's windows, has none and is not held. Against the demo
instance, the read took about a millisecond.

**A target that cannot be read is held.** These all hold the call:

- An `area_id` target, because REST does not list an area's entities
  (`internal/hass/client.go`). "Open the blinds in the living room" by
  area asks first.
- A target inside `data` (`entity_id`, `area_id`, `device_id`, `floor_id`,
  `label_id`).
- An entity Home Assistant does not have.
- Home Assistant being down, or not answering within the tool's timeout.
- A turn barged in on during the read.

The executor also refuses a target inside `data`. Home Assistant acts on
targets there beside the one the gate read, so a yes to the blinds could
otherwise open the garage too.

**A call that presents a nonce goes to the log, not to the target.** The
call that comes back with its nonce is decided by `State.Redeemable` alone,
and its target is not read again. A cover's class can change between the
question and the answer: Home Assistant was down and is back, or someone
reclassed the cover. Reading it again would let the yes fall through as an
unheld call, with the nonce still in its arguments.

**The read has the tool's timeout.** It runs before the call is
dispatched, so it is bounded by the tool's declared `timeout_ms` on the
session's clock, as the call itself is. A read that times out holds the
call.

## Alternatives rejected

- **A list of dangerous entity ids in Chorus's config.** Home Assistant
  already says what each cover is, and the household sets it there ("Show
  as" in its UI). A second list would drift from the first the day a cover
  is renamed.
- **Match on the entity id's text, `cover.garage_*`.** An id is a name a
  person chose, and `cover.door_2` is somebody's garage.
- **Hold every cover call.** Blinds open and close several times a day, and
  a question every time teaches the household to say yes without
  listening.
- **Resolve an area through the template API.** `POST /api/template` with
  `area_entities()` would read an area's covers over REST. It is a Jinja
  evaluator reachable by the model's arguments, and the websocket
  registries are the supported way to read areas.

## Forecloses

- **A garage that Home Assistant does not class as one is not held.** That
  includes a cover with no `device_class`, a cover group, and an opener
  exposed as a `switch` or a `button`. The fix is in Home Assistant: give
  the cover its class, or wrap the switch as a cover with "Change device
  type of a switch".
- **The class is read live and not journalled.** A replay reaches the same
  verdicts, because a hold is in the log as `confirmation_requested`. Why
  a call was *not* held is not in the log, only that it ran.
- **Covers targeted by area always ask.** Areas become readable only when
  the client opens the websocket, which ADR-0027 deferred.
