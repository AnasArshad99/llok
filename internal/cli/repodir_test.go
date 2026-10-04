package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRepoDirRefusesDangerousDirs(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, d := range []string{"/", "/Users", "/etc", "/tmp", home, filepath.Join(home, "Documents"), filepath.Dir(home)} {
		if err := checkRepoDir(d); err == nil {
			t.Errorf("checkRepoDir(%q) should refuse", d)
		}
	}
	if err := checkRepoDir(t.TempDir()); err != nil {
		t.Errorf("a plain directory should be allowed: %v", err)
	}
}
