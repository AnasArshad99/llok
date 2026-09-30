// Package vm boots a locked Linux microVM on Apple's Virtualization framework.
package vm

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/Code-Hex/vz/v3"
	"golang.org/x/term"
)

// Options describes one VM run.
type Options struct {
	Kernel, Initrd string
	Repo           string // host directory shared at /repo
	CPUs           uint
	MemoryMB       uint64
	Command        string // shell snippet run in /repo; empty means interactive shell
	Interactive    bool
	Log            io.Writer
}

var exitRe = regexp.MustCompile(`LLOK_EXIT (\d+)`)

// Run boots the VM, waits for it to stop, and returns the guest command's exit code.
func Run(ctx context.Context, o Options) (int, error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.MemoryMB < 128 {
		return 0, fmt.Errorf("memory too small: %d MiB", o.MemoryMB)
	}

	cmdline := "console=hvc0 rdinit=/init panic=-1 quiet loglevel=3 llok.repo=/repo"
	if o.Command != "" {
		cmdline += " llok.cmd=" + base64.StdEncoding.EncodeToString([]byte(o.Command))
	}

	boot, err := vz.NewLinuxBootLoader(o.Kernel, vz.WithCommandLine(cmdline), vz.WithInitrd(o.Initrd))
	if err != nil {
		return 0, fmt.Errorf("boot loader: %w", err)
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, o.CPUs, o.MemoryMB*1024*1024)
	if err != nil {
		return 0, fmt.Errorf("vm config: %w", err)
	}
	platform, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return 0, err
	}
	cfg.SetPlatformVirtualMachineConfiguration(platform)

	// Console: guest hvc0 <-> our stdin / a pipe we tee to stdout so we can
	// watch for the exit marker without buffering interactive output.
	pr, pw, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer pr.Close()
	// VZ wants a real pipe or tty for the guest's input; /dev/null is refused.
	stdin := os.Stdin
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		inR, inW, err := os.Pipe()
		if err != nil {
			return 0, err
		}
		defer inW.Close()
		go func() { io.Copy(inW, os.Stdin); inW.Close() }()
		stdin = inR
	}
	attach, err := vz.NewFileHandleSerialPortAttachment(stdin, pw)
	if err != nil {
		return 0, err
	}
	serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attach)
	if err != nil {
		return 0, err
	}
	cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return 0, err
	}
	cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	// The repo is the only thing from the host the guest can see.
	shared, err := vz.NewSharedDirectory(o.Repo, false)
	if err != nil {
		return 0, err
	}
	single, err := vz.NewSingleDirectoryShare(shared)
	if err != nil {
		return 0, err
	}
	fs, err := vz.NewVirtioFileSystemDeviceConfiguration("repo")
	if err != nil {
		return 0, err
	}
	fs.SetDirectoryShare(single)
	cfg.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{fs})

	// vsock is the future broker channel. No network device is ever attached.
	vsock, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return 0, err
	}
	cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsock})

	if ok, err := cfg.Validate(); !ok || err != nil {
		return 0, fmt.Errorf("invalid vm config: %w", err)
	}
	machine, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return 0, fmt.Errorf("create vm: %w", err)
	}

	// Tee console output and capture the exit marker.
	var tail bytes.Buffer
	exitCode := -1
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
				tail.Write(buf[:n])
				if tail.Len() > 8192 {
					tail.Next(tail.Len() - 4096)
				}
				if m := exitRe.FindSubmatch(tail.Bytes()); m != nil {
					exitCode, _ = strconv.Atoi(string(m[1]))
				}
			}
			if err != nil {
				return
			}
		}
	}()

	if o.Interactive && term.IsTerminal(int(os.Stdin.Fd())) {
		old, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer term.Restore(int(os.Stdin.Fd()), old)
		}
	}

	start := time.Now()
	if err := machine.Start(); err != nil {
		pw.Close()
		return 0, fmt.Errorf("start vm: %w", err)
	}

	for running := true; running; {
		select {
		case st := <-machine.StateChangedNotify():
			switch st {
			case vz.VirtualMachineStateStopped, vz.VirtualMachineStateError:
				running = false
			}
		case <-ctx.Done():
			if machine.CanStop() {
				_ = machine.Stop()
			}
		}
	}
	// The VM helper process keeps its own dup of the pipe's write end, so
	// closing ours never yields EOF. Close the read end to unblock the tee.
	pw.Close()
	pr.Close()
	select {
	case <-copyDone:
	case <-time.After(time.Second):
	}
	fmt.Fprintf(o.Log, "\r\nllok: vm ran for %s\r\n", time.Since(start).Round(time.Millisecond))

	if o.Command != "" && exitCode < 0 {
		return 0, fmt.Errorf("guest stopped without reporting an exit status")
	}
	if exitCode < 0 {
		exitCode = 0
	}
	return exitCode, nil
}
