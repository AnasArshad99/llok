// Package config loads the per-repo llok.yaml policy file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the policy file looked up in the repo root.
const FileName = "llok.yaml"

// DefaultYAML is what `llok init` writes.
const DefaultYAML = `# llok policy. Everything the agent can touch is declared here.
version: 1

vm:
  cpus: 2
  memory_mb: 1024

# The repo is mounted read-write at this path inside the guest.
mount:
  repo: /repo

# The guest has no network device. In a later step the only way out will be
# the llok broker on the host, and this is where you allow destinations.
network:
  mode: none
  allow: []
`

// Config is the parsed llok.yaml.
type Config struct {
	Version int `yaml:"version"`
	VM      struct {
		CPUs     uint   `yaml:"cpus"`
		MemoryMB uint64 `yaml:"memory_mb"`
	} `yaml:"vm"`
	Mount struct {
		Repo string `yaml:"repo"`
	} `yaml:"mount"`
	Network struct {
		Mode  string   `yaml:"mode"`
		Allow []string `yaml:"allow"`
	} `yaml:"network"`
}

// Default returns the built-in defaults.
func Default() *Config {
	var c Config
	if err := yaml.Unmarshal([]byte(DefaultYAML), &c); err != nil {
		panic("default llok.yaml does not parse: " + err.Error())
	}
	return &c
}

// Load reads llok.yaml from dir, falling back to defaults when absent.
func Load(dir string) (*Config, error) {
	c := Default()
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if c.VM.CPUs == 0 || c.VM.MemoryMB == 0 {
		return nil, fmt.Errorf("%s: vm.cpus and vm.memory_mb must be > 0", FileName)
	}
	if c.Network.Mode != "none" {
		return nil, fmt.Errorf("%s: network.mode %q not supported yet (only \"none\")", FileName, c.Network.Mode)
	}
	return c, nil
}
