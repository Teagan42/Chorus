# 0036. Smart Turn v3.2 judges each pause; Energy's silence stays the floor

- **Status:** accepted
- **Source:** SPEC §4.5, §10, §11 · ADR-0029, ADR-0030, ADR-0035

SPEC §4.5 asks for semantic endpointing so "turn off the... uh... kitchen
lights" works, and §10 names Smart Turn v2 for it. Until now `listen.Energy`
ended every turn after 800 ms of quiet, and ADR-0035 made that wait visible:
it alone was over the 700 ms first-audio target (§11) before any model ran.
`listen.Semantic` now ends a turn when Smart Turn says the words were a whole
one, and falls back to the silence whenever it cannot say.

```go
import (
	"github.com/teaganglenn/chorus/internal/listen"
	"github.com/teaganglenn/chorus/internal/provider/smartturn"
)

// One endpointer per link: it holds that device's turn in progress.
func endpointer(url string) (listen.Endpointer, error) {
	judge, err := smartturn.New(smartturn.Config{BaseURL: url})
	if err != nil {
		return nil, err
	}
	return listen.NewSemantic(judge), nil
}
```

**The rule.** Energy still says where speech is. After 200 ms of quiet
(`listen.DefaultPause`, the pause pipecat runs Smart Turn on) the endpointer
sends the judge the turn so far, at most its last 8 s, off the bridge read
loop on a goroutine the listener owns and waits for (ADR-0030). A "finished"
verdict ends the turn on the next chunk. "Unfinished" holds it open until
speech resumes, which re-asks at the next pause, or until 2 s of quiet
(`listen.DefaultHold`). No verdict by 800 ms (`listen.DefaultSilence`), from
an error, a refusal, or a slow sidecar, ends it where Energy would. A verdict
about a pause that speech has since ended is cancelled and ignored. The End
reports the quiet it waited through (`listen.Trailer`), so ADR-0035's wait
still starts when the person stopped.

**Smart Turn v3.2, not v2, and on the audio, not STT partials.** v2 was
pipecat's model when the spec was written; v3 replaced it upstream with an
8M-parameter Whisper-tiny encoder that judges prosody and words together
from audio alone, so it needs no transcript and runs before the final
decode. v3.2's CPU export is the one pipecat ships as its default.

**The model is pinned through PyPI.** Hugging Face, where upstream
publishes, is unreachable from where this was built, as it was for
ADR-0029. pipecat bundles the model in its wheel, so the pin is that file:
`pipecat_ai-1.12.0-py3-none-any.whl` by URL and sha256
`4cd3dc071b7b64da7ac700a26a224b773ae6ef8d6694a4566332d2e5e39ed6d9`, and the
member `smart-turn-v3.2-cpu.onnx` (8,679,182 bytes) by its own sha256
`2bb026316b14a660486a75b1733cd3fbab8c2fd0314dc9af7be49f8cca967e4f`. A PyPI
file never changes, the wheel is checked before anything is read out of it,
and only the model is kept. The runtime is `onnxruntime` 1.30.0 (1.31.0 was
published the day this was pinned). The log-mel front end is pipecat's numpy port of Whisper's,
vendored with its BSD 2-Clause licence rather than installing `transformers`
or pipecat itself. The sidecar's environment is 20 packages, itself included, and 132 MB.

**Measured on 2026-10-09**, on this dev box's CPU, through the models tier
against the sidecar served from its venv:

| What | Result |
|---|---|
| One verdict | 90–220 ms, most of it feature extraction |
| End after the last loud chunk, eight takes judged finished | 319–361 ms |
| The same through the whole daemon (`cmd/chorusd` models tier) | 340 ms |
| A take judged unfinished ("turn off the, uh,", p = 0.009) | ended at the 2 s hold |
| Energy, for comparison | 800 ms, every turn |

The takes are espeak-ng renderings of household commands, four finished and
five cut mid-sentence, because no recorded speech may enter the repo
(CONTRIBUTING §7) and none could be downloaded. On them the verdicts were
right on 4 of 9: the filler was caught, but the robot voice's falling pitch
read as finished on four of five cut sentences, and one finished command
scored 0.339. Synthetic prosody is not a person's, so this says the pipeline
and its timing work, not how often the model is right in a kitchen. The
models tier prints every verdict for a household to judge on its own
recordings (`-smartturn-wavs`).

## Alternatives rejected

- **Smart Turn v2 over STT partials, as §10 words it.** Superseded upstream,
  and it waits on a partial decode the audio model does not need.
- **Asking the turn-engine LLM.** §4.5 rules it out by name.
- **Trusting "unfinished" without a hold.** A model that is wrong about a
  finished turn would leave the person waiting for the 20 s backstop.
- **Pipecat's 3 s hold.** A household command is short; 2 s already covers a
  long "uh", and every wrong "unfinished" costs the full hold.
- **Installing pipecat, or `transformers`, for the feature extractor.**
  Hundreds of megabytes of dependencies for one numpy function.

## Forecloses

A wrong "finished" cuts the person off sooner than Energy would have: a
hesitation of 200–800 ms that Energy sat through now ends the turn when the
model misreads it. That is the trade §4.5 asks for, and nothing here measures
how often it happens on real speech. The journal does not record which rule
ended a turn; if that is needed to tune the hold or the pause, it is a field
or an event, not a change to this one.

The Docker build is specified but was not run where this was written, which
has no daemon. What ran: `uv sync --all-packages` from the lockfile, the
image's two `uv sync --frozen` steps against only the files its dockerignore
admits, `smartturn --download-only` against the live PyPI URL with both
digests verified, the sidecar served from that venv, and both Go models
tiers against it.
