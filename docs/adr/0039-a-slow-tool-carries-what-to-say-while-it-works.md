# 0039. A slow tool carries what to say while it works

- **Status:** accepted
- **Source:** SPEC §4.1, §11, §14 · ADR-0003, ADR-0037, ADR-0038

A slow tool used to rely on the model to cover it. The prompt told the model
to call `speak` first and the tool after, and the slow tool's description
said it takes several seconds. Measured models did that some of the time.
The rest of the time they called the tool alone, and the person waited in
silence past SPEC §11's budget.

Teagan ran a bisect on qwen3:14b and found no wording that made it reliable.
Now the words are a required argument of the slow tool itself, and the
session speaks them as the call starts.

```go
import "github.com/teaganglenn/chorus/internal/registry"

// The library gets the query; the person hears the rest.
func searchArgs() (string, string) {
	o, _ := registry.Split(`{"query":"adventure films starring Tom Holland","acknowledgement":"Let me look through the library."}`)
	return o.Rest, o.Acknowledgement
}
```

**The argument.** schemagen adds a required `acknowledgement` to every tool
declared `latency: "slow"`. Its description says the words are spoken for
the model, so it should not also call `speak` for them. No tool may declare
a parameter of that name; CUE rejects it, as it rejects `confirmation`
(ADR-0038). Both are offered to the model in `ToolSpec.ModelParams` and
never declared in `Params`, which is what an executor validates against. An
executor would otherwise reject every slow call for missing an argument the
session took off. The prompt's ordering clause is replaced by one sentence saying
the same thing.

**The session speaks it, when the call actually runs.** After a slow call's
arguments are read, and after any confirmation it needs has been given, the
session does four things:

1. It takes the acknowledgement off.
2. It records a `speak` call of its own, with the id `<call>_ack`, mode
   `queue`, and `acknowledges` naming the slow call.
3. It hands that speak call to the speech channel.
4. It invokes the tool with the rest of the arguments.

A call that is held for a yes says nothing. Nor does a call whose tool is
not implemented. "Unlocking the front door now" must not play before anyone
agreed. A call that leaves the acknowledgement out still runs: silence is the
model's failure, and refusing the search would make it worse.

**The dialogue shows the call as the model made it.** In the log, the
acknowledgement is ordinary speech: playback settles it, a barge-in cuts it,
and nobody heard it if it was discarded. The dialogue's said entry carries
`Acknowledges`. Ollama leaves such an entry out of the next ask, because the
slow call's own arguments already show the words. The exception is a cut
acknowledgement, which goes back as a speak call, so the model knows the
person stopped it.

## Alternatives rejected

- **Keep rewording the prompt.** The bisect is the evidence this does not
  converge.
- **Have the orchestrator speak a canned "One moment."** It is reliable, but
  it is the orchestrator authoring speech. The same "One moment." before
  every search sounds like a machine, and SPEC §4.1 gives the words to the
  model.
- **Make the acknowledgement a separate required tool call.** Nothing can
  require a model to make a call. A required argument is the one place a
  schema can insist.

## Forecloses

- **Two acknowledgements.** A model that also volunteers a `speak` beside
  the slow call says it twice. The prompt and the argument's description both
  tell it not to, but this is unmeasured.
- **Not yet run on a real model.** The models tier now asks qwen3:14b to
  "Recommend a movie like Indiana Jones starring Tom Holland" and checks that
  the search says something. It has not run where this was written, which
  has no Ollama endpoint.
- **Fast tools never acknowledge.** A Home Assistant call that happens to be
  slow says nothing, as before.
