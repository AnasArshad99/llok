// Package vm boots a locked Linux microVM on Apple's Virtualization framework.
package vm

import (
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
	Repo           string // host directory shared into the guest
	RepoPath       string // where the guest mounts it (default /repo)
	Name           string // project name, shown in the guest prompt
	CPUs           uint
	MemoryMB       uint64
	Command        string // shell snippet run in the repo; empty means interactive shell
	Interactive    bool

	HideBanner   bool   // suppress the in-guest banner (the host UI shows it instead)
	Disk         string // optional root disk image, attached as /dev/vda
	DiskReadOnly bool
	RootDisk     bool   // tell the guest to switch_root onto the disk
	NAT          bool   // attach a NAT network device (builder VMs only!)
	Mode         string // guest mode, e.g. "build"

	Output   io.Writer // guest console output (default os.Stdout)
	Input    io.Reader // guest console input (default os.Stdin)
	Replies  io.Reader // optional second input: terminal query replies from a UI emulator
	Cols     int       // terminal size reported to the guest (0 = detect)
	Rows     int
	OnMarker func(string) // called for each out-of-band guest marker
}

// Result reports how the run ended.
type Result struct {
	ExitCode int
	Duration time.Duration
	Forced   bool // stopped from the host (Ctrl-] twice or signal)
}

var exitRe = regexp.MustCompile(`^LLOK_EXIT (\d+)`)

// escapeKey is Ctrl-]; pressing it twice forces the VM off from the host.
const escapeKey = 0x1d

// Run boots the VM and waits for it to stop.
func Run(ctx context.Context, o Options) (Result, error) {
	var res Result
	if o.Output == nil {
		o.Output = os.Stdout
	}
	if o.Input == nil {
		o.Input = os.Stdin
	}
	if o.RepoPath == "" {
		o.RepoPath = "/repo"
	}
	if o.MemoryMB < 128 {
		return res, fmt.Errorf("memory too small: %d MiB", o.MemoryMB)
	}

	cmdline := "console=hvc0 rdinit=/init panic=-1 quiet loglevel=3 llok.repo=" + o.RepoPath
	if o.Name != "" {
		cmdline += " llok.name=" + base64.StdEncoding.EncodeToString([]byte(o.Name))
	}
	if o.Command != "" {
		cmdline += " llok.cmd=" + base64.StdEncoding.EncodeToString([]byte(o.Command))
	}
	if o.Mode != "" {
		cmdline += " llok.mode=" + o.Mode
	}
	if o.RootDisk {
		cmdline += " llok.root=1"
	}
	if o.HideBanner {
		cmdline += " llok.banner=0"
	}
	stdinFD := int(os.Stdin.Fd())
	cols, rows := o.Cols, o.Rows
	if cols == 0 && term.IsTerminal(stdinFD) {
		cols, rows, _ = term.GetSize(stdinFD)
	}
	if cols > 0 && rows > 0 {
		cmdline += fmt.Sprintf(" llok.cols=%d llok.rows=%d", cols, rows)
	}

	boot, err := vz.NewLinuxBootLoader(o.Kernel, vz.WithCommandLine(cmdline), vz.WithInitrd(o.Initrd))
	if err != nil {
		return res, fmt.Errorf("boot loader: %w", err)
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, o.CPUs, o.MemoryMB*1024*1024)
	if err != nil {
		return res, fmt.Errorf("vm config: %w", err)
	}
	platform, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return res, err
	}
	cfg.SetPlatformVirtualMachineConfiguration(platform)

	// Console: guest hvc0 <-> pipes on both sides so we can watch input for
	// the escape key and filter markers out of the output.
	inR, inW, err := os.Pipe()
	if err != nil {
		return res, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return res, err
	}
	attach, err := vz.NewFileHandleSerialPortAttachment(inR, outW)
	if err != nil {
		return res, err
	}
	serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attach)
	if err != nil {
		return res, err
	}
	cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return res, err
	}
	cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	// The repo is the only thing from the host the guest can see.
	shared, err := vz.NewSharedDirectory(o.Repo, false)
	if err != nil {
		return res, err
	}
	single, err := vz.NewSingleDirectoryShare(shared)
	if err != nil {
		return res, err
	}
	fs, err := vz.NewVirtioFileSystemDeviceConfiguration("repo")
	if err != nil {
		return res, err
	}
	fs.SetDirectoryShare(single)
	cfg.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{fs})

	// vsock is the future broker channel.
	vsock, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return res, err
	}
	cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsock})

	if o.Disk != "" {
		att, err := vz.NewDiskImageStorageDeviceAttachment(o.Disk, o.DiskReadOnly)
		if err != nil {
			return res, fmt.Errorf("disk %s: %w", o.Disk, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return res, err
		}
		cfg.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{blk})
	}

	// No network device is ever attached to a session VM. Only the rootfs
	// builder gets NAT, and it never sees a repo.
	if o.NAT {
		nat, err := vz.NewNATNetworkDeviceAttachment()
		if err != nil {
			return res, err
		}
		nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
		if err != nil {
			return res, err
		}
		mac, err := vz.NewRandomLocallyAdministeredMACAddress()
		if err != nil {
			return res, err
		}
		nic.SetMACAddress(mac)
		cfg.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{nic})
	}

	if ok, err := cfg.Validate(); !ok || err != nil {
		return res, fmt.Errorf("invalid vm config: %w", err)
	}
	machine, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return res, fmt.Errorf("create vm: %w", err)
	}

	// Guest output -> marker filter -> (noise filter in command mode) -> caller's writer.
	exitCode := -1
	var sink io.Writer = o.Output
	var noise *kernelNoise
	if !o.Interactive {
		noise = &kernelNoise{w: o.Output}
		sink = noise
	}
	filter := &markerFilter{w: sink, on: func(m string) {
		if sm := exitRe.FindStringSubmatch(m); sm != nil {
			exitCode, _ = strconv.Atoi(sm[1])
		}
		if o.OnMarker != nil {
			o.OnMarker(m)
		}
	}}
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		io.Copy(filter, outR)
		filter.Flush()
		if noise != nil {
			noise.Flush()
		}
	}()

	// Host input -> guest, watching for Ctrl-] Ctrl-].
	forceStop := make(chan struct{}, 1)
	go func() {
		defer inW.Close()
		buf := make([]byte, 1024)
		prevEsc := false
		for {
			n, err := o.Input.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				for _, b := range chunk {
					if b == escapeKey && prevEsc {
						select {
						case forceStop <- struct{}{}:
						default:
						}
						return
					}
					prevEsc = b == escapeKey
				}
				if _, err := inW.Write(chunk); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	if o.Replies != nil {
		go func() {
			buf := make([]byte, 256)
			for {
				n, err := o.Replies.Read(buf)
				if n > 0 {
					if _, werr := inW.Write(buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}

	if o.Interactive && term.IsTerminal(stdinFD) {
		old, err := term.MakeRaw(stdinFD)
		if err == nil {
			defer term.Restore(stdinFD, old)
		}
	}

	start := time.Now()
	if err := machine.Start(); err != nil {
		outW.Close()
		return res, fmt.Errorf("start vm: %w", err)
	}

	for running := true; running; {
		select {
		case st := <-machine.StateChangedNotify():
			switch st {
			case vz.VirtualMachineStateStopped, vz.VirtualMachineStateError:
				running = false
			}
		case <-forceStop:
			res.Forced = true
			if machine.CanStop() {
				_ = machine.Stop()
			}
		case <-ctx.Done():
			res.Forced = true
			if machine.CanStop() {
				_ = machine.Stop()
			}
		}
	}
	// The VM helper process keeps its own dup of the pipe's write end, so
	// closing ours never yields EOF. Close the read end to unblock the copy.
	outW.Close()
	outR.Close()
	select {
	case <-copyDone:
	case <-time.After(time.Second):
	}
	res.Duration = time.Since(start)

	if res.Forced {
		return res, nil
	}
	if (o.Command != "" || o.Mode != "") && exitCode < 0 {
		return res, fmt.Errorf("guest stopped without reporting an exit status")
	}
	if exitCode < 0 {
		exitCode = 0
	}
	res.ExitCode = exitCode
	return res, nil
}
