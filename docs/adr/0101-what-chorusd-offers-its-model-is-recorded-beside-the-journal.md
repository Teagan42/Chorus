# 0101. What chorusd offers its model is recorded beside the journal

- **Status:** accepted · refines [ADR-0071](0071-an-export-row-carries-the-context-its-turn-ran-under.md) and [ADR-0075](0075-an-mcp-tool-is-offered-under-conservative-policy-fixed-at-startup.md)
- **Source:** SPEC §6, §8, §9.1, §9.2, §13, §14 · ADR-0059, ADR-0060, ADR-0071, ADR-0075, ADR-0079

ADR-0075 hashed the household's MCP tools into the tool-schema version every
event records, and listed what that cost under Forecloses: the text behind
such a version is in no build and no archive, so an export row of a turn
that offered an MCP tool said `context_missing: tool_schema`, `chorus-eval`
skipped any case on one, and a review-UI re-run asked the turn without the
server's tools it had. ADR-0071's labels resolve only what a build of this
repository can name, and the house's servers are not in the repository.

## chorusd records its offer at every start

`internal/contexts` gains a store. Once the journal is migrated, `chorusd`
writes the persona, the summary prompt hashed beside it, and the tool
declarations it offers its engine, the servers' tools among them, into
`context_versions`, one row per label.

```go
import (
	"context"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
)

// What a chorusd with no MCP server offers, kept so its labels resolve
// wherever its journal is read.
func record(ctx context.Context, kept contexts.Store) error {
	return kept.Record(ctx, contexts.Offer{
		Prompt:     ollama.DefaultPrompt,
		Summary:    ollama.SummaryPrompt,
		ToolSchema: ollama.ToolSchema(registry.Offered()),
	})
}
```

- **The label is the text's.** `Offer.Versions` hashes the text by the rule
  the archive's tests hold it to: the persona with the summary prompt, and
  the declarations compacted as the wire carried them. `chorusd` refuses to
  start when that is not the label its engine stamps, rather than file text
  under a label that never named it. A test builds both engines with a
  server's tool and checks each one's labels resolve.
- **A row is never rewritten.** A label already kept is left as it is; a
  restart with the same servers adds nothing. Nothing is pruned: a row is a
  few kilobytes per distinct offer, and retention (ADR-0065) keeps turns
  that may name any of them.
- **A row that does not hash is never read.** One edited by hand reads as
  never kept, so its turns say `context_missing` rather than ship a context
  the model was not sent (ADR-0071's rule).
- **Every start records, MCP or not.** A dev build's labels, which
  ADR-0071 called the expected case of `context_missing`, now resolve
  wherever its journal is read.

Startup fails when the record cannot be written, as it does when a
migration fails: the database is the journal's, and a process that cannot
write it is not running.

## Readers chain it last

The review UI's export, `cmd/harvest` and `chorus-eval` read the build's
context, then the archive, then the recorded versions
(`contexts.Chain{contexts.Default(), kept}`). Content-addressing makes the
order matter only for speed: two sources cannot disagree about a label.
The review UI and `harvest` migrate the table too, so a review box that
starts before any `chorusd` of this build reads it as empty.

## A re-run carries the house's tools

A re-run is still offered the build's tools, so a reviewer sees what the
current build would do (ADR-0060), plus every tool the turns' recorded
schemas declare whose name opens with `mcp_`, the prefix only a server's
tool has (`registry.ProvidedPrefix`; a test holds the registry to it). A
declared tool the turn had and this build dropped stays dropped: the
build's tools are the build's to change, and the house's are the house's.
The editor starts from that schema, the diff is against it, and the note
beside the editor says how many MCP tools it carries. A turn whose schema
nothing holds, such as one journalled before this, is named there, and its
MCP tools are missing unless the reviewer pastes them in (ADR-0059).

`chorus-eval`'s candidate does the same per case (`Side.HouseTools`),
beside the registry's tools or the `-tools` file's, so a case on an MCP
turn scores the candidate rather than the server it was not given. The
baseline asks under the recorded context, which now resolves.

## Alternatives rejected

- **Journal the schema text.** The journal is per conversation (SPEC §8)
  and the offer is the process's; every conversation would repeat several
  kilobytes the label already names. ADR-0071 kept the journal unchanged
  because a label is enough once its text is kept; this is where it is kept.
- **Archive MCP versions in the repository.** The servers are the
  household's, so their tools cannot be filed by a build, and the
  repository is not the household's data.
- **Offer a re-run exactly the recorded schema.** That would also bring
  back declared tools a later build changed or dropped, and a re-run exists
  to ask what the current build would do (ADR-0060).

## Forecloses

- **The record lives in the journal's database.** An export taken from a
  copy of the journal without `context_versions` says `context_missing`
  for MCP turns, as before.
- **Readiness still does not count MCP servers** (ADR-0075).
- Resources, prompts, sampling, elicitation, OAuth and the servers'
  progress notifications are still not built.
