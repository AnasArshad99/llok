package agents

import (
	"strings"
	"testing"
)

func TestPrepareClaudeOAuth(t *testing.T) {
	a, _ := Resolve("claude-code", nil)
	s, err := Prepare(a, func(p string) (Credential, bool) {
		if p == "anthropic" {
			return Credential{Kind: "oauth", Secret: "x"}, true
		}
		return Credential{}, false
	}, "http://127.0.0.1:3128")
	if err != nil {
		t.Fatal(err)
	}
	if s.Env["CLAUDE_CODE_OAUTH_TOKEN"] != PlaceholderToken || s.Env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3128/anthropic" {
		t.Errorf("env = %v", s.Env)
	}
	if _, has := s.Env["ANTHROPIC_API_KEY"]; has {
		t.Error("oauth credential must not also set the key env")
	}
	if !strings.Contains(s.EnvScript(), "export DISABLE_AUTOUPDATER='1'") {
		t.Errorf("agent env missing: %s", s.EnvScript())
	}
}

func TestPrepareMissingPrimaryCredential(t *testing.T) {
	a, _ := Resolve("codex", nil)
	_, err := Prepare(a, func(string) (Credential, bool) { return Credential{}, false }, "u")
	if err == nil || !strings.Contains(err.Error(), "llok auth openai") {
		t.Errorf("expected a hint to run llok auth openai, got %v", err)
	}
}

func TestPrepareOnlyStoredProvidersRouted(t *testing.T) {
	a, _ := Resolve("gemini-cli", nil)
	s, err := Prepare(a, func(p string) (Credential, bool) {
		return Credential{Kind: "apikey", Secret: "k"}, p == "google"
	}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Creds) != 1 || s.Env["GEMINI_API_KEY"] != PlaceholderKey {
		t.Errorf("creds=%v env=%v", s.Creds, s.Env)
	}
	if _, has := s.Env["OPENAI_API_KEY"]; has {
		t.Error("provider without a credential must not get a placeholder")
	}
	if len(s.Hosts()) != 1 {
		t.Errorf("only providers with credentials are routed: %v", s.Hosts())
	}
}

func TestPrepareIncludesOtherStoredProviders(t *testing.T) {
	a, _ := Resolve("claude-code", nil)
	s, err := Prepare(a, func(p string) (Credential, bool) {
		return Credential{Kind: "apikey", Secret: "k"}, p == "anthropic" || p == "openai"
	}, "http://b")
	if err != nil {
		t.Fatal(err)
	}
	if s.Providers[0].Name != "anthropic" {
		t.Errorf("the agent's own provider must come first, got %s", s.Providers[0].Name)
	}
	if s.Env["OPENAI_API_KEY"] != PlaceholderKey || s.Env["OPENAI_BASE_URL"] != "http://b/openai" {
		t.Errorf("other stored providers should be usable: %v", s.Env)
	}
	c, _ := Resolve("codex", nil)
	cs, _ := Prepare(c, func(p string) (Credential, bool) { return Credential{Kind: "apikey", Secret: "k"}, true }, "http://b")
	if cs.Env["OPENAI_BASE_URL"] != "http://b/openai" {
		t.Errorf("codex base url: %v", cs.Env["OPENAI_BASE_URL"])
	}
}

func TestCustomAgentAndValidation(t *testing.T) {
	custom := map[string]Agent{"mine": {Binary: "mytool", Providers: []string{"openai"}}}
	a, err := Resolve("mine", custom)
	if err != nil || a.Name != "mine" {
		t.Fatalf("resolve custom: %v", err)
	}
	if _, err := Resolve("nope", nil); err == nil {
		t.Error("unknown agent should error")
	}
	if err := Validate(Agent{Name: "x", Binary: "b", Providers: []string{"martian"}}); err == nil {
		t.Error("unknown provider should be rejected")
	}
}

func TestEnvScriptQuoting(t *testing.T) {
	s := Session{Env: map[string]string{"A": "it's"}}
	if got := s.EnvScript(); got != "export A='it'\\''s'\n" {
		t.Errorf("got %q", got)
	}
}

func TestSetupScriptOnlyForStoredProviders(t *testing.T) {
	a, _ := Resolve("claude-code", nil)
	s, _ := Prepare(a, func(p string) (Credential, bool) {
		return Credential{Kind: "apikey", Secret: "k"}, p == "anthropic" || p == "openai"
	}, "u")
	sc := s.SetupScript()
	if !strings.Contains(sc, "# claude-code") || !strings.Contains(sc, "# codex") {
		t.Errorf("setup for stored providers missing: %s", sc)
	}
	if strings.Contains(sc, "# gemini-cli") {
		t.Error("no google credential, gemini setup must not run")
	}
}
