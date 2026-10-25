// Command chorus-eval replays an eval suite's journalled turns against a
// candidate prompt, model and tool set, scores each against the suite's
// expectations beside a baseline, and exits 1 when the candidate regresses
// beyond the gate, so it can stand in front of a persona change (SPEC §13,
// ADR-0079). docs/eval.md is the guide.
//
// It reads the journal at CHORUS_POSTGRES_DSN, or the household's demo day
// with -demo, and asks the model at OLLAMA_URL. It writes nothing to the
// journal: what it asked is cached beside it, on disk.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/evalsuite/evalfake"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/reviewui/household"
	"github.com/teagan42/chorus/internal/session"
)

// version is set by the release build (-X main.version=...).
var version = "dev"

// The same endpoints and models chorusd's turn engine and Replay use, on
// whichever provider CHORUS_LLM_PROVIDER names (ADR-0077).
const (
	ollamaURLEnv    = "OLLAMA_URL"
	ollamaModelEnv  = "OLLAMA_MODEL"
	llmProviderEnv  = "CHORUS_LLM_PROVIDER"
	vllmURLEnv      = "VLLM_URL"
	vllmModelEnv    = "VLLM_MODEL"
	vllmKeyEnv      = "VLLM_API_KEY"
	vllmThinkingEnv = "VLLM_THINKING"
)

// Exit codes: a regression is 1, so a gate tells it from a broken setup.
const (
	exitOK        = 0
	exitRegressed = 1
	exitError     = 2
)

// demoLatency is how long the demo's scripted model takes to first speak.
const demoLatency = 600 * time.Millisecond

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], env{
		stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv,
		open: openPostgres, clock: wallClock{},
	})
	stop()
	os.Exit(code)
}

// env is everything run reaches outside itself, so a test can stand in.
type env struct {
	stdout, stderr io.Writer
	getenv         func(string) string

	// open connects to the journal and what is kept beside it.
	open  func(ctx context.Context) (stores, error)
	clock journal.Clock
}

// stores is what chorus-eval reads: the journal, the review UI's
// judgements, and what chorusd recorded of each version it offered, where a
// turn that offered the house's MCP tools finds its schema (ADR-0101).
type stores struct {
	journal   journal.Store
	decisions curation.Store
	kept      contexts.Store
	close     func()
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func openPostgres(ctx context.Context) (stores, error) {
	pool, err := pgxpool.New(ctx, journal.DSN())
	if err != nil {
		return stores{}, fmt.Errorf("connect journal: %w", err)
	}
	return stores{
		journal: pgJournal{journal.NewPgStore(pool)}, decisions: curation.NewPgStore(pool),
		kept: contexts.NewPgStore(pool), close: pool.Close,
	}, nil
}

// pgJournal names the two read interfaces PgStore already satisfies.
type pgJournal struct{ *journal.PgStore }

// provider builds the engines a side asks. A provider is one entry here.
type provider func(getenv func(string) string, store journal.Store) (evalsuite.Engines, error)

var providers = map[string]provider{
	"ollama": ollamaEngines,
	"vllm":   vllmEngines,
	"demo":   demoEngines,
}

// ollamaEngines builds engines as Replay does (cmd/reviewui/replay.go),
// against the endpoint chorusd asks. Ollama's engine takes no seed.
func ollamaEngines(getenv func(string) string, _ journal.Store) (evalsuite.Engines, error) {
	base := getenv(ollamaURLEnv)
	if base == "" {
		return nil, fmt.Errorf("no model to ask: set %s, the endpoint chorusd uses, or try -demo", ollamaURLEnv)
	}
	return evalsuite.Factory(func(model, prompt string, tools map[string]registry.ToolSpec) (session.Engine, journal.Versions, error) {
		e, err := ollama.New(ollama.Config{BaseURL: base, Model: model, Prompt: prompt, Specs: tools})
		if err != nil {
			return nil, journal.Versions{}, err
		}
		return e, e.Versions(), nil
	}), nil
}

// vllmEngines builds engines as Replay does under CHORUS_LLM_PROVIDER=vllm,
// against the server chorusd asks, so a household on vLLM scores the engine
// that answers it (ADR-0077). Thinking follows VLLM_THINKING, refused as
// chorusd refuses it when it is not a boolean.
func vllmEngines(getenv func(string) string, _ journal.Store) (evalsuite.Engines, error) {
	base, key := getenv(vllmURLEnv), getenv(vllmKeyEnv)
	if base == "" {
		return nil, fmt.Errorf("no model to ask: set %s, the server chorusd uses, or try -demo", vllmURLEnv)
	}
	thinking := false
	if v := getenv(vllmThinkingEnv); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("%s=%q is not a boolean", vllmThinkingEnv, v)
		}
		thinking = on
	}
	return evalsuite.Factory(func(model, prompt string, tools map[string]registry.ToolSpec) (session.Engine, journal.Versions, error) {
		e, err := vllm.New(vllm.Config{BaseURL: base, Model: model, APIKey: key, Thinking: thinking, Prompt: prompt, Specs: tools})
		if err != nil {
			return nil, journal.Versions{}, err
		}
		return e, e.Versions(), nil
	}), nil
}

// demoEngines answers the household's turns as the day recorded them
// (internal/evalsuite/evalfake), so the whole path runs with no model.
func demoEngines(_ func(string) string, store journal.Store) (evalsuite.Engines, error) {
	m, err := evalfake.New(context.Background(), store)
	if err != nil {
		return nil, err
	}
	m.Latency = demoLatency
	return m, nil
}

type options struct {
	suite                   string
	demo                    bool
	provider                string
	model, prompt, tools    string
	baseline, baselineModel string
	reps, maxRegressions    int
	threshold               float64
	jsonOut, markdownOut    string
	cacheDir                string
	noCache                 bool
	timeout                 time.Duration
	printPrompt, printTools bool
}

func usage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Fprintf(fs.Output(), `usage: chorus-eval [flags] <suite.yaml>
       chorus-eval draft [-name NAME] > suite.yaml

Replays each case's turn from the journal (%s, or -demo) against a
candidate and a baseline, scores both, and exits 1 when the candidate
regresses beyond the gate, 2 when it cannot run.

`, journal.DSNEnv)
		fs.PrintDefaults()
	}
}

func run(ctx context.Context, args []string, e env) int {
	if len(args) > 0 && args[0] == "draft" {
		return draft(ctx, args[1:], e)
	}
	o, code, ok := parseFlags(args, e)
	if !ok {
		return code
	}
	switch {
	case o.printPrompt:
		fmt.Fprint(e.stdout, ollama.DefaultPrompt)
		return exitOK
	case o.printTools:
		fmt.Fprintln(e.stdout, ollama.ToolSchema(registry.Offered()))
		return exitOK
	}
	rep, err := evaluate(ctx, o, e)
	if err != nil {
		fmt.Fprintf(e.stderr, "chorus-eval: %v\n", err)
		return exitError
	}
	if err := rep.WriteText(e.stdout); err != nil {
		fmt.Fprintf(e.stderr, "chorus-eval: %v\n", err)
		return exitError
	}
	if err := writeReports(rep, o, e.stdout); err != nil {
		fmt.Fprintf(e.stderr, "chorus-eval: %v\n", err)
		return exitError
	}
	if rep.Regressed {
		return exitRegressed
	}
	return exitOK
}

func parseFlags(args []string, e env) (options, int, bool) {
	var o options
	fs := flag.NewFlagSet("chorus-eval", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = usage(fs)
	fs.BoolVar(&o.demo, "demo", false, "read the household's Thursday (internal/reviewui/household) instead of the journal, and answer with its scripted model")
	fs.StringVar(&o.provider, "provider", "", "what answers: "+strings.Join(slices.Sorted(maps.Keys(providers)), ", ")+" (default $"+llmProviderEnv+" as chorusd reads it, else ollama; demo with -demo)")
	fs.StringVar(&o.model, "model", "", "the candidate's model (default $"+ollamaModelEnv+", else each turn's recorded model)")
	fs.StringVar(&o.prompt, "prompt", "", "file holding the candidate's system prompt (default this build's; -print-prompt writes it)")
	fs.StringVar(&o.tools, "tools", "", "file holding the candidate's tool schema, as Replay edits it (default this build's offered tools; -print-tools writes it); each case also keeps the MCP tools its turn was offered")
	fs.StringVar(&o.baseline, "baseline", "rerun", "rerun: each turn asked again under the prompt and tools it recorded; recorded: the take it recorded, asked of nobody")
	fs.StringVar(&o.baselineModel, "baseline-model", "", "the baseline's model when it reruns (default each turn's recorded model)")
	fs.IntVar(&o.reps, "reps", 0, "times each case is asked of each side (default the suite's, else 1)")
	fs.Float64Var(&o.threshold, "threshold", evalsuite.DefaultGate.Threshold, "how far the candidate's score may fall below the baseline's")
	fs.IntVar(&o.maxRegressions, "max-regressions", evalsuite.DefaultGate.MaxRegressions, "how many checks may regress")
	fs.StringVar(&o.jsonOut, "json", "", "write the JSON report to this file, - for stdout")
	fs.StringVar(&o.markdownOut, "markdown", "", "write the Markdown report to this file, - for stdout")
	fs.StringVar(&o.cacheDir, "cache", defaultCacheDir(), "keep each take here, so scoring again asks nothing")
	fs.BoolVar(&o.noCache, "no-cache", false, "ask every case again, and keep nothing")
	fs.DurationVar(&o.timeout, "timeout", 3*time.Minute, "bound on one ask")
	fs.BoolVar(&o.printPrompt, "print-prompt", false, "write this build's system prompt, to start a -prompt file from, and exit")
	fs.BoolVar(&o.printTools, "print-tools", false, "write this build's tool schema, to start a -tools file from, and exit")
	// Flags may follow the suite too, as `task eval -- <suite> -prompt f` has it.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return o, exitOK, false
			}
			return o, exitError, false
		}
		if fs.NArg() == 0 {
			break
		}
		positional, args = append(positional, fs.Arg(0)), fs.Args()[1:]
	}
	if o.printPrompt || o.printTools {
		return o, 0, true
	}
	if len(positional) != 1 {
		fs.Usage()
		return o, exitError, false
	}
	o.suite = positional[0]
	if o.provider == "" {
		// The provider chorusd answers with, so the score is of that engine.
		o.provider = e.getenv(llmProviderEnv)
		if o.provider == "" {
			o.provider = "ollama"
		}
		if o.demo {
			o.provider = "demo"
		}
	}
	if o.model == "" {
		o.model = e.getenv(ollamaModelEnv)
		if o.provider == "vllm" {
			o.model = e.getenv(vllmModelEnv)
		}
	}
	return o, 0, true
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chorus-eval")
}

// evaluate loads the suite, resolves it against the journal, asks both
// sides and compares them.
func evaluate(ctx context.Context, o options, e env) (evalsuite.Report, error) {
	s, err := evalsuite.Load(o.suite)
	if err != nil {
		return evalsuite.Report{}, err
	}
	if o.reps < 0 || o.reps > evalsuite.MaxRepetitions {
		return evalsuite.Report{}, fmt.Errorf("-reps %d: want 1 to %d", o.reps, evalsuite.MaxRepetitions)
	}
	if o.baseline != "rerun" && o.baseline != "recorded" {
		return evalsuite.Report{}, fmt.Errorf("-baseline %q: want rerun or recorded", o.baseline)
	}
	newEngines, ok := providers[o.provider]
	if !ok {
		return evalsuite.Report{}, fmt.Errorf("-provider %q: want one of %s", o.provider, strings.Join(slices.Sorted(maps.Keys(providers)), ", "))
	}
	cand, err := candidate(o)
	if err != nil {
		return evalsuite.Report{}, err
	}

	var (
		store journal.Store
		src   = contexts.Default()
		clock = e.clock
	)
	if o.demo {
		if store, err = household.Journal(ctx); err != nil {
			return evalsuite.Report{}, err
		}
		src = contexts.Chain{household.Contexts(), contexts.Default()}
	} else {
		st, err := e.open(ctx)
		if err != nil {
			return evalsuite.Report{}, err
		}
		defer st.close()
		store = st.journal
		kept, err := st.kept.Recorded(ctx)
		if err != nil {
			return evalsuite.Report{}, err
		}
		src = contexts.Chain{src, kept}
	}
	cases, err := evalsuite.Resolve(ctx, store, src, s)
	if err != nil {
		return evalsuite.Report{}, err
	}
	engines, err := newEngines(e.getenv, store)
	if err != nil {
		return evalsuite.Report{}, err
	}
	if m, ok := engines.(*evalfake.Model); ok {
		// The scripted model's clock moves only while it answers.
		clock = m.Clock
	}
	reps := s.Repetitions
	if o.reps > 0 {
		reps = o.reps
	}
	r := evalsuite.Runner{
		Engines: engines, Provider: o.provider, Clock: clock, Timeout: o.timeout, Repetitions: reps,
	}
	if !o.noCache && o.cacheDir != "" {
		r.Cache = evalsuite.DirCache{Dir: o.cacheDir}
	}
	baseline := evalsuite.Side{Name: "baseline", Recorded: o.baseline == "recorded", Model: o.baselineModel}
	base, err := r.Side(ctx, cases, baseline)
	if err != nil {
		return evalsuite.Report{}, err
	}
	got, err := r.Side(ctx, cases, cand)
	if err != nil {
		return evalsuite.Report{}, err
	}
	return evalsuite.Compare(s, cases, baseline, cand, base, got, evalsuite.Gate{Threshold: o.threshold, MaxRegressions: o.maxRegressions}), nil
}

// candidate is the side under test: this build's prompt and offered tools
// unless files say otherwise.
func candidate(o options) (evalsuite.Side, error) {
	side := evalsuite.Side{Name: "candidate", Model: o.model, Prompt: ollama.DefaultPrompt, Tools: registry.Offered(), HouseTools: true}
	if o.prompt != "" {
		b, err := os.ReadFile(o.prompt)
		if err != nil {
			return side, fmt.Errorf("-prompt: %w", err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return side, fmt.Errorf("-prompt %s is empty", o.prompt)
		}
		side.Prompt = string(b)
	}
	if o.tools != "" {
		b, err := os.ReadFile(o.tools)
		if err != nil {
			return side, fmt.Errorf("-tools: %w", err)
		}
		tools, err := ollama.ParseToolSchema(string(b))
		if err != nil {
			return side, fmt.Errorf("-tools %s: %w", o.tools, err)
		}
		side.Tools = tools
	}
	return side, nil
}

func writeReports(rep evalsuite.Report, o options, stdout io.Writer) error {
	for _, out := range []struct {
		path  string
		write func(io.Writer) error
	}{{o.jsonOut, rep.WriteJSON}, {o.markdownOut, rep.WriteMarkdown}} {
		switch out.path {
		case "":
		case "-":
			if err := out.write(stdout); err != nil {
				return err
			}
		default:
			f, err := os.Create(out.path)
			if err != nil {
				return err
			}
			if err := out.write(f); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

// draft writes a suite of the turns reviewers have judged, for a person to
// tighten and check in.
func draft(ctx context.Context, args []string, e env) int {
	fs := flag.NewFlagSet("chorus-eval draft", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	name := fs.String("name", "judged", "the drafted suite's name")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	st, err := e.open(ctx)
	if err != nil {
		fmt.Fprintf(e.stderr, "chorus-eval draft: %v\n", err)
		return exitError
	}
	defer st.close()
	s, err := evalsuite.Draft(ctx, st.journal, st.decisions, *name)
	if err == nil && len(s.Cases) == 0 {
		err = errors.New("nothing judged yet: mark turns good, or correct them, in the review UI first")
	}
	var b []byte
	if err == nil {
		b, err = s.Marshal()
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "chorus-eval draft: %v\n", err)
		return exitError
	}
	fmt.Fprintf(e.stdout, "# Drafted by chorus-eval %s from the review UI's judgements. Read each case,\n# tighten what it expects, then check it in (docs/eval.md).\n", version)
	_, _ = e.stdout.Write(b)
	return exitOK
}
