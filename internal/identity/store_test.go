package identity_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/identity"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), identity.DefaultPath)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// What is saved is what is loaded, to the bit: a centroid that drifts on the
// way through the file would drift every score with it.
//
// verifies SPEC §5
func TestTheHouseholdSurvivesAFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), identity.DefaultPath)
	ids := household()
	if err := ids.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := identity.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Model != ids.Model || got.Dim != ids.Dim || len(got.People) != len(ids.People) {
		t.Fatalf("loaded %+v, saved %+v", got, ids)
	}
	for i := range ids.People {
		want, have := ids.People[i], got.People[i]
		if have.ID != want.ID || have.Name != want.Name || have.Utterances != want.Utterances {
			t.Errorf("person %d loaded as %+v", i, have)
		}
		for j := range want.Centroid {
			if have.Centroid[j] != want.Centroid[j] {
				t.Errorf("person %d dim %d: %v became %v", i, j, want.Centroid[j], have.Centroid[j])
			}
		}
	}
}

// A voiceprint is a credential: nobody else on the host gets to read it, and
// a crash mid-write must not leave a torn file where the household was.
//
// verifies SPEC §13
func TestSaveIsPrivateAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, identity.DefaultPath)
	if err := household().Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode is %o, want 600", mode)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries after a save, want only the file", len(entries))
	}
}

// Save over an existing file replaces it whole, so a re-enrollment is never
// half old and half new.
func TestSaveReplacesTheOldHousehold(t *testing.T) {
	path := filepath.Join(t.TempDir(), identity.DefaultPath)
	if err := household().Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	solo := &identity.Identities{Model: "fake", Dim: dim}
	must(solo.Enroll("cass", "", [][]float32{axis(2), axis(2), axis(2)}))
	if err := solo.Save(path); err != nil {
		t.Fatalf("save again: %v", err)
	}
	got, err := identity.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.People) != 1 || got.People[0].ID != "cass" {
		t.Errorf("loaded %v", got.Household())
	}
}

// A missing file is the fresh-install case and the caller may run with nobody
// enrolled, so the cause has to be recognisable through the wrapping.
func TestLoadMissingFileIsRecognisable(t *testing.T) {
	_, err := identity.Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("want error for a missing file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file is not reported as such: %v", err)
	}
	if !strings.Contains(err.Error(), "enrolled") {
		t.Errorf("error does not say what to do: %v", err)
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	if _, err := identity.Load(write(t, "people: [oops\n")); err == nil {
		t.Error("malformed yaml loaded")
	}
}

// Every error names the file and the person, the way config does for a
// satellite, because the file is machine-written and a bad entry means
// someone will be reading it by hand.
func TestLoadValidatesEachPerson(t *testing.T) {
	head := "model: fake\ndim: 4\npeople:\n"
	cases := []struct {
		name, body, want string
	}{
		{"no model", "dim: 4\npeople: []\n", "model is required"},
		{"no dim", "model: fake\npeople: []\n", "dim 0"},
		{"no id", head + "  - utterances: 3\n    centroid: [1, 0, 0, 0]\n", "person 0 (unnamed): id is required"},
		{"wrong width", head + "  - id: alan\n    utterances: 3\n    centroid: [1, 0, 0]\n", "person 0 (alan): centroid has 3 dims, want 4"},
		{"not unit", head + "  - id: alan\n    utterances: 3\n    centroid: [2, 0, 0, 0]\n", "length 2.0000, want 1"},
		{"no utterances", head + "  - id: alan\n    centroid: [1, 0, 0, 0]\n", "utterances is 0"},
		{"not finite", head + "  - id: alan\n    utterances: 3\n    centroid: [.nan, 0, 0, 0]\n", "not finite"},
		{"duplicate", head + "  - id: alan\n    utterances: 3\n    centroid: [1, 0, 0, 0]\n  - id: alan\n    utterances: 3\n    centroid: [0, 1, 0, 0]\n", "duplicate person id \"alan\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := write(t, c.body)
			_, err := identity.Load(path)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), path) {
				t.Errorf("error should name the file and mention %q, got: %v", c.want, err)
			}
		})
	}
}

// Save runs the same validation, so a bug that builds a bad centroid cannot
// persist it for the next boot to choke on.
func TestSaveRefusesAnInvalidHousehold(t *testing.T) {
	path := filepath.Join(t.TempDir(), identity.DefaultPath)
	bad := &identity.Identities{Model: "fake", Dim: dim, People: []identity.Person{{ID: "x", Utterances: 3, Centroid: []float32{2, 0, 0, 0}}}}
	if err := bad.Save(path); err == nil {
		t.Fatal("an invalid household was saved")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a refused save left a file behind")
	}
}
