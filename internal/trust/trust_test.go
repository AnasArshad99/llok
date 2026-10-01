package trust

import (
	"path/filepath"
	"testing"
)

func TestFingerprintAndStore(t *testing.T) {
	a := Fingerprint([]string{"b.com", "a.com"}, nil)
	b := Fingerprint([]string{"a.com", "b.com"}, nil)
	if a != b {
		t.Error("allow order must not change the fingerprint")
	}
	if Fingerprint([]string{"a.com"}, nil) == Fingerprint([]string{"a.com"}, []string{"rule"}) {
		t.Error("rules must change the fingerprint")
	}

	p := filepath.Join(t.TempDir(), "trust.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Approved("/r", a) {
		t.Error("nothing approved yet")
	}
	if err := s.Approve("/r", a); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(p)
	if !s2.Approved("/r", a) {
		t.Error("approval should persist")
	}
	if s2.Approved("/r", Fingerprint([]string{"c.com"}, nil)) {
		t.Error("a changed policy must not be approved")
	}
	s2.Revoke("/r")
	s3, _ := Open(p)
	if s3.Approved("/r", a) {
		t.Error("revoke should persist")
	}
}
