// Package trust remembers which repositories' llok.yaml policies the user
// has approved. A policy file travels with the repo, so for a cloned repo
// it is written by someone else: anything in it that widens what the agent
// may do (extra allowed hosts, changed supervisor rules) must be shown to
// the user and approved once before it takes effect. Approval is tied to a
// fingerprint of those parts, so any later change asks again.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Store is the on-disk map of repo path -> approved policy fingerprint.
type Store struct {
	path string
	mu   sync.Mutex
	m    map[string]string
}

// Open loads (or creates) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, m: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.m); err != nil {
		return nil, err
	}
	return s, nil
}

// Fingerprint hashes the widening parts of a policy. allow is the host
// allowlist; rules is any representation of the rule overrides (JSON).
func Fingerprint(allow []string, rules any) string {
	a := append([]string(nil), allow...)
	sort.Strings(a)
	rb, _ := json.Marshal(rules)
	h := sha256.New()
	ab, _ := json.Marshal(a)
	h.Write(ab)
	h.Write([]byte{0})
	h.Write(rb)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Approved reports whether fp is the approved fingerprint for repo.
func (s *Store) Approved(repo, fp string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[repo] == fp
}

// Approve records fp for repo and saves the store.
func (s *Store) Approve(repo, fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[repo] = fp
	return s.save()
}

// Revoke forgets repo.
func (s *Store) Revoke(repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, repo)
	return s.save()
}

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
