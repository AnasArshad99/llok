// Package guest builds and locates the Linux kernel + initramfs the VM boots.
package guest

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed build.sh
var buildScript []byte

//go:embed init
var initScript []byte

// Home returns ~/.llok (override with LLOK_HOME).
func Home() (string, error) {
	if h := os.Getenv("LLOK_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".llok"), nil
}

// Paths returns the kernel and initrd paths, erroring if they are not built yet.
func Paths() (kernel, initrd string, err error) {
	home, err := Home()
	if err != nil {
		return "", "", err
	}
	kernel = filepath.Join(home, "guest", "Image")
	initrd = filepath.Join(home, "guest", "initrd.cpio.gz")
	for _, p := range []string{kernel, initrd} {
		if _, err := os.Stat(p); err != nil {
			return "", "", errors.New("guest image not built; run: llok guest build")
		}
	}
	return kernel, initrd, nil
}

// KernelVersion returns the guest kernel version recorded at build time, or "".
func KernelVersion() string {
	home, err := Home()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, "guest", "kernel.version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Build runs the embedded build script, which downloads Alpine's kernel and
// assembles the initramfs with our init.
func Build(ctx context.Context) error {
	home, err := Home()
	if err != nil {
		return err
	}
	work := filepath.Join(home, "build")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	scriptPath := filepath.Join(work, "build.sh")
	if err := os.WriteFile(scriptPath, buildScript, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, "init"), initScript, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", scriptPath)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "LLOK_HOME="+home)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("guest build failed: %w", err)
	}
	return nil
}
