package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/broker"
	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/control"
	"github.com/AnasArshad99/llok/internal/creds"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/ui"
	"github.com/AnasArshad99/llok/internal/vm"
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

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
			defer cancel()

			rootfs, hasRootfs := guest.RootfsPath()

			// The broker: only way out of the VM. Needs a stored credential.
			var brk *broker.Broker
			var shareDir string
			if cfg.Network.Mode == "broker" {
				cred, err := creds.Get("anthropic")
				if err != nil {
					return fmt.Errorf("network.mode is broker but no Claude credential is stored; run: llok auth claude  (or set network.mode: none)")
				}
				home, _ := guest.Home()
				os.MkdirAll(filepath.Join(home, "audit"), 0o700)
				brk, err = broker.New(cfg.Name, broker.Policy{Allow: cfg.Network.Allow},
					broker.Credential{Kind: cred.Kind, Secret: cred.Secret},
					filepath.Join(home, "audit", "broker.jsonl"))
				if err != nil {
					return err
				}
				defer brk.Close()
				// The guest trusts llok's CA so TLS to Anthropic's hosts can be
				// terminated here for credential injection.
				ca, err := broker.LoadOrCreateCA(filepath.Join(home, "ca"))
				if err != nil {
					return fmt.Errorf("broker CA: %w", err)
				}
				brk.CA = ca
				shareDir = filepath.Join(home, "share")
				if err := os.MkdirAll(shareDir, 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(shareDir, "ca.pem"), ca.PEM, 0o644); err != nil {
					return err
				}
			}

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
			ctl := control.New()
			opts.Vsock = map[uint32]func(net.Listener){
				control.Port: ctl.Serve,
			}
			opts.GracefulStop = func() bool { return ctl.Shutdown() == nil }
			if brk != nil {
				opts.ShareDir = shareDir
				opts.Broker = true
				opts.CredKind = brk.Cred.Kind
				opts.Vsock[3128] = func(l net.Listener) { brk.Serve(l) }
			}

			var sess *ui.Session
			if useUI {
				sess = ui.New(ui.Info{
					Network:  cfg.Network.Mode,
					Allow:    len(cfg.Network.Allow),
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
				sess.OnResize = func(cols, rows int) { ctl.Resize(cols, rows) }
				if brk != nil {
					brk.OnEvent = func(e broker.Event) { sess.Event(e.Line()) }
				}
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
				fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · vm powered off on request after "+dur.String())
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
