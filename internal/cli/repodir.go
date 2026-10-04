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
// Documents, Keychains and everything else live there), and system trees.
// Whatever directory llok runs in becomes /repo, so this is the one place
// that mistake can be caught.
func checkRepoDir(abs string) error {
	abs = filepath.Clean(abs)
	home, _ := os.UserHomeDir()
	home = filepath.Clean(home)

	forbidden := map[string]string{
		"/":        "the whole filesystem",
		"/Users":   "every user's home folder",
		"/System":  "a system directory",
		"/Library": "a system directory",
		"/private": "a system directory",
		"/etc":     "a system directory",
		"/var":     "a system directory",
		"/usr":     "a system directory",
		"/bin":     "a system directory",
		"/sbin":    "a system directory",
		"/opt":     "a system directory",
		"/tmp":     "a shared temporary directory",
		"/Volumes": "all mounted volumes",
	}
	if why, ok := forbidden[abs]; ok {
		return fmt.Errorf("refusing to share %s (%s) into a VM; run llok inside a project folder", abs, why)
	}
	if home != "" && (abs == home || strings.HasPrefix(home, abs+"/")) {
		return fmt.Errorf("refusing to share your home folder (%s) into a VM: the agent would get your documents, downloads, music, keychains and everything else; cd into a project folder first", abs)
	}
	if home != "" {
		for _, sub := range []string{"Library", "Desktop", "Documents", "Downloads", "Music", "Pictures", "Movies"} {
			if abs == filepath.Join(home, sub) {
				return fmt.Errorf("refusing to share ~/%s itself into a VM; run llok inside a project folder under it", sub)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(abs, config.FileName)); err != nil {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
			fmt.Fprintln(os.Stderr, warn("sharing "+abs+", which has no llok.yaml and is not a git repository; everything under it is visible to the agent"))
		}
	}
	return nil
}
