package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/agents"
	"github.com/AnasArshad99/llok/internal/broker"
	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/control"
	"github.com/AnasArshad99/llok/internal/creds"
	"github.com/AnasArshad99/llok/internal/guard"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/ui"
	"github.com/AnasArshad99/llok/internal/vm"
	"github.com/AnasArshad99/llok/internal/watch"
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
		agentFl string
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
			absRepo, err := filepath.Abs(repo)
			if err != nil {
				return err
			}
			if err := checkRepoDir(absRepo); err != nil {
				return err
			}
			// First run in this repo: set it up rather than boot on silent
			// defaults. Interactively that is the same flow as llok init.
			if _, statErr := os.Stat(filepath.Join(absRepo, config.FileName)); os.IsNotExist(statErr) {
				if command == "" && isTTY() {
					fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · no "+config.FileName+" in "+absRepo+" yet. A few questions, then the VM boots.")
					fmt.Fprintln(os.Stderr)
					if _, err := setupRepo(absRepo, true); err != nil {
						return err
					}
				} else {
					fmt.Fprintln(os.Stderr, styleDim.Render("no "+config.FileName+" in "+absRepo+"; using defaults (run llok init to create one)"))
				}
			}
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
			kernel, initrd, err := guest.Paths()
			if err != nil {
				return err
			}
			// The image must be the one its manifest describes. Quick check
			// here (kernel and initramfs hashed, root disk size); llok doctor
			// hashes everything.
			if os.Getenv("LLOK_SKIP_VERIFY") != "1" {
				m, err := guest.LoadManifest()
				if err != nil {
					return errors.New("the guest image has no manifest; rebuild it with: llok guest build")
				}
				if err := m.Verify(true); err != nil {
					return fmt.Errorf("guest image does not match its manifest (%v); rebuild with: llok guest build", err)
				}
			}
			if err := ensureTrusted(cfg, absRepo); err != nil {
				return err
			}
			if cfg.Network.Mode == "none" && cfg.Agent.Primary() != "shell" && cfg.Agent.Primary() != "" {
				fmt.Fprintln(os.Stderr, warn("network.mode is none: the agent cannot reach its provider and will fail to sign in."))
				fmt.Fprintln(os.Stderr, styleDim.Render("  set  network: mode: broker  in "+config.FileName+" (or run: llok init --force), then: llok auth status"))
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

			// Which agent this session is for, and what it needs. --agent
			// overrides llok.yaml for this run only.
			if agentFl != "" {
				cfg.Agent = config.AgentList{agentFl}
			}
			agent, err := cfg.ResolveAgent()
			if err != nil {
				return err
			}
			if agent.Name == "shell" && command == "" && isTTY() && len(cfg.Agent) == 0 {
				if names, err := agentPicker(nil); err == nil && len(names) > 0 {
					agent, _ = agents.Resolve(names[0], cfg.Agents)
				}
			}
			// Secondary agents only need a warning when their provider has
			// no credential; the default agent's provider is required.
			if others, err := cfg.ResolveAgents(); err == nil {
				for _, o := range others[min(1, len(others)):] {
					if len(o.Providers) > 0 {
						if _, err := creds.Get(o.Providers[0]); errors.Is(err, creds.ErrNotFound) {
							fmt.Fprintln(os.Stderr, warn(o.Name+" is listed but "+o.Providers[0]+" is not signed in; run: llok auth "+o.Providers[0]))
						}
					}
				}
			}

			// The broker: only way out of the VM. Needs credentials for the
			// agent's providers (at least the primary one).
			var brk *broker.Broker
			var shareDir string
			var session agents.Session
			if cfg.Network.Mode == "broker" {
				var lookupErr error
				session, err = agents.Prepare(agent, func(p string) (agents.Credential, bool) {
					c, err := creds.Get(p)
					if errors.Is(err, creds.ErrNotFound) {
						return agents.Credential{}, false
					}
					if err != nil {
						lookupErr = err
						return agents.Credential{}, false
					}
					return agents.Credential{Kind: c.Kind, Secret: c.Secret, Meta: c.Meta}, true
				}, "http://127.0.0.1:3128")
				if lookupErr != nil {
					return lookupErr
				}
				if err != nil {
					return fmt.Errorf("%w  (or set network.mode: none)", err)
				}
				var routes []broker.Route
				for _, p := range session.Providers {
					c, ok := session.Creds[p.Name]
					if !ok {
						continue
					}
					r := broker.Route{Name: p.Name, Hosts: p.Hosts, Cred: broker.Credential{Kind: c.Kind, Secret: c.Secret}}
					if (c.Kind == "oauth" || c.Kind == "chatgpt") && p.TokenHeader != "" {
						r.Header, r.Format = p.TokenHeader, p.TokenFormat
					} else {
						r.Header, r.Format = p.KeyHeader, p.KeyFormat
					}
					r.Upstream, _ = url.Parse(p.Upstream)
					routes = append(routes, r)
				}
				home, _ := guest.Home()
				os.MkdirAll(filepath.Join(home, "audit"), 0o700)
				brk, err = broker.New(cfg.Name, broker.Policy{Allow: cfg.Network.Allow}, routes,
					filepath.Join(home, "audit", "broker.jsonl"))
				if err != nil {
					return err
				}
				defer brk.Close()
				// The guest trusts llok's CA so TLS to provider hosts can be
				// terminated here for credential injection.
				ca, err := broker.LoadOrCreateCA(filepath.Join(home, "ca"))
				if err != nil {
					return fmt.Errorf("broker CA: %w", err)
				}
				brk.CA = ca
				// A per-session read-only share carries the CA and the agent's
				// environment (placeholders only, never real secrets).
				shareDir, err = os.MkdirTemp(filepath.Join(home, "share"), "session-")
				if err != nil {
					os.MkdirAll(filepath.Join(home, "share"), 0o755)
					shareDir, err = os.MkdirTemp(filepath.Join(home, "share"), "session-")
					if err != nil {
						return err
					}
				}
				defer os.RemoveAll(shareDir)
				if err := os.WriteFile(filepath.Join(shareDir, "ca.pem"), ca.PEM, 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(shareDir, "env.sh"), []byte(session.EnvScript()), 0o644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(shareDir, "setup.sh"), []byte(session.SetupScript()), 0o644); err != nil {
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
			if devNAT {
				if os.Getenv("LLOK_DEV") != "1" {
					return errors.New("--dev-nat attaches a network device to a session VM; set LLOK_DEV=1 if you really mean it")
				}
				opts.NAT = true
			}
			if hasRootfs {
				opts.Disk = rootfs
				opts.DiskReadOnly = true
				opts.RootDisk = true
			}
			// Supervisor: host-side rules over repo file changes and broker
			// decisions, with a session ledger. Metadata only.
			home, _ := guest.Home()
			ledger, err := guard.OpenLedger(filepath.Join(home, "sessions"), cfg.Name)
			if err != nil {
				return err
			}
			defer ledger.Close()
			eng := guard.New(cfg.GuardRules())
			eng.Ledger = ledger
			var machine vm.Machine
			eng.Machine = func() guard.Machine { return machine }
			opts.OnStart = func(m vm.Machine) { machine = m }
			stopWatch, err := watch.Start(absRepo, func(p, op string) {
				eng.Observe(guard.Event{Kind: guard.KindFile, Path: p, Op: op})
			})
			if err != nil {
				return fmt.Errorf("watch repo: %w", err)
			}
			defer stopWatch()
			if brk != nil {
				brk.OnEvent = func(e broker.Event) {
					eng.Observe(guard.Event{Kind: guard.KindNet, Host: e.Host, Decision: e.Decision})
				}
			}

			ctl := control.New()
			var exitMu sync.Mutex
			exitCode, exitSeen := 0, false
			ctl.OnReply = func(r control.Reply) {
				if r.Op == "exit" {
					exitMu.Lock()
					exitCode, exitSeen = r.Code, true
					exitMu.Unlock()
				}
			}
			opts.ExitCode = func() (int, bool) {
				exitMu.Lock()
				defer exitMu.Unlock()
				return exitCode, exitSeen
			}
			opts.Vsock = map[uint32]func(net.Listener){
				control.Port: ctl.Serve,
			}
			opts.GracefulStop = func() bool { return ctl.Shutdown() == nil }
			opts.AgentBinary = agent.Binary
			if brk != nil {
				opts.ShareDir = shareDir
				opts.Broker = true
				opts.Vsock[3128] = func(l net.Listener) { brk.Serve(l) }
			}

			var sess *ui.Session
			if useUI {
				var provNames []string
				for _, p := range session.Providers {
					if _, ok := session.Creds[p.Name]; ok {
						provNames = append(provNames, p.Name)
					}
				}
				sess = ui.New(ui.Info{
					Agent:     agent.Name,
					Providers: provNames,
					Network:   cfg.Network.Mode,
					Allow:     len(cfg.Network.Allow),
					Project:   cfg.Name,
					RepoHost:  absRepo,
					RepoVM:    cfg.Mount.Repo,
					CPUs:      cpus,
					MemoryMB:  memMB,
					Kernel:    guest.KernelVersion(),
				})
				opts.Cols, opts.Rows = sess.ConsoleSize()
				opts.Output = sess
				opts.Input = sess.Input(os.Stdin)
				opts.Replies = sess.Replies()
				opts.OnMarker = sess.Marker
				sess.OnResize = func(cols, rows int) { ctl.Resize(cols, rows) }
				eng.Notify = sess.Event
				eng.Ask = func(q string, opts []guard.Option) byte {
					uo := make([]ui.Option, len(opts))
					for i, o := range opts {
						uo[i] = ui.Option{Key: o.Key, Label: o.Label}
					}
					sess.SetPaused(true)
					k := sess.Ask(q, uo)
					sess.SetPaused(false)
					return k
				}
				if brk != nil {
					brk.OnEvent = func(e broker.Event) {
						sess.Event(e.Line())
						eng.Observe(guard.Event{Kind: guard.KindNet, Host: e.Host, Decision: e.Decision})
					}
				}
				sess.Start()
			} else {
				// Outside the UI the guest's bytes reach a real terminal or a
				// log: keep text and colours, drop everything that could
				// program the terminal.
				opts.Output = io.Writer(os.Stdout)
				opts.Sanitize = true
			}

			res, err := vm.Run(ctx, opts)
			if sess != nil {
				sess.Stop()
			}
			if why := eng.StoppedBy(); why != "" {
				fmt.Fprintln(os.Stderr, styleErr.Render("llok")+" · vm stopped by the supervisor: "+why)
				os.Exit(125)
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
	cmd.Flags().StringVar(&agentFl, "agent", "", "agent to set this session up for (overrides llok.yaml)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print the boot summary and run summary")
	cmd.Flags().BoolVar(&devNAT, "dev-nat", false, "DEVELOPMENT ONLY: attach a NAT network device")
	_ = cmd.Flags().MarkHidden("dev-nat")
	return cmd
}
