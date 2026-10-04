package guest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Manifest records what a guest build produced: the hash of every artifact
// and the pinned inputs and agent versions behind them. llok doctor checks
// the artifacts against it, and llok up refuses to boot on a mismatch.
type Manifest struct {
	Built     time.Time           `json:"built"`
	LlokVer   string              `json:"llok_version"`
	Versions  Versions            `json:"versions"`
	Agents    []string            `json:"agents"` // lines from the image's agents-info
	Artifacts map[string]Artifact `json:"artifacts"`
}

// Artifact is one file's identity.
type Artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func manifestPath() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "guest", "manifest.json"), nil
}

// LoadManifest reads the manifest, if any.
func LoadManifest() (*Manifest, error) {
	p, err := manifestPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

// WriteManifest hashes the current artifacts and saves the manifest.
func WriteManifest(llokVersion string, v Versions, agents []string) (*Manifest, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	m := &Manifest{Built: time.Now().UTC(), LlokVer: llokVersion, Versions: v, Agents: agents, Artifacts: map[string]Artifact{}}
	for _, name := range []string{"Image", "initrd.cpio.gz", "rootfs.img"} {
		a, err := hashFile(filepath.Join(home, "guest", name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		m.Artifacts[name] = a
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	p, _ := manifestPath()
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

// Verify compares the artifacts on disk with the manifest. quick checks
// sizes only for the large root disk; full hashes everything.
func (m *Manifest) Verify(quick bool) error {
	home, err := Home()
	if err != nil {
		return err
	}
	for name, want := range m.Artifacts {
		p := filepath.Join(home, "guest", name)
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("%s: missing", name)
		}
		if st.Size() != want.Size {
			return fmt.Errorf("%s: size %d, manifest says %d", name, st.Size(), want.Size)
		}
		if quick && name == "rootfs.img" {
			continue
		}
		got, err := hashFile(p)
		if err != nil {
			return err
		}
		if got.SHA256 != want.SHA256 {
			return fmt.Errorf("%s: sha256 %s…, manifest says %s…", name, got.SHA256[:12], want.SHA256[:12])
		}
	}
	return nil
}

func hashFile(p string) (Artifact, error) {
	f, err := os.Open(p)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}
