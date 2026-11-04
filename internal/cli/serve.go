package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/events"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/session"
	"github.com/AnasArshad99/llok/internal/ui"
)

// startServer launches the background process that owns the VM for a
// detached session and waits until it accepts attachments.
func startServer(home string, spec runSpec, cmd *cobra.Command) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cols, rows := ui.TermSize()
	if cols >= 90 {
		cols -= 33 // the panel and its separator
	}
	args := []string{"_serve", "--session", spec.name, "--repo", spec.absRepo, "--agent", spec.agent.Name,
		"--cpus", fmt.Sprint(spec.cpus), "--mem", fmt.Sprint(spec.memMB),
		"--cols", fmt.Sprint(cols), "--rows", fmt.Sprint(rows)}
	if spec.nat {
		args = append(args, "--dev-nat")
	}
	if spec.readOnly {
		args = append(args, "--ro")
	}
	if err := os.MkdirAll(session.Dir(home, spec.name), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(session.LogPath(home, spec.name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	c := exec.Command(exe, args...)
	c.Stdout, c.Stderr = logf, logf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	// The server is on its own now; reap it from a goroutine so it never
	// turns into a zombie if this client outlives it.
	exited := make(chan error, 1)
	go func() { exited <- c.Wait() }()

	sock := session.ConsoleSocket(home, spec.name)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return fmt.Errorf("session failed to start:\n%s", tailFile(session.LogPath(home, spec.name), 20))
		default:
		}
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("session did not start in time; see " + session.LogPath(home, spec.name))
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := splitLines(string(b))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return joinLines(lines)
}

// newServeCmd is the hidden background half of a detached session.
func newServeCmd() *cobra.Command {
	var (
		name, repo, agentFl string
		cpus                uint
		memMB               uint64
		cols, rows          int
		ro                  bool
	)
	cmd := &cobra.Command{
		Use:    "_serve",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := guest.Home()
			if err != nil {
				return err
			}
			cfg, err := config.Load(repo)
			if err != nil {
				return err
			}
			if agentFl != "" {
				cfg.Agent = config.AgentList{agentFl}
			}
			agent, err := cfg.ResolveAgent()
			if err != nil {
				return err
			}
			// Approved in the foreground already; refuse rather than ask here.
			if err := ensureTrusted(cfg, repo); err != nil {
				return err
			}
			if cpus == 0 {
				cpus = cfg.VM.CPUs
			}
			if memMB == 0 {
				memMB = cfg.VM.MemoryMB
			}
			spec := runSpec{cfg: cfg, absRepo: repo, agent: agent, cpus: cpus, memMB: memMB, name: name,
				nat: devNATEnabled(cmd), readOnly: cfg.Mount.ReadOnly || ro}
			return serveSession(home, spec, cols, rows)
		},
	}
	cmd.Flags().StringVar(&name, "session", "", "")
	cmd.Flags().StringVar(&repo, "repo", "", "")
	cmd.Flags().StringVar(&agentFl, "agent", "", "")
	cmd.Flags().UintVar(&cpus, "cpus", 0, "")
	cmd.Flags().Uint64Var(&memMB, "mem", 0, "")
	cmd.Flags().IntVar(&cols, "cols", 0, "")
	cmd.Flags().IntVar(&rows, "rows", 0, "")
	cmd.Flags().BoolVar(&ro, "ro", false, "")
	registerDevFlags(cmd)
	return cmd
}

// serveSession runs the VM with a session server as its console.
func serveSession(home string, spec runSpec, cols, rows int) error {
	rt, err := newRuntime(spec)
	if err != nil {
		return err
	}
	defer rt.close()
	started := time.Now()
	if err := session.WriteRecord(home, session.Record{Name: spec.name, Project: spec.cfg.Name, Repo: spec.absRepo,
		Agent: spec.agent.Name, PID: os.Getpid(), Started: started, Attach: true, CPUs: spec.cpus, MemoryMB: spec.memMB, ReadOnly: spec.readOnly}); err != nil {
		return err
	}
	defer session.RemoveRecord(home, spec.name)

	srv := session.New(spec.name, ui.Info{
		Agent:     spec.agent.Name,
		Providers: rt.providers,
		Network:   spec.cfg.Network.Mode,
		Allow:     len(spec.cfg.Network.Allow),
		Project:   spec.cfg.Name,
		RepoHost:  spec.absRepo,
		RepoVM:    spec.cfg.Mount.Repo,
		CPUs:      spec.cpus,
		MemoryMB:  spec.memMB,
		Kernel:    guest.KernelVersion(),
		Session:   spec.name,
		Started:   started,
		ReadOnly:  spec.readOnly,
	})
	powerOff := make(chan struct{}, 1)
	requestPowerOff := func() {
		select {
		case powerOff <- struct{}{}:
		default:
		}
	}
	rt.opts.Output = srv
	rt.opts.Input = srv.Input()
	rt.opts.Cols, rt.opts.Rows = cols, rows
	rt.opts.NoEscape = true
	rt.opts.HideBanner = true
	rt.opts.PowerOff = powerOff
	rt.OnMarker = srv.Marker
	rt.OnNet = srv.Event
	rt.eng.Notify = srv.Event
	rt.eng.Ask = func(q string, opts []guardOption) byte {
		srv.SetPaused(true)
		k := srv.Ask(q, opts)
		srv.SetPaused(false)
		return k
	}
	srv.OnResize = func(c, r int) { rt.ctl.Resize(c, r) }
	srv.Others = func() int { return max(0, len(session.List(home))-1) }
	srv.Touch = rt.touch
	srv.OnOp = func(op string) error {
		switch op {
		case "poweroff":
			srv.Event("powering off on request")
			requestPowerOff()
			return nil
		case "kill":
			return rt.eng.Kill("you")
		case "pause":
			return rt.eng.Pause("you")
		case "resume":
			return rt.eng.Resume("you")
		}
		return errors.New("unknown op " + op)
	}
	if err := srv.Listen(session.ConsoleSocket(home, spec.name)); err != nil {
		return err
	}
	srv.Event("vm starting")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer cancel()

	// Idle sessions power themselves off so forgotten ones do not sit on
	// memory forever.
	var idleStop string
	if idle, _ := spec.cfg.IdleTimeout(); idle > 0 {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if rt.idleFor() >= idle && srv.State() == "running" {
						idleStop = "idle for " + idle.String()
						srv.Event("✗ " + idleStop + "; powering off")
						rt.bus.Emit(events.Event{Type: events.TypeVM, State: "stopped", Source: "idle", Reason: idleStop})
						requestPowerOff()
						return
					}
				}
			}
		}()
	}

	res, err := rt.run(ctx)
	x := session.Exit{Code: res.ExitCode, Forced: res.Forced, StoppedBy: rt.eng.StoppedBy(), DurationMS: res.Duration.Milliseconds()}
	if x.StoppedBy == "" {
		x.StoppedBy = idleStop
	}
	if err != nil {
		x.Err = err.Error()
	}
	srv.Close(x)
	return nil
}
