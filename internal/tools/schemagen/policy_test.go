package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Policy constraints are the reason the schemas are CUE rather than JSON
// Schema. If a violation ever validates, the generated Go and the model-facing
// schema agree on something unsafe, and nothing downstream catches it.
// verifies SPEC §6

const schemaDir = "../../../schema"

func cueBin(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("cue")
	if err != nil {
		if home, herr := os.UserHomeDir(); herr == nil {
			candidate := filepath.Join(home, "go", "bin", "cue")
			if _, serr := os.Stat(candidate); serr == nil {
				return candidate
			}
		}
		t.Skip("cue not installed; run `task tools`")
	}
	return path
}

// stageSchema copies the real schemas into a temp module, optionally adding one
// fixture, so a violation is evaluated against production constraints.
func stageSchema(t *testing.T, fixture string) string {
	t.Helper()
	dir := t.TempDir()

	modDir := filepath.Join(dir, "cue.mod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Reuse the real module file so the fixtures evaluate under the same CUE
	// language version as production.
	copyFile(t, filepath.Join(schemaDir, "..", "cue.mod", "module.cue"), filepath.Join(modDir, "module.cue"))

	srcs, err := filepath.Glob(filepath.Join(schemaDir, "*.cue"))
	if err != nil || len(srcs) == 0 {
		t.Fatalf("no schemas found in %s: %v", schemaDir, err)
	}
	for _, src := range srcs {
		copyFile(t, src, filepath.Join(dir, filepath.Base(src)))
	}
	if fixture != "" {
		copyFile(t, filepath.Join(schemaDir, "testdata", fixture), filepath.Join(dir, fixture))
	}
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	write(t, dst, string(b))
}

// vet runs concrete validation. Plain `cue vet` does not evaluate the
// conditional constraints, so -c is load-bearing, not a style choice.
func vet(t *testing.T, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command(cueBin(t), "vet", "-c", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSchemasValidate(t *testing.T) {
	out, err := vet(t, stageSchema(t, ""))
	if err != nil {
		t.Fatalf("production schemas must validate concretely:\n%s", out)
	}
}

func TestPolicyViolationsAreRejected(t *testing.T) {
	cases := []struct {
		fixture string
		why     string
		expect  string
	}{
		{
			"uninterruptible_without_confirmation.cue",
			"an uninterruptible tool must be confirmable or barge-in strands a side effect",
			"requires_confirmation",
		},
		{
			"person_scope_without_unknown_speaker.cue",
			"person-scoped tools must declare what an unidentified speaker gets",
			"unknown_speaker",
		},
		{
			"slow_tool_short_timeout.cue",
			"a slow tool with a short timeout is reported failed while still working",
			"timeout_ms",
		},
		{
			"training_signal_without_versions.cue",
			"a training signal with no recorded versions cannot be attributed",
			"requires_versions",
		},
	}

	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			out, err := vet(t, stageSchema(t, c.fixture))
			if err == nil {
				t.Fatalf("violation accepted: %s", c.why)
			}
			if !strings.Contains(out, c.expect) {
				t.Errorf("rejection should name %q so the author knows what broke; got:\n%s", c.expect, out)
			}
		})
	}
}
