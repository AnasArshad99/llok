package guest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestWriteAndVerify(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LLOK_HOME", home)
	os.MkdirAll(filepath.Join(home, "guest"), 0o755)
	os.WriteFile(filepath.Join(home, "guest", "Image"), []byte("kernel"), 0o644)
	os.WriteFile(filepath.Join(home, "guest", "initrd.cpio.gz"), []byte("initrd"), 0o644)
	os.WriteFile(filepath.Join(home, "guest", "rootfs.img"), []byte("rootfs"), 0o644)

	v, err := PinnedVersions()
	if err != nil {
		t.Fatal(err)
	}
	m, err := WriteManifest("test", v, []string{"claude-code 1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Artifacts) != 3 {
		t.Fatalf("artifacts = %v", m.Artifacts)
	}
	loaded, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify(false); err != nil {
		t.Errorf("fresh build should verify: %v", err)
	}
	// Same size, different content: quick check passes for the disk, full fails.
	os.WriteFile(filepath.Join(home, "guest", "rootfs.img"), []byte("ROOTFS"), 0o644)
	if err := loaded.Verify(true); err != nil {
		t.Errorf("quick verify checks size only for the disk: %v", err)
	}
	if err := loaded.Verify(false); err == nil {
		t.Error("full verify must catch a modified root disk")
	}
	// A modified kernel is caught even by the quick check.
	os.WriteFile(filepath.Join(home, "guest", "Image"), []byte("KERNEL"), 0o644)
	if err := loaded.Verify(true); err == nil {
		t.Error("quick verify must hash the kernel")
	}
}

func TestPinnedVersionsLoad(t *testing.T) {
	v, err := PinnedVersions()
	if err != nil {
		t.Fatal(err)
	}
	if v.Alpine.Branch == "" || len(v.Alpine.MinirootfsSHA256) != 64 || v.Agents["claude-code"] == "" {
		t.Errorf("pins incomplete: %+v", v)
	}
}
