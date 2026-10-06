package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodPSK = "seJlx7BCna54FicYF6Sg4xUB1y+8cUpFHJRm9+eKf2c="

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadParsesInventory(t *testing.T) {
	path := writeConfig(t, `satellites:
  - name: living-room
    address: 10.0.12.116:6053
    psk: `+goodPSK+`
    room: living-room
    profile: satellite1
  - name: kitchen
    address: 10.0.12.117:6053
    psk: `+goodPSK+`
    room: kitchen
    profile: voice-pe
`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Satellites) != 2 {
		t.Fatalf("got %d satellites, want 2", len(c.Satellites))
	}
	if c.Satellites[1].Profile != "voice-pe" {
		t.Errorf("profile = %q", c.Satellites[1].Profile)
	}
}

// The error must name the example file: a missing inventory is the first thing
// every new contributor hits.
func TestLoadMissingFileSuggestsExample(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("want error for missing file")
	}
	if !strings.Contains(err.Error(), "devices.example.yaml") {
		t.Errorf("error should point at the example: %v", err)
	}
}

func TestLoadRejectsEmptyInventory(t *testing.T) {
	if _, err := Load(writeConfig(t, "satellites: []\n")); err == nil {
		t.Error("want error for empty inventory")
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	if _, err := Load(writeConfig(t, "satellites: [oops\n")); err == nil {
		t.Error("want error for malformed YAML")
	}
}

// A bad PSK otherwise surfaces as an opaque Noise handshake failure against
// real hardware, which is a miserable thing to debug.
func TestLoadValidatesPSK(t *testing.T) {
	cases := []struct {
		name, psk, want string
	}{
		{"not base64", "!!!not base64!!!", "base64"},
		{"wrong length", "c2hvcnQ=", "32 bytes"},
		{"empty", "", "psk"},
		{"example placeholder", "BASE64_32_BYTE_KEY_HERE", "devices.example.yaml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeConfig(t, "satellites:\n  - name: x\n    address: 10.0.0.1:6053\n    psk: \""+c.psk+"\"\n")
			_, err := Load(path)
			if err == nil {
				t.Fatalf("psk %q accepted", c.psk)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.want)) {
				t.Errorf("error should mention %q, got: %v", c.want, err)
			}
		})
	}
}

func TestLoadValidatesAddress(t *testing.T) {
	for _, addr := range []string{"", "10.0.12.116", "not a host:port:extra"} {
		path := writeConfig(t, "satellites:\n  - name: x\n    address: \""+addr+"\"\n    psk: "+goodPSK+"\n")
		if _, err := Load(path); err == nil {
			t.Errorf("address %q accepted", addr)
		}
	}
}

func TestLoadRequiresName(t *testing.T) {
	path := writeConfig(t, "satellites:\n  - address: 10.0.0.1:6053\n    psk: "+goodPSK+"\n")
	if _, err := Load(path); err == nil {
		t.Error("nameless satellite accepted")
	}
}

// Duplicate names make -device ambiguous and silently pick one.
func TestLoadRejectsDuplicateNames(t *testing.T) {
	path := writeConfig(t, `satellites:
  - name: dup
    address: 10.0.0.1:6053
    psk: `+goodPSK+`
  - name: dup
    address: 10.0.0.2:6053
    psk: `+goodPSK+`
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("duplicate names accepted")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("error should name the duplicate: %v", err)
	}
}

func TestFindByName(t *testing.T) {
	c := &Config{Satellites: []Satellite{
		{Name: "a", Address: "1:6053"},
		{Name: "b", Address: "2:6053"},
	}}

	got, err := c.Find("b")
	if err != nil {
		t.Fatalf("Find(b): %v", err)
	}
	if got.Address != "2:6053" {
		t.Errorf("got %+v", got)
	}

	// Must return a pointer into the inventory, not a copy, so callers see
	// the same satellite the config describes.
	if got != &c.Satellites[1] {
		t.Error("Find should return a pointer into the slice")
	}

	if _, err := c.Find("missing"); err == nil {
		t.Error("want error for unknown name")
	}
}

func TestFindEmptyNameRequiresExactlyOne(t *testing.T) {
	one := &Config{Satellites: []Satellite{{Name: "only"}}}
	got, err := one.Find("")
	if err != nil || got.Name != "only" {
		t.Errorf("single-satellite default failed: %v %+v", err, got)
	}

	two := &Config{Satellites: []Satellite{{Name: "a"}, {Name: "b"}}}
	if _, err := two.Find(""); err == nil {
		t.Error("ambiguous default should error rather than guess")
	}
}

// The committed example must stay loadable in shape and must not be usable
// as-is, or someone will ship the placeholder key.
func TestExampleConfigIsATemplateNotUsable(t *testing.T) {
	body, err := os.ReadFile("../../devices.example.yaml")
	if err != nil {
		t.Skipf("no example file: %v", err)
	}
	path := writeConfig(t, string(body))
	if _, err := Load(path); err == nil {
		t.Error("devices.example.yaml must not load as a working config")
	}
}
