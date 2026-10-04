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
	// Setup runs as the unprivileged user at every boot, with the session
	// environment exported, to mark the agent as signed in and skip its
	// first-run wizard. The home directory is fresh each boot.
	Setup string `yaml:"setup,omitempty"`
	Note  string `yaml:"-"` // one line for pickers
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
		Name:        "openai",
		Hosts:       []string{"api.openai.com", "chatgpt.com", "auth.openai.com", "files.openai.com"},
		Upstream:    "https://api.openai.com",
		KeyEnv:      "OPENAI_API_KEY",
		BaseURLEnv:  "OPENAI_BASE_URL",
		KeyHeader:   "Authorization",
		KeyFormat:   "Bearer {secret}",
		TokenHeader: "Authorization", // ChatGPT login: Codex sends its access token as a bearer
		TokenFormat: "Bearer {secret}",
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
		// Skip the wizard (theme, sign-in, folder trust); the installer may
		// have left a .claude.json, so merge rather than overwrite.
		Setup: `{ [ -s "$HOME/.claude.json" ] && cat "$HOME/.claude.json" || echo '{}'; } \
  | jq --arg repo "$LLOK_REPO" '. + {hasCompletedOnboarding:true, theme:"dark"}
      | .projects = (.projects // {})
      | .projects[$repo] = ((.projects[$repo] // {}) + {hasTrustDialogAccepted:true})' \
  > "$HOME/.claude.json.new" 2>/dev/null && mv "$HOME/.claude.json.new" "$HOME/.claude.json"`,
		Note: "Anthropic's CLI; subscription login or API key",
	},
	"codex": {
		Binary:    "codex",
		Install:   "npm install -g @openai/codex",
		Providers: []string{"openai"},
		// Codex only counts itself signed in with an auth file. Seed it in
		// the mode the user signed in with: an API key, or a ChatGPT login
		// (access/refresh tokens are placeholders the broker swaps; the
		// account id and identity token are not secrets and travel as-is).
		Setup: `mkdir -p "$HOME/.codex"
if [ ! -f "$HOME/.codex/auth.json" ]; then
  if [ "$LLOK_CRED_OPENAI" = chatgpt ]; then
    printf '{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"%s","access_token":"llok-placeholder-token","refresh_token":"llok-placeholder-token","account_id":"%s"},"last_refresh":"%s"}\n' \
      "$LLOK_OPENAI_ID_TOKEN" "$LLOK_OPENAI_ACCOUNT_ID" "$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)" > "$HOME/.codex/auth.json"
    MODE=chatgpt
  else
    printf '{"auth_mode":"apikey","OPENAI_API_KEY":"%s"}\n' "$OPENAI_API_KEY" > "$HOME/.codex/auth.json"
    MODE=apikey
  fi
  grep -q preferred_auth_method "$HOME/.codex/config.toml" 2>/dev/null || printf 'preferred_auth_method = "%s"\n' "$MODE" >> "$HOME/.codex/config.toml"
fi`,
		Note: "OpenAI's CLI; ChatGPT login or API key",
	},
	"gemini-cli": {
		Binary:    "gemini",
		Install:   "npm install -g @google/gemini-cli",
		Providers: []string{"google"},
		// Skip the auth picker: the key comes from the environment.
		Setup: `mkdir -p "$HOME/.gemini"
[ -f "$HOME/.gemini/settings.json" ] || echo '{"selectedAuthType":"gemini-api-key","security":{"auth":{"selectedType":"gemini-api-key"}}}' > "$HOME/.gemini/settings.json"`,
		Note: "Google's CLI; Gemini API key",
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
	Kind   string // "apikey", "oauth" or "chatgpt"
	Secret string
	Meta   map[string]string // non-secret companions, exported to the guest
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
		up := strings.ToUpper(p.Name)
		s.Env["LLOK_CRED_"+up] = c.Kind
		for k, v := range c.Meta {
			s.Env["LLOK_"+up+"_"+strings.ToUpper(k)] = v
		}
		switch {
		case c.Kind == "oauth" && p.TokenEnv != "":
			s.Env[p.TokenEnv] = PlaceholderToken
		case c.Kind == "chatgpt":
			// the agent reads its login from a file the setup script seeds
		case p.KeyEnv != "":
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

// SetupScript renders the first-run setup of every built-in agent whose
// primary provider has a credential in this session, plus the configured
// agent if it is custom. It runs as the agent user with the env exported.
func (s Session) SetupScript() string {
	var b strings.Builder
	b.WriteString("# generated by llok for this session\n")
	emit := func(a Agent) {
		if a.Setup == "" || len(a.Providers) == 0 {
			return
		}
		if _, ok := s.Creds[a.Providers[0]]; !ok {
			return
		}
		b.WriteString("# " + a.Name + "\n" + a.Setup + "\n")
	}
	for _, n := range Names() {
		a := builtin[n]
		a.Name = n
		emit(a)
	}
	if _, ok := builtin[s.Agent.Name]; !ok {
		emit(s.Agent)
	}
	return b.String()
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
