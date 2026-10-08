# 0027. Act on the home through a detached Home Assistant service call

- **Status:** accepted
- **Source:** SPEC §6, §4.4, §7, §14 item 5 · commits `49b18c7`, `cfa6d51`, `010863d`

Phase 1 needs one real tool to demonstrate speak-while-tooling against, and
SPEC §6 names Home Assistant as the adapter that exposes entities, areas, and
scripts. The adapter is `internal/hass`, over HA's REST API, and it declares
three tools in `schema/tool.cue`: `ha_call_service` to act on the home,
`ha_get_state` to read one entity, and `ha_find_entities` so the model never
has to guess an entity id. Nothing else — the point is a real side effect
under the supervisor, not coverage of HA's service catalogue.

## The interrupt policy is `detach`

A service call is committed the moment the request leaves. HA has no undo, and
the orchestrator cannot tell from a cancelled request whether the lights
changed. Each of SPEC §4.4's three policies was weighed against that:

- `cancel` would abort the HTTP request on barge-in. The request has usually
  already been read by HA, so the side effect happens anyway, and the journal
  records `cancelled` — a lie the model then reasons from. "Did the lights go
  off?" has no honest answer in the log.
- `uninterruptible` is what the side effect looks like from the outside, but
  the schema couples it to `requires_confirmation` (ADR-0012), and "turn the
  lights off" must not come back as a question. That coupling is correct for
  the tools it was written for; it is the wrong default for the common case
  here.
- `detach` lets the call finish on the session's context, and keeps the result
  marked as having landed after the cut. The work is wasted only in the sense
  that nobody was waiting for it; the model still sees what the home did, which
  is the §4.4 principle of recording the truth.

```go
import "github.com/teaganglenn/chorus/internal/registry"

// A service call runs to completion across a barge-in and keeps its result.
func serviceCallDetaches() bool {
	return registry.Specs["ha_call_service"].OnInterrupt == registry.InterruptDetach
}
```

The pin is tested: `schema/testdata/ha_call_service_cancelled.cue` redeclares
the policy as `cancel` and `cue vet -c` must reject the conflict, so the
policy cannot be loosened without the schema test noticing.

Some services do warrant confirmation — `lock.unlock`, `cover.open_garage`,
`alarm_control_panel.disarm`. The registry is per-tool, not per-service, so a
per-service confirmation gate is a new mechanism (a declared allow-list the
orchestrator checks before dispatch, under the confirmation flow of ADR-0011).
It belongs with phase 2's confirmation gates (SPEC §14), and nothing here
forecloses it: the gate would sit in front of the same tool.

## Discovery is a tool, not prompt inventory

The model cannot call a service on an id it has never seen. Two places the
inventory could live: a `ha_find_entities` tool, or the system prompt. The
prompt is a versioned artifact (SPEC §13) and every version is recorded per
event, so an inventory in it would make every renamed light a new prompt
version and every trace incomparable to the last. A tool costs one round trip
on the turns that need it and keeps the prompt stable. The result is bounded
and sorted, so a page of a real home reads the same way every time.

## Areas, honestly

`ha_call_service` accepts `area_id` because HA's service data does, and the
satellite already contributes its room as turn metadata (SPEC §5).
`ha_find_entities` does not filter by area: `/api/states` does not carry area
membership. That lives in HA's entity and device registries, which only the
websocket API exposes. Rather than invent an endpoint, the tool filters by
domain and name, and area filtering waits for a websocket client.

## Argument checking is read from the registry

The handlers in `internal/hass` do not define their own argument structs. They
read the generated `registry.Specs` entry at call time, reject unknown fields,
missing required ones and wrong types before anything reaches HA, and a test
asserts that every `ha_` tool declared in CUE is implemented and nothing else
is. One declaration, one implementation, no second list to drift (SPEC §6).

## Alternatives rejected

A single `ha_execute` taking a free-text intent for HA's own conversation
agent. It would hide the side effect behind HA's interpretation of a sentence
the orchestrator had already transcribed, and the result would not say which
entity changed.

Declaring `ha_call_service` slow. HA answers a light in well under a second;
telling the model it takes several seconds (ADR-0011's latency hint) would
make it speak an acknowledgement before every switch, which is the wrong
rhythm for "lights off".

## Forecloses

Nothing in the registry. What is deferred: per-service confirmation; the
websocket API, and with it the area registry, state subscriptions and
`ha_find_entities` by area; scripts beyond `script.turn_on`, which already
works through `ha_call_service`; and any reading of `return_response` data,
which only some services produce and none of these three tools needs.
