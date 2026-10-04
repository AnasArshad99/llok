package guest

import (
	"os"
	"path/filepath"
	"testing"
)

// Uses the APKINDEX the build cached, if present, so no network is needed.
func TestAPKVersionFromCachedIndex(t *testing.T) {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".llok", "cache", "APKINDEX.tar.gz"))
	if err != nil {
		t.Skip("no cached APKINDEX")
	}
	v := apkVersion(data, "linux-virt")
	if v == "" {
		t.Fatal("linux-virt version not found in cached APKINDEX")
	}
	t.Logf("linux-virt %s", v)
	if apkVersion(data, "no-such-package-xyz") != "" {
		t.Error("unknown package must not match")
	}
}
