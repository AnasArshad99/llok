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
	// `security -i` reads commands from stdin, so the secret never appears
	// on a command line where other processes could read it.
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -l %s -w %s\n",
		quote(service), quote(provider), quote("llok · "+provider), quote(string(data))))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// quote wraps an argument for security's interactive command parser, which
// accepts double-quoted strings with backslash escapes.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Get loads the credential for provider.
func Get(provider string) (Credential, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", service, "-a", provider, "-w")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// errSecItemNotFound exits 44; anything else is a real Keychain problem.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return Credential{}, ErrNotFound
		}
		return Credential{}, fmt.Errorf("keychain: %s", strings.TrimSpace(stderr.String()))
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
