// Package watch reports which files under the repo changed, from the host
// side, using macOS FSEvents. Paths and operations only, never contents.
package watch

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsevents"
)

// Start watches root and calls fn(relativePath, op) for each change, with
// op one of create, write, delete, rename. Returns a stop function.
func Start(root string, fn func(path, op string)) (func(), error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	es := &fsevents.EventStream{
		Paths:   []string{root},
		Latency: 100 * time.Millisecond,
		Flags:   fsevents.FileEvents | fsevents.NoDefer,
	}
	if err := es.Start(); err != nil {
		return nil, err
	}
	go func() {
		for batch := range es.Events {
			for _, ev := range batch {
				rel, ok := strings.CutPrefix(ev.Path, root+"/")
				if !ok || rel == "" || !interesting(rel) {
					continue
				}
				if op := opFor(ev.Flags, filepath.Join(root, rel)); op != "" {
					fn(rel, op)
				}
			}
		}
	}()
	return es.Stop, nil
}

// interesting drops git's object and index churn but keeps the parts of
// .git that influence what runs on the host.
func interesting(rel string) bool {
	if strings.HasPrefix(rel, ".git/") {
		return strings.HasPrefix(rel, ".git/hooks/") ||
			strings.HasPrefix(rel, ".git/info/") ||
			rel == ".git/config"
	}
	return true
}

// opFor maps FSEvents flags to one operation. The flags are sticky for a
// path within a stream (a file deleted earlier and recreated still carries
// ItemRemoved), so the current state on disk decides between delete and
// the rest.
func opFor(f fsevents.EventFlags, abs string) string {
	_, statErr := os.Lstat(abs)
	gone := os.IsNotExist(statErr)
	if f&fsevents.ItemIsDir != 0 && !gone {
		return "" // directory creations/mods are noise; deletions are not
	}
	switch {
	case gone && f&(fsevents.ItemRemoved|fsevents.ItemRenamed) != 0:
		return "delete"
	case gone:
		return ""
	case f&fsevents.ItemRenamed != 0:
		return "rename"
	case f&fsevents.ItemCreated != 0:
		return "create"
	case f&fsevents.ItemModified != 0:
		return "write"
	}
	return ""
}
