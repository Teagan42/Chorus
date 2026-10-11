// Command enroll writes identities.yaml, the household's voiceprints
// (SPEC §5, ADR-0025). A dev tool in the shape of probe and harvest, with
// three verbs: add embeds a person's guided phrases over the speaker-ID
// sidecar and saves their centroid, list names who is enrolled, remove
// drops one. chorusd reads the file at startup only, so each change here
// asks for a restart and says so.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/provider/speakerid"
)

// speakerIDURLEnv is where chorusd finds the sidecar (.env.example); the
// same variable so one .env serves both.
const speakerIDURLEnv = "SPEAKERID_URL"

// version is set by the release build (-X main.version=...).
var version = "dev"

// errUsage marks what the caller got wrong rather than what failed, so main
// exits 2 the way flag.Parse would.
var errUsage = errors.New("usage")

const usage = `usage: enroll <verb> [flags]

  add     -id ID [-name NAME] -wav A.wav -wav B.wav -wav C.wav ...
          embed each take over the speaker-ID sidecar (` + speakerIDURLEnv + `,
          or -speakerid-url) and save the person's voiceprint
  list    name who is enrolled
  remove  -id ID

Every verb takes -identities PATH (default: identities.yaml beside the
inventory -devices names, which is where chorusd looks). Takes are WAV,
16 kHz mono 16-bit PCM, the satellite's own format; nothing is resampled.
chorusd reads the file at startup, so restart it after a change.
`

// restart is said after every write: there is no hot reload, and a person
// enrolled while the daemon runs is a guest until it is.
const restart = "restart chorusd to see the change: the household is read at startup only"

// deps is every seam the command has, injected so a test never reads the
// real environment or dials anything.
type deps struct {
	out, report io.Writer
	getenv      func(string) string
	newEmbedder func(baseURL string) (identity.Embedder, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := run(ctx, os.Args[1:], deps{out: os.Stdout, report: os.Stderr, getenv: os.Getenv, newEmbedder: speakerIDEmbedder})
	switch {
	case errors.Is(err, errUsage):
		os.Exit(2)
	case err != nil:
		fmt.Fprintf(os.Stderr, "enroll: %v\n", err)
		os.Exit(1)
	}
}

// speakerIDEmbedder is the real sidecar client at its defaults: TitaNet-L,
// 192 dims, so a sidecar serving anything else is refused on the first take.
func speakerIDEmbedder(baseURL string) (identity.Embedder, error) {
	return speakerid.New(speakerid.Config{BaseURL: baseURL})
}

// run dispatches on the verb. Each verb parses its own flags, so a flag the
// verb does not take is an error rather than silently ignored.
func run(ctx context.Context, args []string, d deps) error {
	if len(args) == 0 {
		fmt.Fprint(d.report, usage)
		return errUsage
	}
	var verb func(context.Context, *verbFlags, []string, deps) error
	switch args[0] {
	case "add":
		verb = add
	case "list":
		verb = list
	case "remove":
		verb = remove
	default:
		fmt.Fprint(d.report, usage)
		return fmt.Errorf("%w: unknown verb %q", errUsage, args[0])
	}
	fmt.Fprintf(d.report, "enroll %s\n", version)
	return verb(ctx, newVerbFlags(args[0], d.report), args[1:], d)
}

// verbFlags is the flag set every verb shares, with the two that locate the
// file. A verb adds its own before parsing.
type verbFlags struct {
	*flag.FlagSet
	devices, identities *string
}

func newVerbFlags(verb string, report io.Writer) *verbFlags {
	fs := flag.NewFlagSet("enroll "+verb, flag.ContinueOnError)
	fs.SetOutput(report)
	return &verbFlags{
		FlagSet:    fs,
		devices:    fs.String("devices", "devices.yaml", "satellite inventory path; identities.yaml sits beside it"),
		identities: fs.String("identities", "", "enrolled voiceprints path (default: identities.yaml beside the inventory)"),
	}
}

// parse reads the verb's arguments and resolves the file the way chorusd
// does: -identities, or identities.yaml beside whatever -devices names.
func (f *verbFlags) parse(args []string) (string, error) {
	if err := f.Parse(args); err != nil {
		return "", fmt.Errorf("%w: %w", errUsage, err)
	}
	if f.NArg() > 0 {
		f.Usage()
		return "", fmt.Errorf("%w: unexpected argument %q", errUsage, f.Arg(0))
	}
	if *f.identities != "" {
		return *f.identities, nil
	}
	return filepath.Join(filepath.Dir(*f.devices), identity.DefaultPath), nil
}

// required is a flag the verb cannot do without, reported as usage.
func (f *verbFlags) required(name, value string) error {
	if value != "" {
		return nil
	}
	f.Usage()
	return fmt.Errorf("%w: -%s is required", errUsage, name)
}

// add enrolls one person from their guided phrases. Every take is read and
// checked before the first is embedded, so a bad file costs no sidecar calls
// and a refused enrollment writes nothing.
func add(ctx context.Context, f *verbFlags, args []string, d deps) error {
	id := f.String("id", "", "the person's id, what every transcript will carry (required)")
	name := f.String("name", "", "the person's display name")
	url := f.String("speakerid-url", "", "speaker-ID sidecar base URL (default: $"+speakerIDURLEnv+")")
	var paths []string
	f.Func("wav", "one take, 16 kHz mono 16-bit PCM WAV; repeat for each (at least 3)", func(p string) error {
		paths = append(paths, p)
		return nil
	})
	path, err := f.parse(args)
	if err != nil {
		return err
	}
	if err := f.required("id", *id); err != nil {
		return err
	}
	if len(paths) == 0 {
		return f.required("wav", "")
	}
	if len(paths) < identity.MinUtterances {
		return fmt.Errorf("add %s: %d wav files, want at least %d: one phrase's embedding is that phrase as much as the voice", *id, len(paths), identity.MinUtterances)
	}
	if *url == "" {
		*url = d.getenv(speakerIDURLEnv)
	}
	if *url == "" {
		return fmt.Errorf("add %s: no speaker-ID sidecar: set %s (see .env.example) or pass -speakerid-url", *id, speakerIDURLEnv)
	}

	takes := make([][]byte, 0, len(paths))
	for _, p := range paths {
		pcm, err := readTake(p)
		if err != nil {
			return fmt.Errorf("add %s: %s: %w", *id, p, err)
		}
		takes = append(takes, pcm)
	}

	emb, err := d.newEmbedder(*url)
	if err != nil {
		return err
	}
	ids, err := identity.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The first person starts the household for this embedder; the
		// file then pins the model for everyone after.
		ids = identity.New(emb)
	case err != nil:
		return err
	}
	if err := ids.EnrollAudio(ctx, emb, *id, *name, takes); err != nil {
		return err
	}
	if err := ids.Save(path); err != nil {
		return err
	}

	who := *id
	if *name != "" {
		who = fmt.Sprintf("%s (%s)", *id, *name)
	}
	fmt.Fprintf(d.out, "enrolled %s from %d utterances under %s into %s; %d enrolled\n", who, len(takes), ids.Model, path, len(ids.People))
	fmt.Fprintln(d.out, restart)
	return nil
}

// list names the household and how many phrases built each voiceprint. The
// vectors stay in the file: they are biometric, and a terminal is not where
// they belong.
func list(_ context.Context, f *verbFlags, args []string, d deps) error {
	path, err := f.parse(args)
	if err != nil {
		return err
	}
	ids, err := identity.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(d.out, nobody(path))
		return nil
	case err != nil:
		return err
	}
	if len(ids.People) == 0 {
		fmt.Fprintf(d.out, "nobody is enrolled in %s\n", path)
		return nil
	}
	for _, p := range ids.People {
		fmt.Fprintf(d.out, "%-16s %-24s %d utterances\n", p.ID, p.Name, p.Utterances)
	}
	return nil
}

// remove drops one person and saves the rest, atomically, so a crash cannot
// lose the household to take one person out of it.
func remove(_ context.Context, f *verbFlags, args []string, d deps) error {
	id := f.String("id", "", "the person's id (required)")
	path, err := f.parse(args)
	if err != nil {
		return err
	}
	if err := f.required("id", *id); err != nil {
		return err
	}
	ids, err := identity.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %s", *id, nobody(path))
	}
	if err != nil {
		return err
	}
	kept := ids.People[:0:0]
	for _, p := range ids.People {
		if p.ID != *id {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(ids.People) {
		return fmt.Errorf("remove %s: not enrolled in %s (enrolled: %s)", *id, path, strings.Join(ids.Household(), ", "))
	}
	ids.People = kept
	if err := ids.Save(path); err != nil {
		return err
	}
	fmt.Fprintf(d.out, "removed %s from %s; %d enrolled\n", *id, path, len(ids.People))
	fmt.Fprintln(d.out, restart)
	return nil
}

func nobody(path string) string {
	return fmt.Sprintf("nobody is enrolled: %s does not exist", path)
}
