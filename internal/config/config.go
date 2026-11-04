// Package config loads the per-repo llok.yaml policy file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/AnasArshad99/llok/internal/agents"
	"github.com/AnasArshad99/llok/internal/guard"
)

// FileName is the policy file looked up in the repo root.
const FileName = "llok.yaml"

// Config is the parsed llok.yaml.
type Config struct {
	Version int       `yaml:"version"`
	Name    string    `yaml:"name"`
	Agent   AgentList `yaml:"agent"` // one name or a list; the first is the default
	VM      struct {
		CPUs     uint   `yaml:"cpus"`
		MemoryMB uint64 `yaml:"memory_mb"`
	} `yaml:"vm"`
	Mount struct {
		Repo string `yaml:"repo"`
		// ReadOnly shares the repo read-only; the agent's writes go to a
		// scratch overlay inside the VM and vanish at power-off.
		ReadOnly bool `yaml:"readonly"`
		// Exclude lists repo paths (relative, shell globs) hidden inside the
		// VM: covered with an empty read-only file or folder.
		Exclude []string `yaml:"exclude"`
	} `yaml:"mount"`
	Network struct {
		Mode  string   `yaml:"mode"`
		Allow []string `yaml:"allow"`
	} `yaml:"network"`
	// Session governs detached sessions: Idle is how long a session may go
	// with no output, input, file change or request before it powers off
	// ("off" disables).
	Session struct {
		Idle string `yaml:"idle"`
	} `yaml:"session"`
	// Rules override or extend the built-in supervisor rules (see guard.Defaults).
	Rules []guard.Rule `yaml:"rules"`
	// Agents optionally defines extra agents inline, keyed by name.
	Agents map[string]agents.Agent `yaml:"agents,omitempty"`
}

// AgentList is the agent: field: "claude-code" or [claude-code, codex].
type AgentList []string

// UnmarshalYAML accepts a scalar or a sequence.
func (l *AgentList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			*l = nil
			return nil
		}
		*l = AgentList{n.Value}
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := n.Decode(&out); err != nil {
			return err
		}
		*l = out
		return nil
	}
	return fmt.Errorf("agent must be a name or a list of names")
}

// Render writes the field as YAML: a bare name, or a flow list.
func (l AgentList) Render() string {
	switch len(l) {
	case 0:
		return "shell"
	case 1:
		return l[0]
	}
	return "[" + strings.Join(l, ", ") + "]"
}

// Primary is the default agent, or "" when none is set.
func (l AgentList) Primary() string {
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

// ResolveAgent returns the default agent this repo is configured for.
func (c *Config) ResolveAgent() (agents.Agent, error) {
	name := c.Agent.Primary()
	if name == "" {
		name = "shell"
	}
	return agents.Resolve(name, c.Agents)
}

// ResolveAgents returns every configured agent, default first.
func (c *Config) ResolveAgents() ([]agents.Agent, error) {
	var out []agents.Agent
	for _, n := range c.Agent {
		a, err := agents.Resolve(n, c.Agents)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// GuardRules returns the effective supervisor rule set.
func (c *Config) GuardRules() []guard.Rule {
	return guard.Merge(guard.Defaults(), c.Rules)
}

// MountPathFor is where a repo named name appears inside the VM: its own
// name at the root, e.g. /career-ops. Characters that would not survive the
// kernel command line are replaced.
func MountPathFor(name string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, name)
	clean = strings.Trim(clean, "-.")
	if clean == "" {
		clean = "repo"
	}
	return "/" + clean
}

// Default returns the built-in defaults.
func Default() *Config {
	c := &Config{Version: 1, Name: "project", Agent: AgentList{"claude-code"}}
	c.VM.CPUs = 2
	c.VM.MemoryMB = 1024
	c.Mount.Repo = "/repo"
	c.Mount.Exclude = DefaultExclude()
	c.Network.Mode = "broker"
	c.Session.Idle = "2h"
	return c
}

// DefaultExclude is what new repos hide from the agent: local secrets.
func DefaultExclude() []string {
	return []string{".env", ".env.*", "*.pem", "*.key"}
}

// Render writes the config as a commented llok.yaml.
func (c *Config) Render() []byte {
	return []byte(fmt.Sprintf(`# llok policy. Everything the agent can touch is declared here.
version: %d
name: %s

# Which coding agents the VM is set up for: one name, or a list whose first
# entry is the default. Built in: claude-code, codex, gemini-cli, shell.
agent: %s

vm:
  cpus: %d
  memory_mb: %d

# The repo is mounted read-write at this path inside the VM (default: its name).
# readonly: true shares it read-only instead; the agent can still write, but
# into a scratch layer inside the VM that is discarded at power-off, so
# nothing reaches your files (llok --ro does the same for one run).
# exclude: paths (relative to the repo, shell globs) the agent must not see.
# They appear empty and read-only inside the VM; the files on your Mac are
# untouched. Set exclude: [] to share everything.
mount:
  repo: %s
  readonly: %t
  exclude: %s

# The guest has no network device. Its only way out is the llok broker on
# the host, which lets the agent reach its provider API with your credential
# injected on the way out. Anything else must be listed under allow.
#   mode: broker | none
#   allow: extra hosts the agent may reach, e.g. registry.npmjs.org, "*.github.com"
network:
  mode: %s
  allow: []

# Sessions keep running when you detach (Ctrl-] d) or close the terminal;
# llok sessions lists them, llok attach <name> returns. idle: how long one
# may sit with no output, input, file change or request before it powers
# itself off. off disables.
session:
  idle: %s

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
`, c.Version, c.Name, c.Agent.Render(), c.VM.CPUs, c.VM.MemoryMB, c.Mount.Repo, c.Mount.ReadOnly, renderList(c.Mount.Exclude), c.Network.Mode, c.Session.Idle))
}

// IdleTimeout parses session.idle; zero means disabled.
func (c *Config) IdleTimeout() (time.Duration, error) {
	v := strings.TrimSpace(c.Session.Idle)
	switch v {
	case "", "off", "0", "none":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s: session.idle %q must be a duration like 2h or 45m, or off", FileName, v)
	}
	if d > 0 && d < time.Minute {
		return 0, fmt.Errorf("%s: session.idle %q is too short (minimum 1m)", FileName, v)
	}
	return d, nil
}

func renderList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = `"` + it + `"`
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// Load reads llok.yaml from dir, falling back to defaults when absent.
func Load(dir string) (*Config, error) {
	c := Default()
	abs, _ := filepath.Abs(dir)
	c.Name = filepath.Base(abs)
	c.Mount.Repo = MountPathFor(c.Name)
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
	for _, e := range c.Mount.Exclude {
		if e == "" || strings.HasPrefix(e, "/") || strings.Contains(e, "..") || strings.ContainsAny(e, " \n\t") {
			return nil, fmt.Errorf("%s: mount.exclude entry %q must be a relative path without spaces or '..'", FileName, e)
		}
	}
	if _, err := c.IdleTimeout(); err != nil {
		return nil, err
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
	if _, err := c.ResolveAgents(); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return c, nil
}
