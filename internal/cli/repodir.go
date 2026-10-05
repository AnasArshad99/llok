package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AnasArshad99/llok/internal/config"
)

// checkRepoDir refuses directories that must never be shared read-write
// into a VM: the filesystem root, the user's home folder (Music, Downloads,
// Documents, Keychains and everything else live there), and system trees,
// including everything underneath them. The path is resolved first so a
// shortcut (symlink) into a refused place is caught by where it leads.
func checkRepoDir(abs string) error {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", abs, err)
	}
	real = filepath.Clean(real)
	home, _ := os.UserHomeDir()
	if home != "" {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = filepath.Clean(h)
		}
	}
	if real != abs {
		abs = real + " (via " + abs + ")"
	}

	// Exact refusals: sharing these is sharing far more than a project.
	exact := map[string]string{
		"/":        "the whole filesystem",
		"/Users":   "every user's home folder",
		"/Volumes": "all mounted volumes",
		"/tmp":     "a shared temporary directory",
	}
	if why, ok := exact[real]; ok {
		return fmt.Errorf("refusing to share %s (%s) into a VM; run llok inside a project folder", abs, why)
	}
	if home != "" && (real == home || strings.HasPrefix(home, real+"/")) {
		return fmt.Errorf("refusing to share your home folder (%s) into a VM: the agent would get your documents, downloads, music, keychains and everything else; cd into a project folder first", abs)
	}

	// Subtree refusals: anything under these is off limits, not just the
	// folder itself. ~/Library holds LaunchAgents, keychains and app data.
	trees := []string{"/System", "/Library", "/private", "/etc", "/var", "/usr", "/bin", "/sbin", "/opt", "/cores", "/dev"}
	if home != "" {
		trees = append(trees, filepath.Join(home, "Library"))
	}
	for _, t := range trees {
		if real == t || strings.HasPrefix(real, t+"/") {
			// Temporary folders under /private/var/folders and /private/tmp
			// are ordinary scratch space; everything else under /private is not.
			if strings.HasPrefix(real, "/private/tmp/") || strings.HasPrefix(real, "/private/var/folders/") {
				break
			}
			return fmt.Errorf("refusing to share a folder under %s (%s) into a VM; run llok inside a project folder", t, abs)
		}
	}

	// Top-level personal folders: a project lives inside them, not at them.
	if home != "" {
		for _, sub := range []string{"Desktop", "Documents", "Downloads", "Music", "Pictures", "Movies", "Public", "Applications"} {
			if real == filepath.Join(home, sub) {
				return fmt.Errorf("refusing to share ~/%s itself into a VM; run llok inside a project folder under it", sub)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(real, config.FileName)); err != nil {
		if _, err := os.Stat(filepath.Join(real, ".git")); err != nil {
			fmt.Fprintln(os.Stderr, warn("sharing "+abs+", which has no llok.yaml and is not a git repository; everything under it is visible to the agent"))
		}
	}
	return nil
}
