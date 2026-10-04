package guest

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed versions.yaml
var versionsYAML []byte

// Versions are the pinned inputs of the guest image.
type Versions struct {
	Alpine struct {
		Branch           string `yaml:"branch"`
		Minirootfs       string `yaml:"minirootfs"`
		MinirootfsSHA256 string `yaml:"minirootfs_sha256"`
		KernelAPK        string `yaml:"kernel_apk"`
		KernelAPKSHA256  string `yaml:"kernel_apk_sha256"`
	} `yaml:"alpine"`
	Agents map[string]string `yaml:"agents"` // agent name -> version
}

// PinnedVersions returns the versions compiled into this llok.
func PinnedVersions() (Versions, error) {
	var v Versions
	if err := yaml.Unmarshal(versionsYAML, &v); err != nil {
		return v, fmt.Errorf("embedded versions.yaml: %w", err)
	}
	if v.Alpine.Minirootfs == "" || v.Alpine.KernelAPK == "" {
		return v, fmt.Errorf("embedded versions.yaml is incomplete")
	}
	return v, nil
}

// env renders the pins as environment for the build script.
func (v Versions) env() []string {
	return []string{
		"LLOK_ALPINE_BRANCH=" + v.Alpine.Branch,
		"LLOK_ROOTFS_FILE=" + v.Alpine.Minirootfs,
		"LLOK_ROOTFS_SHA256=" + v.Alpine.MinirootfsSHA256,
		"LLOK_KERNEL_APK=" + v.Alpine.KernelAPK,
		"LLOK_KERNEL_APK_SHA256=" + v.Alpine.KernelAPKSHA256,
	}
}
