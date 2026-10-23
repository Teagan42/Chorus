# 0102. Every provider declares what it can do, and chorusd refuses a pipeline that cannot work

- **Status:** accepted · builds what [ADR-0013](0013-local-first-model-stack.md) and [ADR-0012](0012-cue-is-the-source-of-truth.md) called for
- **Source:** SPEC §4.1, §4.3, §10, §11 · audits/sidecars-2026-10-10, the 2026-10-10 audit's Missing 11

ADR-0013 put every model behind a provider that declares its capabilities
(streaming? tool calls? interruption?) so that a configured pipeline that
cannot satisfy the architecture is rejected. ADR-0012 named provider
capabilities among what CUE authors. Neither was built: SPEC §10 said
nothing in `schema/` described a provider, and CONTRIBUTING §2 said nothing
checked a pipeline's viability. Eleven provider types later, a household
choosing among two engines, three voices and three ears learned what a
choice cost from the first turn, or never.

## The declarations

`schema/provider.cue` declares each provider under its role, and
`internal/tools/schemagen` generates `internal/provider/capability`'s
`Declared` and `Needs` from it, with
[`docs/reference/providers.md`](../reference/providers.md) beside them.
The roles are the six seams a turn meets: `stt`, `endpointing`,
`speaker_id`, `embedding` (memory recall, ADR-0044), `llm` and `tts`.

| Capability | Roles | Means |
|---|---|---|
| `cancels` | every role | A cancelled request returns at once. A barge-in, a deadline and a discarded speculation all cancel. |
| `tool_calls` | `llm` | The model calls tools natively. `speak` is one (ADR-0003). |
| `streams_text` | `llm` | Words arrive as the model writes them. |
| `streams_tool_arguments` | `llm` | A call's arguments arrive as the model writes them, so a speak call's words reach the voice before the call closes. vLLM's do (ADR-0077); Ollama's arrive whole. |
| `languages` | `stt` | What the ear hears, as ISO 639-1 codes. Empty is whatever the server's model hears. Parakeet on speaches declares `en`. |

What CUE refuses, each with a fixture in `schema/testdata/`: a capability
declared on a role that has no word for it, a provider silent on whether it
cancels, streamed arguments without tool calls, a language that is not an
ISO code, and a need on a capability its role cannot declare.

## The needs

Beside the declarations, `needs` says what the architecture asks of each
role's provider and what lacking it costs. A **required** need refuses the
pipeline: every role must cancel (SPEC §4.3), the engine must call tools
(SPEC §4.1), and the ear must hear the configured `STT_LANGUAGE`. A
**degraded** need is said at startup with its cost: an engine that does not
stream text, or does not stream a call's arguments (SPEC §4.1, §11).

## The check

`buildProviders` collects what each provider it built declares, the ear, the
engine and the voice always and the optional sidecars when configured, and
`capability.Check` holds each to its role's needs. It runs after the
providers are built and before the journal is opened or a satellite heard,
so it touches no network. Every refusal is named in one error, so one
restart fixes them all. Each degradation is one warning line.

```go
import "github.com/teagan42/chorus/internal/provider/capability"

// Parakeet on speaches hears English, and nothing else.
func parakeetHearsGerman() bool {
	return capability.Of(capability.RoleSTT, "speaches").Hears("de")
}
```

Each provider type returns its declaration from `Capabilities()`, and
chorusd's engine, voice and ear interfaces require it, so a provider wired
into chorusd without a declaration does not compile. A provider built at
runtime is a `capability.Declaration` value and is held to the same needs.

**Build time is `task test`.** The household's own environment is only known
when chorusd starts. The repository's is checked on every push:
`.env.example` under `docker-compose.yml`'s overrides, and all eighteen
choices of engine, voice and ear. A schema change that would refuse the
shipped stack fails CI.

**Every claim is held by a test.** Each provider package cancels a request
at a server that never answers and fails unless it returns at once
(`capabilitytest.Cancels`). The streaming claims are held by the engines'
own stream tests.

## What it found

- On the default stack, every start now says that Ollama's speak calls reach
  the voice whole, so first audio waits for the whole call, and that
  `CHORUS_LLM_PROVIDER=vllm` streams them. SPEC §10 calls streamed tool-call
  parsing a hard requirement. The check degrades on it rather than refusing,
  because Ollama is the default that every house runs today.
- `STT_LANGUAGE=de` on speaches used to start and hear German as English,
  since Parakeet accepts a language and ignores it. It is now refused, and
  the refusal names the ears that hear other languages.

## Alternatives rejected

- **Declarations as Go literals only.** ADR-0012 puts provider capabilities
  in CUE. A hand-written reference would rot, and CUE checks the
  cross-field rules, such as streamed arguments implying tool calls.
- **Refuse Ollama for not streaming arguments.** That would stop every
  existing install for a cost the household can live with. Saying it at
  every start is honest; refusing it is a regression.
- **Ask each server what it can do.** Startup touches no network
  (`buildProviders`), and what a server says about itself is not what the
  client code that talks to it can do. Readiness already asks the sidecars
  what they serve (ADR-0097).
- **Declare whether a voice streams.** `satellite.Synth` returns a whole
  sentence's audio, so every voice would declare false. That would put a
  warning on every start that no configuration can fix.

## Forecloses

- **A new provider is a schema change.** Wiring one means declaring it in
  `provider.cue`, committed as `feat(schema)` then `chore(gen)`.
- **A declaration is the client's, not the server's.** The OpenAI and
  Wyoming ears declare no languages, so a household that asks an
  English-only Whisper for German is not refused. What that server hears is
  its own to say.
- **Needs are the build's.** A household cannot relax a required need, only
  choose a provider that meets it.
- **Tool providers are not a role.** MCP's and Home Assistant's policy stays
  in `tool.cue` and ADR-0075. A `tools` role would be a schema change, and
  `Capabilities()` is the seam it would use.
- **Only chorusd checks.** The review UI's Replay and `chorus-eval` build an
  engine without the check.
