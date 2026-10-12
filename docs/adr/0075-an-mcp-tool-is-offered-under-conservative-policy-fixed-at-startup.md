# 0075. An MCP tool is offered under conservative policy, and the offer is fixed at startup

- **Status:** accepted · refines [ADR-0011](0011-registry-owns-policy.md) and [ADR-0060](0060-a-timeout-leaves-outliving-work-running-and-each-edge-of-a-session-is-journalled.md)
- **Source:** SPEC §4.4, §5, §6, §7, §8, §13, §14 · ADR-0011, ADR-0027, ADR-0038, ADR-0039, ADR-0060, ADR-0063, ADR-0071

SPEC §6 makes MCP one provider among several, with the registry owning what
MCP has no word for, and asks for conservative defaults; §14 listed the
provider as not built. `internal/provider/mcp` builds it: it connects to the
household's MCP servers, offers each tool they list to the model beside the
registry's, and runs the calls. A server is code chorusd did not write,
acting on whatever its credentials reach, described in prose chorusd did
not write, so every default below is the one that does least when it is
wrong, and only the household's configuration relaxes one.

## The client

The official Go SDK, `github.com/modelcontextprotocol/go-sdk` v1.8.0, its
newest release, from 4 September 2026 (Apache-2.0 and MIT). It speaks both transports a house
runs: stdio, to a command chorusd starts, and Streamable HTTP, to a URL. It
delivers `notifications/tools/list_changed`, honours cancellation by
sending `notifications/cancelled`, and ships in-memory transports, so the
tests drive a real server in process. A hand-written JSON-RPC client would
have to track the protocol's revisions itself; the SDK negotiates them.

## The defaults

```go
import "github.com/teagan42/chorus/internal/provider/mcp"

// Every MCP tool, until the household says otherwise.
var defaults = mcp.DefaultPolicy()
```

| Policy | Default | Why |
|---|---|---|
| name | `mcp_<server>_<tool>` | Lowercased, anything outside `[a-z0-9_]` made `_`, at most 64 characters. The prefix keeps it clear of every native and `ha_` tool; a server's name has no underscore, so the name splits one way. Two tools that become one name are both refused, rather than whichever the server listed last. |
| `scope` | `person`, `unknown_speaker: deny` | A guest, or the television, runs nothing on a server that may hold someone's mail or calendar (SPEC §5). With speaker identification off everyone is a guest, so a house without it must relax this deliberately. |
| `requires_confirmation` | `true` | Every call is held for the asker's yes, by ADR-0038's nonce and ADR-0063's rule that the yes is the asker's. A read-only tool is the household's to wave through: MCP's `readOnlyHint` is the server's claim about itself, and a claim never relaxes a gate. |
| `on_interrupt` | `cancel` | A barge-in or the timeout cancels the call, and the SDK tells the server to stop: nothing chorusd started is left running behind the household's back. `detach` and `uninterruptible` would keep work going nobody now waits for. As ADR-0027 found for Home Assistant, a cancelled call may still have happened; `cancelled` means chorusd stopped waiting and asked the server to stop, and a household that knows a tool's effect is committed when sent may set `detach`. |
| `timeout_ms` | `5000` | Half the registry's default: a household waiting on a server it knows nothing about hears silence. Under `cancel`, the timeout ends the call (ADR-0060), so no MCP call outlives it by default. |
| `latency` | `fast` | `slow` makes the model speak an acknowledgement on every call (ADR-0039), which only the household can say a tool deserves. |

The tool's description is the server's, bounded at 1024 bytes, as is each
parameter's. Its results are its structured content when it gives one, else
its text, bounded at 16 KiB; an image or a sound is named, not inlined. A
result the server marks `isError` is `tool_error`, a protocol error is
`mcp_error`, a server that is not connected or drops mid-call is
`mcp_unavailable`, and each is a result the model reasons about (SPEC §7).

## Overrides

`CHORUS_MCP_CONFIG` names a YAML file; unset, chorusd talks to no server.
[`mcp.example.yaml`](../../mcp.example.yaml) is the reference, and a test
loads it. Each server is a `command` (run with only `PATH`, `HOME` and its
own `env`: chorusd's environment holds the journal's and Home Assistant's
credentials) or a `url` with `headers`. `${NAME}` in an env or header value
is read from chorusd's environment, and one that is unset stops startup
rather than sending an empty token. A `policy` under the server relaxes or
tightens every tool it lists; one under `tools.<name>`, by the server's own
name, does so for that tool, and `offer: false` hides it. Fields are known:
a misspelt `requires_confirmaton` stops startup instead of leaving the
default in place unnoticed. Every combination is checked against
`tool.cue`'s rules at load, through `registry.Provide`, so an
`uninterruptible` tool without a yes fails at startup, not on a call.

`registry.Provide` derives a provided tool's model view as schemagen derives
a declared one's (the nonce on a confirmable tool, the acknowledgement on a
slow one, the hints in the description), and a test finishes every
generated tool from its declaration and gets back what was generated, so
the two cannot drift. The one rule it relaxes is a parameter's name: a
provided tool's arguments reach its server as written, so `pullNumber`
keeps its camelCase.

## Schemas

The model's wire carries a flat parameter list: a type, a description,
string enums and an array's item type. A parameter whose JSON Schema says
more (a nested object's fields, a union, a range, a number enum) is offered
as its nearest wire type with the rest appended to its description as
compact JSON, so the model is told what the server will check. The server
validates what it is sent. A tool whose schema still cannot be held, such as
one declaring a parameter named `confirmation`, is refused with a log line,
and the server's other tools are offered.

## The offer is fixed at startup

ADR-0060 made the offered set a property of the build so the tool-schema
version every event records (SPEC §8) means the same thing everywhere. An
MCP server's tools are a property of the house, and of the moment, so they
cannot be. The rule kept is the one SPEC §8 needs: the version names
exactly what the model was sent. `chorusd` stamps one version per process,
so it fixes the offer once:

- **Startup waits for discovery.** Every server gets up to
  `discovery_timeout_ms` (10 s) to list its tools, retrying on a backoff
  meanwhile, so a container started beside `chorusd` is waited for. What the
  servers listed by then is offered, beside the registry's, and hashed into
  the version.
- **A server that missed the window offers nothing until the next start.**
  It is retried, from 1 s doubling to a minute, and the log says when it
  answers that a restart offers its tools.
- **A server that drops is reconnected** on the same backoff. Its tools stay
  offered and answer `mcp_unavailable` meanwhile; a call it dies during
  answers the same, never a hang.
- **A changed list is reconciled for calls, not offers.** On
  `list_changed`, and on every reconnect, the tools are listed again. A call
  to an offered tool the server no longer lists answers `mcp_tool_gone`
  without reaching it; tools it added are logged as offered from the next
  start.

The server's name is in each tool's name, so `tool_called` and `tool_result`
record which server ran a call without a schema change. Connections are
logged, not journalled: the journal is per conversation, and a server's
state is the process's, while each call it fails is already a result in the
log.

## Alternatives rejected

- **Change the offer when the list changes.** The journal stamps versions
  per process (`journal.New`), the session enforces from a fixed map, and
  the engine hashes its tools once. Making all three move together means a
  version per append and a turn pinned to the tools it asked with; a race
  between them records a version the model was not sent.
- **`detach` by default.** A barge-in would leave the call running against
  a server nobody chose to trust with it, and its result landing later is
  something the household did not ask to wait for.
- **Trust the server's annotations.** `readOnlyHint` and
  `destructiveHint` are what the server says of itself. They may inform a
  household's configuration; they never relax the defaults.
- **Keep the server's tool names.** They may collide with each other
  across servers, with a native tool, or break `^[a-z][a-z0-9_]*$`.
- **Refuse any tool whose schema the wire cannot carry.** Nested objects are
  common, and the server validates its own arguments; describing the rest is
  better than offering nothing.

## Forecloses

- **The tool-schema version varies by household.** Two houses on one build
  record different versions once either configures a server. The text
  behind an MCP version is not in the repository's context archive
  (ADR-0071), so an export row of a turn that offered MCP tools says
  `context_missing` for its tools until something records the offered
  schema beside the journal.
- **Replay offers the build's tools.** The review UI re-runs under
  `registry.Offered`; a turn that called an MCP tool is re-run without it
  unless the reviewer pastes its declaration into the schema editor
  (ADR-0059).
- **A server's tools change only on restart.** A household adding a tool to
  a running server restarts `chorusd` for the model to see it.
- **Readiness does not count MCP servers.** `/readyz` (ADR-0068) neither
  fails nor degrades for one; the log says.
- Not built: resources, prompts, sampling and elicitation, OAuth, and the
  server's own progress notifications.
