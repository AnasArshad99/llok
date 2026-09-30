package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/muhammadanas/llok/internal/config"
	"github.com/muhammadanas/llok/internal/guest"
	"github.com/muhammadanas/llok/internal/vm"
)

func newUpCmd() *cobra.Command {
	var (
		command string
		cpus    uint
		memMB   uint64
		repo    string
		quiet   bool
	)
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Boot a locked microVM with the repo mounted at /repo",
		Long: `Boots a fresh microVM with no network device attached and the repo shared
read-write at /repo. With --cmd the command runs inside /repo and the VM powers
off when it exits. Without --cmd you get an interactive shell in the guest.
Press Ctrl-] twice to force the VM off from the host.`,
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
			if !quiet {
				mode := "shell  " + styleDim.Render("(exit to power off · Ctrl-] twice to force)")
				if !interactive {
					mode = "run  " + styleDim.Render(command)
				}
				printBox("llok · booting locked VM", kv([][2]string{
					{"project", cfg.Name},
					{"repo", absRepo + "  →  /repo"},
					{"vm", fmt.Sprintf("%d vCPU · %d MiB · Alpine linux-virt", cpus, memMB)},
					{"network", "none"},
					{"mode", mode},
				}))
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
			defer cancel()

			code, err := vm.Run(ctx, vm.Options{
				Kernel:      kernel,
				Initrd:      initrd,
				Repo:        absRepo,
				Name:        cfg.Name,
				CPUs:        cpus,
				MemoryMB:    memMB,
				Command:     command,
				Interactive: interactive,
				Log:         os.Stderr,
			})
			if err != nil {
				return err
			}
			if code != 0 {
				fmt.Fprintln(os.Stderr, bad(fmt.Sprintf("guest command exited with status %d", code)))
				os.Exit(code)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&command, "cmd", "c", "", "command to run inside /repo (non-interactive)")
	cmd.Flags().UintVar(&cpus, "cpus", 0, "vCPUs (default from llok.yaml)")
	cmd.Flags().Uint64Var(&memMB, "mem", 0, "memory in MiB (default from llok.yaml)")
	cmd.Flags().StringVar(&repo, "repo", ".", "repo directory to mount at /repo")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "no banner")
	return cmd
}
