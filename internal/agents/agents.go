// Package agents is llok's built-in knowledge of coding agents and the
// providers they talk to. Users never manage these as files: they pick an
// agent by name in llok.yaml, and may describe an extra one inline there.
package agents

import (
	"fmt"
	"sort"
	"strings"
)

// Provider is an API the broker lets an agent reach, and how it authenticates.
type Provider struct {
	Name  string
	Hosts []string // intercepted by the broker; the credential is injected here
	// Upstream is the API base for the agent's "base URL" env var, which
	// llok points at the broker. Requests arriving there are forwarded here.
	Upstream string
	// Env var names the agent reads. KeyEnv takes an API key; TokenEnv (when
	// the provider supports a subscription login) takes an OAuth token;
	// BaseURLEnv is pointed at the broker.
	KeyEnv, TokenEnv, BaseURLEnv string
	// How the credential travels. The key header carries an API key; the
	// token header carries an OAuth token. Format uses {secret}.
	KeyHeader, KeyFormat     string
	TokenHeader, TokenFormat string
	// Login, when set, is a subscription sign-in llok can drive on the host
	// and capture the resulting token from.
	Login *Login
}

// Login describes how to obtain a token from the provider's own CLI.
type Login struct {
	Command []string // run on the host in a pty
	Pattern string   // regexp matching the token in its output
	Needs   string   // binary that must be installed on the host
}

// Agent is a coding CLI llok can install in the image and configure.
type Agent struct {
	Name      string            `yaml:"-"`
	Binary    string            `yaml:"binary"`
	Install   string            `yaml:"install"`   // shell line run in the image builder
	Providers []string          `yaml:"providers"` // first is primary (gets the base URL)
	Env       map[string]string `yaml:"env,omitempty"`
	Note      string            `yaml:"-"` // one line for pickers
}

// Placeholder values the guest holds instead of real credentials. The
// broker swaps them on the way out.
const (
	PlaceholderKey   = "llok-placeholder-key"
	PlaceholderToken = "llok-placeholder-token"
)

var providers = map[string]Provider{
	"anthropic": {
		Name:        "anthropic",
		Hosts:       []string{"api.anthropic.com", "platform.claude.com", "console.anthropic.com"},
		Upstream:    "https://api.anthropic.com",
		KeyEnv:      "ANTHROPIC_API_KEY",
		TokenEnv:    "CLAUDE_CODE_OAUTH_TOKEN",
		BaseURLEnv:  "ANTHROPIC_BASE_URL",
		KeyHeader:   "X-Api-Key",
		KeyFormat:   "{secret}",
		TokenHeader: "Authorization",
		TokenFormat: "Bearer {secret}",
		Login: &Login{
			Command: []string{"claude", "setup-token"},
			Pattern: `sk-ant-oat01-[A-Za-z0-9_\-]{20,}`,
			Needs:   "claude",
		},
	},
	"openai": {
		Name:       "openai",
		Hosts:      []string{"api.openai.com", "chatgpt.com", "auth.openai.com"},
		Upstream:   "https://api.openai.com",
		KeyEnv:     "OPENAI_API_KEY",
		BaseURLEnv: "OPENAI_BASE_URL",
		KeyHeader:  "Authorization",
		KeyFormat:  "Bearer {secret}",
	},
	"google": {
		Name:       "google",
		Hosts:      []string{"generativelanguage.googleapis.com"},
		Upstream:   "https://generativelanguage.googleapis.com",
		KeyEnv:     "GEMINI_API_KEY",
		BaseURLEnv: "GOOGLE_GEMINI_BASE_URL",
		KeyHeader:  "X-Goog-Api-Key",
		KeyFormat:  "{secret}",
	},
	"dashscope": {
		Name:       "dashscope",
		Hosts:      []string{"dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com"},
		Upstream:   "https://dashscope-intl.aliyuncs.com",
		KeyEnv:     "OPENAI_API_KEY", // Qwen Code speaks the OpenAI-compatible API
		BaseURLEnv: "OPENAI_BASE_URL",
		KeyHeader:  "Authorization",
		KeyFormat:  "Bearer {secret}",
	},
}

var builtin = map[string]Agent{
	"claude-code": {
		Binary:    "claude",
		Install:   "curl -fsSL https://claude.ai/install.sh | bash -s -- ${LLOK_CLAUDE_VERSION:-latest}",
		Providers: []string{"anthropic"},
		Env: map[string]string{
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_AUTOUPDATER":                      "1",
		},
		Note: "Anthropic's CLI; subscription login or API key",
	},
	"opencode": {
		Binary:    "opencode",
		Install:   "curl -fsSL https://opencode.ai/install | bash",
		Providers: []string{"anthropic", "openai", "google"},
		Note:      "open-source multi-provider CLI; uses whichever keys you have stored",
	},
	"codex": {
		Binary:    "codex",
		Install:   "npm install -g @openai/codex",
		Providers: []string{"openai"},
		Note:      "OpenAI's CLI; API key",
	},
	"qwen-code": {
		Binary:    "qwen",
		Install:   "npm install -g @qwen-code/qwen-code",
		Providers: []string{"dashscope"},
		Env: map[string]string{
			"OPENAI_BASE_URL": "{broker}/dashscope/compatible-mode/v1",
			"OPENAI_MODEL":    "qwen3-coder-plus",
		},
		Note: "Alibaba's Qwen CLI; DashScope API key",
	},
	"gemini-cli": {
		Binary:    "gemini",
		Install:   "npm install -g @google/gemini-cli",
		Providers: []string{"google"},
		Note:      "Google's CLI; Gemini API key",
	},
	"shell": {
		Binary:    "",
		Providers: nil,
		Note:      "no agent, just a shell inside the VM",
	},
}

// Names lists built-in agents in a stable order.
func Names() []string {
	var out []string
	for n := range builtin {
		if n != "shell" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return append(out, "shell")
}

// Installable lists the built-in agents that go into the image.
func Installable() []Agent {
	var out []Agent
	for _, n := range Names() {
		a := builtin[n]
		if a.Install != "" {
			a.Name = n
			out = append(out, a)
		}
	}
	return out
}

// Resolve returns the agent by name, consulting custom definitions first.
func Resolve(name string, custom map[string]Agent) (Agent, error) {
	if a, ok := custom[name]; ok {
		a.Name = name
		if err := Validate(a); err != nil {
			return Agent{}, err
		}
		return a, nil
	}
	if a, ok := builtin[name]; ok {
		a.Name = name
		return a, nil
	}
	return Agent{}, fmt.Errorf("unknown agent %q; built in: %s", name, strings.Join(Names(), ", "))
}

// Validate checks a custom agent definition from llok.yaml.
func Validate(a Agent) error {
	if a.Name == "" {
		return fmt.Errorf("agent without a name")
	}
	if a.Binary == "" {
		return fmt.Errorf("agent %q: binary is required", a.Name)
	}
	for _, p := range a.Providers {
		if _, ok := providers[p]; !ok {
			return fmt.Errorf("agent %q: unknown provider %q; known: %s", a.Name, p, strings.Join(ProviderNames(), ", "))
		}
	}
	return nil
}

// ProviderNames lists providers in a stable order.
func ProviderNames() []string {
	var out []string
	for n := range providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Lookup returns a provider by name.
func Lookup(name string) (Provider, bool) {
	p, ok := providers[name]
	return p, ok
}

// Note returns the one-line description of a built-in agent.
func Note(name string) string { return builtin[name].Note }

// Credential is what the host has stored for a provider.
type Credential struct {
	Kind   string // "apikey" or "oauth"
	Secret string
}

// Session is everything the broker and the guest need for one run of an
// agent: the providers it may reach, the credential for each, and the
// environment to export inside the VM.
type Session struct {
	Agent     Agent
	Providers []Provider            // in agent order; first is primary
	Creds     map[string]Credential // by provider name; only those stored
	Env       map[string]string     // exported in the guest
}

// Prepare builds the session for agent a. Every provider the user has a
// credential for is included (so any agent in the image can be used), but
// the chosen agent's providers must be satisfied: its primary one is
// required. brokerURL is where the guest reaches the broker; each provider
// gets its own path under it so base URLs do not collide.
func Prepare(a Agent, lookup func(provider string) (Credential, bool), brokerURL string) (Session, error) {
	s := Session{Agent: a, Creds: map[string]Credential{}, Env: map[string]string{}}
	add := func(p Provider, c Credential) {
		s.Providers = append(s.Providers, p)
		s.Creds[p.Name] = c
		if c.Kind == "oauth" && p.TokenEnv != "" {
			s.Env[p.TokenEnv] = PlaceholderToken
		} else if p.KeyEnv != "" {
			s.Env[p.KeyEnv] = PlaceholderKey
		}
		if p.BaseURLEnv != "" {
			s.Env[p.BaseURLEnv] = brokerURL + "/" + p.Name
		}
	}
	// The agent's own providers first (primary required), then any other
	// provider with a credential so the other agents in the image work too.
	seen := map[string]bool{}
	for i, pn := range a.Providers {
		p, ok := providers[pn]
		if !ok {
			return s, fmt.Errorf("unknown provider %q", pn)
		}
		c, ok := lookup(pn)
		if !ok {
			if i == 0 {
				return s, fmt.Errorf("agent %s needs a %s credential; run: llok auth %s", a.Name, pn, pn)
			}
			continue
		}
		add(p, c)
		seen[pn] = true
	}
	for _, pn := range ProviderNames() {
		if seen[pn] {
			continue
		}
		if c, ok := lookup(pn); ok {
			add(providers[pn], c)
		}
	}
	for k, v := range a.Env {
		// Agent-specific values win, e.g. a base URL that needs a path.
		s.Env[k] = strings.ReplaceAll(v, "{broker}", brokerURL)
	}
	return s, nil
}

// Hosts returns every host the session's providers use.
func (s Session) Hosts() []string {
	var out []string
	for _, p := range s.Providers {
		out = append(out, p.Hosts...)
	}
	return out
}

// Primary is the configured agent's main provider, if it has one.
func (s Session) Primary() (Provider, bool) {
	if len(s.Providers) == 0 || len(s.Agent.Providers) == 0 {
		return Provider{}, false
	}
	return s.Providers[0], true
}

// EnvScript renders the guest environment as shell exports.
func (s Session) EnvScript() string {
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("export " + k + "='" + strings.ReplaceAll(s.Env[k], "'", `'\''`) + "'\n")
	}
	return b.String()
}
