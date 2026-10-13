package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := Default()
	c.Name = "demo"
	if err := os.WriteFile(filepath.Join(dir, FileName), c.Render(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || got.Agent.Primary() != "claude-code" || got.VM.CPUs != 2 || got.VM.MemoryMB != 1024 ||
		got.Mount.Repo != "/repo" || got.Network.Mode != "broker" {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

func TestLoadMissingUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != filepath.Base(dir) || got.Network.Mode != "broker" || got.Mount.Repo != MountPathFor(got.Name) {
		t.Errorf("defaults: %+v", got)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"cpus":  "vm:\n  cpus: 0\n  memory_mb: 512\n",
		"mode":  "network:\n  mode: wifi\n",
		"parse": "vm: [\n",
	}
	for name, body := range cases {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644)
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), FileName) {
			t.Errorf("%s: expected an error naming %s, got %v", name, FileName, err)
		}
	}
}

func TestAgentListScalarAndSequence(t *testing.T) {
	for in, want := range map[string]string{
		"agent: codex\n":                      "codex",
		"agent: [claude-code, codex]\n":       "[claude-code, codex]",
		"agent:\n  - gemini-cli\n  - codex\n": "[gemini-cli, codex]",
	} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, FileName), []byte("vm: {cpus: 1, memory_mb: 512}\n"+in), 0o644)
		c, err := Load(dir)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if c.Agent.Render() != want {
			t.Errorf("%q: got %q want %q", in, c.Agent.Render(), want)
		}
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("agent: [claude-code, nope]\n"), 0o644)
	if _, err := Load(dir); err == nil {
		t.Error("unknown agent in a list should be rejected")
	}
}

func TestMountPathFor(t *testing.T) {
	for in, want := range map[string]string{
		"career-ops": "/career-ops", "My App": "/My-App", "a/b": "/a-b", "...": "/repo", "": "/repo", "x.y_z": "/x.y_z",
	} {
		if got := MountPathFor(in); got != want {
			t.Errorf("MountPathFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExcludeDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	c := Default()
	c.Name = "x"
	os.WriteFile(filepath.Join(dir, FileName), c.Render(), 0o644)
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Mount.Exclude) != len(DefaultExclude()) || got.Mount.Exclude[0] != ".env" {
		t.Errorf("exclude round trip: %v", got.Mount.Exclude)
	}
	for _, bad := range []string{"/etc", "../x", "a b"} {
		os.WriteFile(filepath.Join(dir, FileName), []byte("mount:\n  exclude: [\""+bad+"\"]\n"), 0o644)
		if _, err := Load(dir); err == nil {
			t.Errorf("exclude %q should be rejected", bad)
		}
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("mount:\n  exclude: []\n"), 0o644)
	got, _ = Load(dir)
	if len(got.Mount.Exclude) != 0 {
		t.Error("an explicit empty list must mean no exclusions")
	}
}

func TestIdleTimeout(t *testing.T) {
	c := Default()
	if d, err := c.IdleTimeout(); err != nil || d != 2*time.Hour {
		t.Errorf("default idle: %v %v", d, err)
	}
	c.Session.Idle = "off"
	if d, _ := c.IdleTimeout(); d != 0 {
		t.Error("off must disable")
	}
	for _, bad := range []string{"soon", "10s", "-1h"} {
		c.Session.Idle = bad
		if _, err := c.IdleTimeout(); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
