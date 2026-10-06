// Package config loads the satellite inventory. PSKs live in a gitignored file
// rather than a database so they are not part of anything that gets backed up
// or committed by accident (docs/SPEC.md §13).
package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Noise_NNpsk0 takes a 32-byte pre-shared key. A shorter one is accepted here
// only to fail much later as an opaque handshake error.
const pskLen = 32

// Placeholder in devices.example.yaml.
const pskPlaceholder = "BASE64_32_BYTE_KEY_HERE"

type Satellite struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
	PSK     string `yaml:"psk"`
	Room    string `yaml:"room"`
	Profile string `yaml:"profile"`
}

type Config struct {
	Satellites []Satellite `yaml:"satellites"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (copy devices.example.yaml)", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.Satellites) == 0 {
		return nil, fmt.Errorf("%s lists no satellites", path)
	}

	seen := make(map[string]bool, len(c.Satellites))
	for i := range c.Satellites {
		s := &c.Satellites[i]
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("%s: satellite %d (%s): %w", path, i, s.describe(), err)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("%s: duplicate satellite name %q", path, s.Name)
		}
		seen[s.Name] = true
	}
	return &c, nil
}

// describe names a satellite for error messages, tolerating a missing name.
func (s *Satellite) describe() string {
	if s.Name != "" {
		return s.Name
	}
	return "unnamed"
}

func (s *Satellite) validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if err := validateAddress(s.Address); err != nil {
		return err
	}
	return validatePSK(s.PSK)
}

func validateAddress(address string) error {
	if address == "" {
		return fmt.Errorf("address is required (host:6053)")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("address %q is not host:port: %w", address, err)
	}
	if host == "" {
		return fmt.Errorf("address %q has no host", address)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("address %q has a non-numeric port", address)
	}
	return nil
}

// validatePSK decodes the key here so a typo is a config error rather than a
// handshake failure against hardware.
func validatePSK(psk string) error {
	switch psk {
	case "":
		return fmt.Errorf("psk is required (api.encryption.key from the device's ESPHome config)")
	case pskPlaceholder:
		return fmt.Errorf("psk is still the devices.example.yaml placeholder; copy the real key")
	}

	raw, err := base64.StdEncoding.DecodeString(psk)
	if err != nil {
		return fmt.Errorf("psk is not valid base64: %w", err)
	}
	if len(raw) != pskLen {
		return fmt.Errorf("psk decodes to %d bytes, want %d bytes", len(raw), pskLen)
	}
	return nil
}

// PSKBytes returns the decoded key. Load has already validated it.
func (s *Satellite) PSKBytes() ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s.PSK)
	if err != nil {
		return nil, fmt.Errorf("psk for %s: %w", s.Name, err)
	}
	return raw, nil
}

// Find returns the named satellite, or the only one if name is empty.
func (c *Config) Find(name string) (*Satellite, error) {
	if name == "" {
		if len(c.Satellites) != 1 {
			return nil, fmt.Errorf("inventory has %d satellites; specify -device", len(c.Satellites))
		}
		return &c.Satellites[0], nil
	}
	for i := range c.Satellites {
		if c.Satellites[i].Name == name {
			return &c.Satellites[i], nil
		}
	}
	return nil, fmt.Errorf("no satellite named %q in inventory", name)
}
