// Package config loads the per-repo llok.yaml policy file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/AnasArshad99/llok/internal/agents"
	"github.com/AnasArshad99/llok/internal/guard"
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
	// Rules override or extend the built-in supervisor rules (see guard.Defaults).
	Rules []guard.Rule `yaml:"rules"`
	// Agents optionally defines extra agents inline, keyed by name.
	Agents map[string]agents.Agent `yaml:"agents,omitempty"`
}

// ResolveAgent returns the agent this repo is configured for.
func (c *Config) ResolveAgent() (agents.Agent, error) {
	name := c.Agent
	if name == "" {
		name = "shell"
	}
	return agents.Resolve(name, c.Agents)
}

// GuardRules returns the effective supervisor rule set.
func (c *Config) GuardRules() []guard.Rule {
	return guard.Merge(guard.Defaults(), c.Rules)
}

// Default returns the built-in defaults.
func Default() *Config {
	c := &Config{Version: 1, Name: "project", Agent: "claude-code"}
	c.VM.CPUs = 2
	c.VM.MemoryMB = 1024
	c.Mount.Repo = "/repo"
	c.Network.Mode = "broker"
	return c
}

// Render writes the config as a commented llok.yaml.
func (c *Config) Render() []byte {
	return []byte(fmt.Sprintf(`# llok policy. Everything the agent can touch is declared here.
version: %d
name: %s

# Which coding agent the VM is set up for. Built in: claude-code, opencode,
# codex, qwen-code, gemini-cli, shell. (llok agents lists them.)
agent: %s

vm:
  cpus: %d
  memory_mb: %d

# The repo is mounted read-write at this path inside the guest.
mount:
  repo: %s

# The guest has no network device. Its only way out is the llok broker on
# the host, which lets the agent reach its provider API with your credential
# injected on the way out. Anything else must be listed under allow.
#   mode: broker | none
#   allow: extra hosts the agent may reach, e.g. registry.npmjs.org, "*.github.com"
network:
  mode: %s
  allow: []

# Supervisor rules run on your Mac over what it can see: files the agent
# changed in the repo and hosts the broker was asked for. Built-in defaults:
#   git-hooks    pause the VM when .git/hooks/* is written
#   ci-config    notify when .github/workflows/* changes
#   mass-delete  pause the VM when 50+ files are deleted within 10s
# Override by name, disable with action: off, or add your own:
#   - name: lockfile
#     on: file
#     path: package-lock.json
#     action: notify
#     message: lockfile changed
rules: []
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
	if c.Network.Mode != "none" && c.Network.Mode != "broker" {
		return nil, fmt.Errorf("%s: network.mode must be \"broker\" or \"none\"", FileName)
	}
	if err := guard.Validate(c.GuardRules()); err != nil {
		return nil, fmt.Errorf("%s: rules: %w", FileName, err)
	}
	for name, a := range c.Agents {
		a.Name = name
		if err := agents.Validate(a); err != nil {
			return nil, fmt.Errorf("%s: agents: %w", FileName, err)
		}
	}
	if c.Agent != "" {
		if _, err := c.ResolveAgent(); err != nil {
			return nil, fmt.Errorf("%s: %w", FileName, err)
		}
	}
	return c, nil
}
