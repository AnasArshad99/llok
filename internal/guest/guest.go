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
	"time"

	"github.com/AnasArshad99/llok/internal/vm"
)

//go:embed build.sh
var buildScript []byte

//go:embed init
var initScript []byte

//go:embed build-rootfs.sh
var rootfsScript []byte

// RootfsSizeGiB is the (sparse) size of the root disk image.
const RootfsSizeGiB = 8

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

// RootfsPath returns the root disk image path and whether it exists.
func RootfsPath() (string, bool) {
	home, err := Home()
	if err != nil {
		return "", false
	}
	p := filepath.Join(home, "guest", "rootfs.img")
	_, err = os.Stat(p)
	return p, err == nil
}

// BuildRootfs boots a builder VM (the only VM that gets a network device)
// which installs Alpine, the toolchain and Claude Code onto a fresh disk.
func BuildRootfs(ctx context.Context) error {
	kernel, initrd, err := Paths()
	if err != nil {
		return err
	}
	img, _ := RootfsPath()
	part := img + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	if err := f.Truncate(RootfsSizeGiB << 30); err != nil {
		f.Close()
		return err
	}
	f.Close()

	scratch, err := os.MkdirTemp("", "llok-builder-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)

	fmt.Println("llok guest: booting builder VM (NAT network, blank 8 GiB disk)")
	res, err := vm.Run(ctx, vm.Options{
		Kernel:   kernel,
		Initrd:   initrd,
		Repo:     scratch, // an empty scratch dir; the builder never sees a repo
		Name:     "builder",
		CPUs:     2,
		MemoryMB: 2048,
		Disk:     part,
		NAT:      true,
		Mode:     "build",
		Output:   os.Stdout,
	})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("builder exited with status %d", res.ExitCode)
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Rename(part, img); err != nil {
		return err
	}
	st, _ := os.Stat(img)
	fmt.Printf("llok guest: rootfs -> %s (%d GiB sparse, built in %s)\n", img, st.Size()>>30, res.Duration.Round(time.Second))
	return nil
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
	if err := os.WriteFile(filepath.Join(work, "build-rootfs.sh"), rootfsScript, 0o755); err != nil {
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
