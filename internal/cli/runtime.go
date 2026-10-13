package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AnasArshad99/llok/internal/agents"
	"github.com/AnasArshad99/llok/internal/broker"
	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/control"
	"github.com/AnasArshad99/llok/internal/creds"
	"github.com/AnasArshad99/llok/internal/events"
	"github.com/AnasArshad99/llok/internal/guard"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/session"
	"github.com/AnasArshad99/llok/internal/vm"
	"github.com/AnasArshad99/llok/internal/watch"
)

// runSpec is everything a VM run needs, resolved before any VM work
// starts: the foreground command asks its questions first, then either
// runs in place or hands the spec to a background session process.
type runSpec struct {
	cfg     *config.Config
	absRepo string
	agent   agents.Agent
	cpus    uint
	memMB   uint64
	command string // non-empty: run it in the repo and power off
	name    string // session name (run directory, sockets, ledger)
	nat     bool   // developer builds only
}

// runtime is one prepared run: broker, supervisor, feed, control channel
// and the VM options wired to them. The caller supplies the console side
// (a terminal, or a session server) and calls run.
type runtime struct {
	spec      runSpec
	home      string
	opts      vm.Options
	brk       *broker.Broker
	eng       *guard.Engine
	bus       *events.Bus
	feed      *events.Server
	ctl       *control.Host
	providers []string // providers with credentials in this session
	start     time.Time

	// OnMarker, when set, also receives guest markers (after the feed).
	OnMarker func(string)
	// OnNet, when set, receives each broker decision line for a panel.
	OnNet func(string)

	lastTouch atomic.Int64
	cleanups  []func()
}

// startedOrNow is the run's start time, or now before it starts.
func (rt *runtime) startedOrNow() time.Time {
	if rt.start.IsZero() {
		return time.Now()
	}
	return rt.start
}

func (rt *runtime) touch() { rt.lastTouch.Store(time.Now().UnixNano()) }

// idleFor is how long the run has seen no output, input, file change or
// request.
func (rt *runtime) idleFor() time.Duration {
	return time.Since(time.Unix(0, rt.lastTouch.Load()))
}

func (rt *runtime) close() {
	for i := len(rt.cleanups) - 1; i >= 0; i-- {
		rt.cleanups[i]()
	}
	rt.cleanups = nil
}

// newRuntime prepares a run. It never prompts: anything interactive (first
// run setup, trust, the agent picker) happens before, in the foreground.
func newRuntime(spec runSpec) (*runtime, error) {
	home, err := guest.Home()
	if err != nil {
		return nil, err
	}
	rt := &runtime{spec: spec, home: home}
	rt.touch()
	ok := false
	defer func() {
		if !ok {
			rt.close()
		}
	}()
	cfg, agent := spec.cfg, spec.agent

	kernel, initrd, err := guest.Paths()
	if err != nil {
		return nil, err
	}

	// A per-session read-only share carries llok's files for the guest:
	// the exclude list, and in broker mode the CA and the agent env.
	os.MkdirAll(filepath.Join(home, "share"), 0o755)
	shareDir, err := os.MkdirTemp(filepath.Join(home, "share"), "session-")
	if err != nil {
		return nil, err
	}
	rt.cleanups = append(rt.cleanups, func() { os.RemoveAll(shareDir) })
	if err := os.WriteFile(filepath.Join(shareDir, "exclude.txt"), []byte(strings.Join(cfg.Mount.Exclude, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}

	// The broker: only way out of the VM. Needs credentials for the
	// agent's providers (at least the primary one).
	if cfg.Network.Mode == "broker" {
		var lookupErr error
		sess, err := agents.Prepare(agent, func(p string) (agents.Credential, bool) {
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
			return nil, lookupErr
		}
		if err != nil {
			return nil, fmt.Errorf("%w  (or set network.mode: none)", err)
		}
		var routes []broker.Route
		for _, p := range sess.Providers {
			c, ok := sess.Creds[p.Name]
			if !ok {
				continue
			}
			rt.providers = append(rt.providers, p.Name)
			r := broker.Route{Name: p.Name, Hosts: p.Hosts, Cred: broker.Credential{Kind: c.Kind, Secret: c.Secret}}
			if (c.Kind == "oauth" || c.Kind == "chatgpt") && p.TokenHeader != "" {
				r.Header, r.Format = p.TokenHeader, p.TokenFormat
			} else {
				r.Header, r.Format = p.KeyHeader, p.KeyFormat
			}
			r.Upstream, _ = url.Parse(p.Upstream)
			routes = append(routes, r)
		}
		os.MkdirAll(filepath.Join(home, "audit"), 0o700)
		rt.brk, err = broker.New(cfg.Name, broker.Policy{Allow: cfg.Network.Allow}, routes,
			filepath.Join(home, "audit", "broker.jsonl"))
		if err != nil {
			return nil, err
		}
		rt.cleanups = append(rt.cleanups, func() { rt.brk.Close() })
		// The guest trusts llok's CA so TLS to provider hosts can be
		// terminated here for credential injection.
		ca, err := broker.LoadOrCreateCA(filepath.Join(home, "ca"))
		if err != nil {
			return nil, fmt.Errorf("broker CA: %w", err)
		}
		rt.brk.CA = ca
		for name, body := range map[string][]byte{
			"ca.pem": ca.PEM, "env.sh": []byte(sess.EnvScript()), "setup.sh": []byte(sess.SetupScript()),
		} {
			if err := os.WriteFile(filepath.Join(shareDir, name), body, 0o644); err != nil {
				return nil, err
			}
		}
	}

	rt.opts = vm.Options{
		Kernel:      kernel,
		Initrd:      initrd,
		Repo:        spec.absRepo,
		RepoPath:    cfg.Mount.Repo,
		Name:        cfg.Name,
		CPUs:        spec.cpus,
		MemoryMB:    spec.memMB,
		Command:     spec.command,
		Interactive: spec.command == "",
		NAT:         spec.nat,
		AgentBinary: agent.Binary,
		ShareDir:    shareDir,
	}
	if rootfs, ok := guest.RootfsPath(); ok {
		rt.opts.Disk = rootfs
		rt.opts.DiskReadOnly = true
		rt.opts.RootDisk = true
	}

	// Supervisor: host-side rules over repo file changes and broker
	// decisions, with a session ledger. Metadata only.
	ledger, err := guard.OpenLedger(filepath.Join(home, "sessions"), cfg.Name)
	if err != nil {
		return nil, err
	}
	rt.cleanups = append(rt.cleanups, func() { ledger.Close() })
	rt.eng = guard.New(cfg.GuardRules())
	rt.eng.Ledger = ledger
	var machine vm.Machine
	rt.eng.Machine = func() guard.Machine { return machine }
	rt.opts.OnStart = func(m vm.Machine) { machine = m }

	// The live feed: every ledger line and the session's lifecycle, on a
	// local socket other programs can follow and act on.
	rt.bus = events.NewBus(cfg.Name)
	rt.eng.Report = func(ev guard.Event, rule, action string) { rt.bus.Emit(feedEvent(ev, rule, action)) }
	os.MkdirAll(session.Dir(home, spec.name), 0o700)
	rt.feed, err = events.Serve(session.EventsSocket(home, spec.name), rt.bus, socketControl{rt.eng})
	if err != nil {
		return nil, err
	}
	rt.cleanups = append(rt.cleanups, func() { rt.feed.Close() })
	rt.bus.Emit(events.Event{Type: events.TypeSession, Phase: "start", Agent: agent.Name,
		Repo: spec.absRepo, VMRepo: cfg.Mount.Repo, PID: os.Getpid()})

	stopWatch, err := watch.Start(spec.absRepo, func(p, op string) {
		rt.touch()
		rt.eng.Observe(guard.Event{Kind: guard.KindFile, Path: p, Op: op})
	})
	if err != nil {
		return nil, fmt.Errorf("watch repo: %w", err)
	}
	rt.cleanups = append(rt.cleanups, stopWatch)
	if rt.brk != nil {
		rt.brk.OnEvent = func(e broker.Event) {
			rt.touch()
			if rt.OnNet != nil {
				rt.OnNet(e.Line())
			}
			rt.eng.Observe(guard.Event{Kind: guard.KindNet, Host: e.Host, Decision: e.Decision})
		}
	}
	rt.opts.OnMarker = func(m string) {
		if strings.HasPrefix(m, "LLOK_READY") {
			rt.bus.Emit(events.Event{Type: events.TypeSession, Phase: "ready"})
		}
		if rt.OnMarker != nil {
			rt.OnMarker(m)
		}
	}

	// Control channel: resize and shutdown, plus the trusted exit code.
	rt.ctl = control.New()
	var exitMu sync.Mutex
	exitCode, exitSeen := 0, false
	rt.ctl.OnReply = func(r control.Reply) {
		if r.Op == "exit" {
			exitMu.Lock()
			exitCode, exitSeen = r.Code, true
			exitMu.Unlock()
		}
	}
	rt.opts.ExitCode = func() (int, bool) {
		exitMu.Lock()
		defer exitMu.Unlock()
		return exitCode, exitSeen
	}
	rt.opts.Vsock = map[uint32]func(net.Listener){control.Port: rt.ctl.Serve}
	rt.opts.GracefulStop = func() bool { return rt.ctl.Shutdown() == nil }
	if rt.brk != nil {
		rt.opts.Broker = true
		rt.opts.Vsock[3128] = func(l net.Listener) { rt.brk.Serve(l) }
	}
	ok = true
	return rt, nil
}

// run boots the VM and waits for it to stop, closing the feed with the
// session's end event.
func (rt *runtime) run(ctx context.Context) (vm.Result, error) {
	rt.start = time.Now()
	res, err := vm.Run(ctx, rt.opts)
	end := events.Event{Type: events.TypeSession, Phase: "end", DurationMS: res.Duration.Milliseconds(), StoppedBy: rt.eng.StoppedBy()}
	if err == nil {
		code := res.ExitCode
		end.Exit = &code
	} else {
		end.Reason = err.Error()
	}
	rt.bus.Emit(end)
	return res, err
}

// socketControl lets a program on the events socket pause, resume or kill
// the session through the supervisor, so the panel and ledger see it too.
type socketControl struct{ eng *guard.Engine }

func (c socketControl) Pause() error  { return c.eng.Pause("socket") }
func (c socketControl) Resume() error { return c.eng.Resume("socket") }
func (c socketControl) Kill() error   { return c.eng.Kill("socket") }

// feedEvent maps a ledger line to a feed event: a raw observation, a rule
// outcome, or a VM state change.
func feedEvent(ev guard.Event, rule, action string) events.Event {
	out := events.Event{Time: ev.Time, Path: ev.Path, Op: ev.Op, Host: ev.Host, Decision: ev.Decision}
	switch {
	case ev.Kind == guard.KindControl:
		out.Type, out.State, out.Source = events.TypeVM, action, ev.Host
		out.Host = ""
	case rule != "" && action != "paused" && action != "stopped":
		out.Type, out.Rule, out.Action = events.TypeRule, rule, action
	case action != "":
		// paused/stopped (by a rule), resumed/killed (by the user at the prompt)
		out.Type, out.State, out.Reason = events.TypeVM, action, rule
		if rule == "" {
			out.Source = "you"
		} else {
			out.Source = "rule"
		}
	case ev.Kind == guard.KindFile:
		out.Type = events.TypeFile
	default:
		out.Type = events.TypeNet
	}
	return out
}

// summarize prints how a run ended and returns the process exit code.
func summarize(res vm.Result, err error, stoppedBy string, verbose bool) int {
	if stoppedBy != "" {
		fmt.Fprintln(os.Stderr, styleErr.Render("llok")+" · vm stopped by the supervisor: "+stoppedBy)
		return 125
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, bad(err.Error()))
		return 1
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
	default:
		if verbose {
			fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · vm ran "+dur.String()+" · powered off")
		}
	}
	return res.ExitCode
}
