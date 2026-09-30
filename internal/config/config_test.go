package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if got.Name != "demo" || got.Agent != "claude-code" || got.VM.CPUs != 2 || got.VM.MemoryMB != 1024 ||
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
	if got.Name != filepath.Base(dir) || got.Network.Mode != "broker" {
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
