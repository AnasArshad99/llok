package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/agents"
	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/creds"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/session"
)

func newUpCmd() *cobra.Command {
	var (
		command string
		cpus    uint
		memMB   uint64
		repo    string
		plain   bool
		verbose bool
		agentFl string
		name    string
	)
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Boot a locked microVM with the repo mounted inside",
		Long: `Boots a fresh microVM with no network device attached and the repo shared
read-write inside it. Without --cmd you get a full-screen session: the guest
shell on the left, VM status on the right. The VM runs detached from the
terminal: Ctrl-] then d leaves it running, llok attach returns to it, and a
running session for the repo is attached rather than started again. With
--cmd the command runs in the repo and the VM powers off when it exits.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			absRepo, err := filepath.Abs(repo)
			if err != nil {
				return err
			}
			if err := checkRepoDir(absRepo); err != nil {
				return err
			}
			home, err := guest.Home()
			if err != nil {
				return err
			}
			interactive := command == ""
			useUI := interactive && isTTY() && !plain

			// A session already running for this repo: join it, unless a
			// different name was asked for.
			if useUI {
				if name == "" {
					if existing := session.ForRepo(home, absRepo); len(existing) > 0 {
						pick := existing[0]
						if len(existing) > 1 {
							opts := make([]option[session.Record], len(existing))
							for i, r := range existing {
								opts[i] = option[session.Record]{Label: r.Name + "  " + styleDim.Render(r.Agent+" · up "+fmtUptime(r)), Value: r}
							}
							pick, err = selectPrompt("Several sessions are running for this repo", "", opts, 0)
							if err != nil {
								return err
							}
						}
						if !pick.Attach {
							return fmt.Errorf("session %s is running in another terminal (plain or --cmd mode) and cannot be attached", pick.Name)
						}
						fmt.Fprintln(os.Stderr, styleDim.Render("attaching to running session "+pick.Name))
						return attachLoop(home, pick.Name)
					}
				} else if r, ok := session.Find(home, name); ok {
					if !r.Attach {
						return fmt.Errorf("session %s cannot be attached", name)
					}
					return attachLoop(home, name)
				}
			}

			spec, err := prepareRun(absRepo, command, cpus, memMB, agentFl, interactive && isTTY())
			if err != nil {
				return err
			}
			spec.nat = devNATEnabled(cmd)
			if name == "" {
				name = session.FreeName(home, spec.cfg.Name)
			} else if _, ok := session.Find(home, name); ok {
				return fmt.Errorf("a session named %s is already running", name)
			}
			spec.name = name

			if useUI {
				// Full-screen sessions run detached, so they survive this
				// terminal: start the server, then attach to it.
				if err := startServer(home, spec, cmd); err != nil {
					return err
				}
				return attachLoop(home, name)
			}

			// Plain or command mode: run in this process, console on stdout.
			if verbose || interactive {
				mode := "shell  " + styleDim.Render("(exit to power off · Ctrl-] twice to force)")
				if !interactive {
					mode = "run  " + styleDim.Render(command)
				}
				printBox("llok · booting locked VM", kv([][2]string{
					{"project", spec.cfg.Name},
					{"repo", absRepo + "  →  " + spec.cfg.Mount.Repo},
					{"vm", fmt.Sprintf("%d vCPU · %d MiB · Alpine linux-virt", spec.cpus, spec.memMB)},
					{"network", spec.cfg.Network.Mode},
					{"mode", mode},
				}))
			}
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
			defer cancel()
			rt, err := newRuntime(spec)
			if err != nil {
				return err
			}
			defer rt.close()
			session.WriteRecord(home, session.Record{Name: name, Project: spec.cfg.Name, Repo: absRepo,
				Agent: spec.agent.Name, PID: os.Getpid(), Started: rt.startedOrNow(), CPUs: spec.cpus, MemoryMB: spec.memMB})
			defer session.RemoveRecord(home, name)
			// The guest's bytes reach a real terminal or a log: keep text
			// and colours, drop everything that could program the terminal.
			rt.opts.Output = io.Writer(os.Stdout)
			rt.opts.Sanitize = true
			res, err := rt.run(ctx)
			if code := summarize(res, err, rt.eng.StoppedBy(), verbose); code != 0 {
				os.Exit(code)
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
	cmd.Flags().StringVarP(&name, "session", "s", "", "session name (default: the project name, then project-2, …)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print the boot summary and run summary")
	registerDevFlags(cmd)
	return cmd
}

// prepareRun does the part of a run that may ask questions: first-run
// setup, image check, trust, agent choice. It returns a spec that needs
// no further input.
func prepareRun(absRepo, command string, cpus uint, memMB uint64, agentFl string, tty bool) (runSpec, error) {
	var spec runSpec
	// First run in this repo: set it up rather than boot on silent
	// defaults. Interactively that is the same flow as llok init.
	if _, statErr := os.Stat(filepath.Join(absRepo, config.FileName)); os.IsNotExist(statErr) {
		if tty {
			fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · no "+config.FileName+" in "+absRepo+" yet. A few questions, then the VM boots.")
			fmt.Fprintln(os.Stderr)
			if _, err := setupRepo(absRepo, true); err != nil {
				return spec, err
			}
		} else {
			fmt.Fprintln(os.Stderr, styleDim.Render("no "+config.FileName+" in "+absRepo+"; using defaults (run llok init to create one)"))
		}
	}
	cfg, err := config.Load(absRepo)
	if err != nil {
		return spec, err
	}
	if cpus == 0 {
		cpus = cfg.VM.CPUs
	}
	if memMB == 0 {
		memMB = cfg.VM.MemoryMB
	}
	if err := verifyImage(); err != nil {
		return spec, err
	}
	if err := ensureTrusted(cfg, absRepo); err != nil {
		return spec, err
	}
	if cfg.Network.Mode == "none" && cfg.Agent.Primary() != "shell" && cfg.Agent.Primary() != "" {
		fmt.Fprintln(os.Stderr, warn("network.mode is none: the agent cannot reach its provider and will fail to sign in."))
		fmt.Fprintln(os.Stderr, styleDim.Render("  set  network: mode: broker  in "+config.FileName+" (or run: llok init --force), then: llok auth status"))
	}
	// Which agent this session is for. --agent overrides llok.yaml for
	// this run only.
	if agentFl != "" {
		cfg.Agent = config.AgentList{agentFl}
	}
	agent, err := cfg.ResolveAgent()
	if err != nil {
		return spec, err
	}
	if agent.Name == "shell" && command == "" && tty && len(cfg.Agent) == 0 {
		if names, err := agentPicker(nil); err == nil && len(names) > 0 {
			agent, _ = agents.Resolve(names[0], cfg.Agents)
		}
	}
	// Secondary agents only need a warning when their provider has no
	// credential; the default agent's provider is required.
	if others, err := cfg.ResolveAgents(); err == nil {
		for _, o := range others[min(1, len(others)):] {
			if len(o.Providers) > 0 {
				if _, err := creds.Get(o.Providers[0]); errors.Is(err, creds.ErrNotFound) {
					fmt.Fprintln(os.Stderr, warn(o.Name+" is listed but "+o.Providers[0]+" is not signed in; run: llok auth "+o.Providers[0]))
				}
			}
		}
	}
	return runSpec{cfg: cfg, absRepo: absRepo, agent: agent, cpus: cpus, memMB: memMB, command: command}, nil
}

// verifyImage checks the guest image against its manifest. Quick check
// here (kernel and initramfs hashed, root disk size); llok doctor hashes
// everything.
func verifyImage() error {
	if os.Getenv("LLOK_SKIP_VERIFY") == "1" {
		return nil
	}
	m, err := guest.LoadManifest()
	if err != nil {
		return errors.New("the guest image has no manifest; rebuild it with: llok guest build")
	}
	if err := m.Verify(true); err != nil {
		return fmt.Errorf("guest image does not match its manifest (%v); rebuild with: llok guest build", err)
	}
	return nil
}
