// Package creds stores provider credentials in the macOS Keychain via the
// `security` tool, so the VM never has to hold a real secret.
package creds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// service is the Keychain service name. Tests and development set
// LLOK_KEYCHAIN_SERVICE so they never touch the user's real credential.
var service = func() string {
	if s := os.Getenv("LLOK_KEYCHAIN_SERVICE"); s != "" {
		return s
	}
	return "llok"
}()

// Credential is what the broker injects.
type Credential struct {
	Kind   string `json:"kind"` // apikey | oauth
	Secret string `json:"secret"`
}

// ErrNotFound means nothing is stored for that provider.
var ErrNotFound = errors.New("no credential stored")

// Set stores a credential for provider (e.g. "anthropic"), replacing any old one.
func Set(provider string, c Credential) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	cmd := exec.Command("security", "add-generic-password", "-U",
		"-s", service, "-a", provider, "-l", "llok · "+provider, "-w", string(data))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Get loads the credential for provider.
func Get(provider string) (Credential, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-s", service, "-a", provider, "-w").Output()
	if err != nil {
		return Credential{}, ErrNotFound
	}
	var c Credential
	if err := json.Unmarshal(bytes.TrimSpace(out), &c); err != nil {
		return Credential{}, fmt.Errorf("keychain entry for %s is malformed", provider)
	}
	if c.Secret == "" {
		return Credential{}, ErrNotFound
	}
	return c, nil
}

// Delete removes the credential for provider.
func Delete(provider string) error {
	return exec.Command("security", "delete-generic-password",
		"-s", service, "-a", provider).Run()
}

// Masked shows enough of a secret to recognise it.
func Masked(s string) string {
	if len(s) <= 12 {
		return "••••"
	}
	return s[:8] + "…" + s[len(s)-4:]
}
