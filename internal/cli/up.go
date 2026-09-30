package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/muhammadanas/llok/internal/config"
	"github.com/muhammadanas/llok/internal/guest"
	"github.com/muhammadanas/llok/internal/ui"
	"github.com/muhammadanas/llok/internal/vm"
)

func newUpCmd() *cobra.Command {
	var (
		command string
		cpus    uint
		memMB   uint64
		repo    string
		plain   bool
		verbose bool
		devNAT  bool
	)
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Boot a locked microVM with the repo mounted inside",
		Long: `Boots a fresh microVM with no network device attached and the repo shared
read-write inside it. Without --cmd you get a full-screen session: the guest
shell on the left, VM status on the right. With --cmd the command runs in the
repo and the VM powers off when it exits. Press Ctrl-] twice to force the VM
off from the host.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(repo)
			if err != nil {
				return err
			}
			if cpus == 0 {
				cpus = cfg.VM.CPUs
			}
			if memMB == 0 {
				memMB = cfg.VM.MemoryMB
			}
			absRepo, err := filepath.Abs(repo)
			if err != nil {
				return err
			}
			kernel, initrd, err := guest.Paths()
			if err != nil {
				return err
			}

			interactive := command == ""
			useUI := interactive && isTTY() && !plain

			if verbose || (interactive && !useUI) {
				mode := "shell  " + styleDim.Render("(exit to power off · Ctrl-] twice to force)")
				if !interactive {
					mode = "run  " + styleDim.Render(command)
				}
				printBox("llok · booting locked VM", kv([][2]string{
					{"project", cfg.Name},
					{"repo", absRepo + "  →  " + cfg.Mount.Repo},
					{"vm", fmt.Sprintf("%d vCPU · %d MiB · Alpine linux-virt", cpus, memMB)},
					{"network", "none"},
					{"mode", mode},
				}))
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
			defer cancel()

			rootfs, hasRootfs := guest.RootfsPath()
			opts := vm.Options{
				Kernel:      kernel,
				Initrd:      initrd,
				Repo:        absRepo,
				RepoPath:    cfg.Mount.Repo,
				Name:        cfg.Name,
				CPUs:        cpus,
				MemoryMB:    memMB,
				Command:     command,
				Interactive: interactive,
				HideBanner:  useUI,
			}
			opts.NAT = devNAT
			if hasRootfs {
				opts.Disk = rootfs
				opts.DiskReadOnly = true
				opts.RootDisk = true
			}

			var sess *ui.Session
			if useUI {
				sess = ui.New(ui.Info{
					Project:  cfg.Name,
					RepoHost: absRepo,
					RepoVM:   cfg.Mount.Repo,
					CPUs:     cpus,
					MemoryMB: memMB,
					Kernel:   guest.KernelVersion(),
				})
				opts.Cols, opts.Rows = sess.ConsoleSize()
				opts.Output = sess
				opts.Input = sess.Input(os.Stdin)
				opts.Replies = sess.Replies()
				opts.OnMarker = sess.Marker
				sess.Start()
			} else {
				opts.Output = io.Writer(os.Stdout)
			}

			res, err := vm.Run(ctx, opts)
			if sess != nil {
				sess.Stop()
			}
			if err != nil {
				return err
			}

			dur := res.Duration.Round(100 * time.Millisecond)
			if res.Duration < time.Second {
				dur = res.Duration.Round(time.Millisecond)
			}
			switch {
			case res.Forced:
				fmt.Fprintln(os.Stderr, styleWarn.Render("llok")+" · vm forced off after "+dur.String())
			case res.ExitCode != 0:
				fmt.Fprintln(os.Stderr, styleErr.Render("llok")+fmt.Sprintf(" · vm ran %s · exit %d", dur, res.ExitCode))
				os.Exit(res.ExitCode)
			default:
				if interactive || verbose {
					fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · vm ran "+dur.String()+" · powered off")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&command, "cmd", "c", "", "command to run inside the repo (non-interactive)")
	cmd.Flags().UintVar(&cpus, "cpus", 0, "vCPUs (default from llok.yaml)")
	cmd.Flags().Uint64Var(&memMB, "mem", 0, "memory in MiB (default from llok.yaml)")
	cmd.Flags().StringVar(&repo, "repo", ".", "repo directory to mount inside the VM")
	cmd.Flags().BoolVar(&plain, "plain", false, "no full-screen UI; raw console in the current terminal")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print the boot summary and run summary")
	cmd.Flags().BoolVar(&devNAT, "dev-nat", false, "DEVELOPMENT ONLY: attach a NAT network device")
	_ = cmd.Flags().MarkHidden("dev-nat")
	return cmd
}
