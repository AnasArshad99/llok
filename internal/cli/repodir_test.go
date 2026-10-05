package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRepoDirRefusesDangerousDirs(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, d := range []string{
		"/", "/Users", "/etc", "/tmp", "/usr/local/bin", "/Library/LaunchDaemons",
		home, filepath.Join(home, "Documents"), filepath.Dir(home),
		filepath.Join(home, "Library"), filepath.Join(home, "Library", "LaunchAgents"),
	} {
		if err := checkRepoDir(d); err == nil {
			t.Errorf("checkRepoDir(%q) should refuse", d)
		}
	}
	if err := checkRepoDir(t.TempDir()); err != nil {
		t.Errorf("a plain scratch directory should be allowed: %v", err)
	}
}

func TestCheckRepoDirFollowsSymlinks(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := t.TempDir()
	link := filepath.Join(dir, "project")
	if err := os.Symlink(home, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	err := checkRepoDir(link)
	if err == nil {
		t.Fatal("a symlink to the home folder must be refused")
	}
	if !strings.Contains(err.Error(), "home folder") || !strings.Contains(err.Error(), "via") {
		t.Errorf("error should name the real target and the link: %v", err)
	}

	// A symlink to an ordinary folder is fine.
	ok := filepath.Join(dir, "real")
	os.Mkdir(ok, 0o755)
	os.Symlink(ok, filepath.Join(dir, "alias"))
	if err := checkRepoDir(filepath.Join(dir, "alias")); err != nil {
		t.Errorf("symlink to a normal folder should be allowed: %v", err)
	}
}
