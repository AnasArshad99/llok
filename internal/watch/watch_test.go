package watch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsevents"
)

func TestOpForUsesDiskStateForDeletes(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live")
	os.WriteFile(live, []byte("x"), 0o644)
	gone := filepath.Join(dir, "gone")

	if op := opFor(fsevents.ItemRemoved|fsevents.ItemIsFile, gone); op != "delete" {
		t.Errorf("missing file with Removed flag = %q, want delete", op)
	}
	// Sticky flags: a file deleted earlier but present now is not a delete.
	if op := opFor(fsevents.ItemRemoved|fsevents.ItemCreated|fsevents.ItemModified|fsevents.ItemIsFile, live); op == "delete" {
		t.Error("a file that exists must not be reported as deleted")
	}
	if op := opFor(fsevents.ItemCreated|fsevents.ItemIsFile, live); op != "create" {
		t.Errorf("create = %q", op)
	}
	if op := opFor(fsevents.ItemIsDir|fsevents.ItemCreated, dir); op != "" {
		t.Errorf("directory creation is noise, got %q", op)
	}
}

func TestInterestingKeepsHostExecutingGitPaths(t *testing.T) {
	for p, want := range map[string]bool{
		".git/hooks/pre-commit": true, ".git/config": true, ".git/info/attributes": true,
		".git/objects/ab/cd": false, ".git/index": false, "src/main.go": true,
	} {
		if interesting(p) != want {
			t.Errorf("interesting(%q) = %v", p, !want)
		}
	}
}
