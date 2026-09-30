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

// Config is the parsed llok.yaml.
type Config struct {
	Version int    `yaml:"version"`
	Name    string `yaml:"name"`
	Agent   string `yaml:"agent"`
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
	c := &Config{Version: 1, Name: "project", Agent: "claude-code"}
	c.VM.CPUs = 2
	c.VM.MemoryMB = 1024
	c.Mount.Repo = "/repo"
	c.Network.Mode = "none"
	return c
}

// Render writes the config as a commented llok.yaml.
func (c *Config) Render() []byte {
	return []byte(fmt.Sprintf(`# llok policy. Everything the agent can touch is declared here.
version: %d
name: %s

# Which coding agent runs inside the VM (claude-code | shell).
agent: %s

vm:
  cpus: %d
  memory_mb: %d

# The repo is mounted read-write at this path inside the guest.
mount:
  repo: %s

# The guest has no network device. In a later step the only way out will be
# the llok broker on the host, and this is where you allow destinations.
network:
  mode: %s
  allow: []
`, c.Version, c.Name, c.Agent, c.VM.CPUs, c.VM.MemoryMB, c.Mount.Repo, c.Network.Mode))
}

// Load reads llok.yaml from dir, falling back to defaults when absent.
func Load(dir string) (*Config, error) {
	c := Default()
	abs, _ := filepath.Abs(dir)
	c.Name = filepath.Base(abs)
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
