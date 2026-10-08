package identity

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultPath sits next to devices.yaml and is gitignored for the same
// reason: a voiceprint is a credential that cannot be rotated (SPEC §13).
const DefaultPath = "identities.yaml"

// unitTolerance is how far a stored centroid may drift from unit length and
// still load. YAML round-trips a float32 exactly, so anything looser than
// rounding noise is a hand edit.
const unitTolerance = 1e-3

// Identities is the enrolled household: the contents of identities.yaml.
// Model and Dim pin the embedder the centroids came from, so a model swap is
// refused at load instead of scoring as noise for weeks.
type Identities struct {
	Model  string   `yaml:"model"`
	Dim    int      `yaml:"dim"`
	People []Person `yaml:"people"`
}

// Person is one enrolled household member.
type Person struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name,omitempty"`

	// Utterances is how many phrases built the centroid, kept so a thin
	// enrollment can be told apart from a bad one when a match misfires.
	Utterances int `yaml:"utterances"`

	// Centroid is unit length (see Centroid). Flow style keeps 192 floats on
	// a few lines instead of 192.
	Centroid []float32 `yaml:"centroid,flow"`
}

// New starts an empty household for one embedder.
func New(emb Embedder) *Identities {
	return &Identities{Model: emb.Model(), Dim: emb.Dim()}
}

// Load reads and validates the household. A missing file is wrapped so the
// caller can choose to run with nobody enrolled: os.IsNotExist sees through.
func Load(path string) (*Identities, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (nobody is enrolled yet)", path, err)
	}
	var ids Identities
	if err := yaml.Unmarshal(b, &ids); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := ids.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &ids, nil
}

// Save writes the household atomically, owner-readable only. A crash halfway
// through a plain write would leave a file that fails to load for everyone,
// which is the whole household locked out until someone finds the backup.
func (ids *Identities) Save(path string) error {
	if err := ids.Validate(); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	b, err := yaml.Marshal(ids)
	if err != nil {
		return fmt.Errorf("save %s: encode: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	// After a successful rename there is nothing left to remove; on any other
	// path this is the cleanup, and its own failure has nothing to add.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("save %s: %w", path, err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("save %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	return nil
}

// Validate checks what a load or a save has to be able to rely on. Errors
// name the person by index and id, the way config does for satellites.
func (ids *Identities) Validate() error {
	if ids.Model == "" {
		return errors.New("model is required")
	}
	if ids.Dim <= 0 {
		return fmt.Errorf("dim %d is not positive", ids.Dim)
	}
	seen := make(map[string]bool, len(ids.People))
	for i := range ids.People {
		p := &ids.People[i]
		if err := p.validate(ids.Dim); err != nil {
			return fmt.Errorf("person %d (%s): %w", i, p.describe(), err)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate person id %q", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

func (p *Person) describe() string {
	if p.ID != "" {
		return p.ID
	}
	return "unnamed"
}

func (p *Person) validate(dim int) error {
	if p.ID == "" {
		return errors.New("id is required")
	}
	if p.Utterances < 1 {
		return fmt.Errorf("utterances is %d; a centroid comes from at least one", p.Utterances)
	}
	if len(p.Centroid) != dim {
		return fmt.Errorf("centroid has %d dims, want %d", len(p.Centroid), dim)
	}
	var sum float64
	for _, x := range p.Centroid {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return errors.New("centroid is not finite")
		}
		sum += float64(x) * float64(x)
	}
	if n := math.Sqrt(sum); math.Abs(n-1) > unitTolerance {
		return fmt.Errorf("centroid has length %.4f, want 1", n)
	}
	return nil
}
