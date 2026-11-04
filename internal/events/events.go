// Package events is the session's live feed: everything llok observes or
// decides, one JSON line each, over a local Unix socket that other programs
// on this Mac can read. The same socket accepts a few control lines (pause,
// resume, kill), so a supervisor outside llok can act on what it sees.
//
// The feed is metadata only: paths, hosts, decisions, states. Never file
// contents, request bodies or credentials.
package events

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Type names the kind of event.
type Type string

const (
	TypeSession Type = "session" // phase: start | ready | end
	TypeFile    Type = "file"    // a repo path changed (host-side watcher)
	TypeNet     Type = "net"     // the broker decided on a destination
	TypeRule    Type = "rule"    // a supervisor rule fired
	TypeVM      Type = "vm"      // state: paused | resumed | stopped
	TypeControl Type = "control" // a control line was received over the socket
)

// Event is one line on the feed.
type Event struct {
	Seq     uint64    `json:"seq"`
	Time    time.Time `json:"time"`
	Type    Type      `json:"type"`
	Project string    `json:"project,omitempty"`

	// session
	Phase      string `json:"phase,omitempty"` // start | ready | end
	Agent      string `json:"agent,omitempty"`
	Repo       string `json:"repo,omitempty"`    // host path
	VMRepo     string `json:"vm_repo,omitempty"` // path inside the VM
	ReadOnly   bool   `json:"read_only,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Exit       *int   `json:"exit,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	StoppedBy  string `json:"stopped_by,omitempty"`

	// file / net (also carried on rule events, naming what matched)
	Path     string `json:"path,omitempty"`
	Op       string `json:"op,omitempty"`
	Host     string `json:"host,omitempty"`
	Decision string `json:"decision,omitempty"`
	Status   int    `json:"status,omitempty"`

	// rule
	Rule   string `json:"rule,omitempty"`
	Action string `json:"action,omitempty"`

	// vm / control
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
	Source string `json:"source,omitempty"` // who caused a vm state change: rule | you | socket
	Op2    string `json:"command,omitempty"`
}

// Command is a control line a client may send: {"op":"pause"|"resume"|"kill"}.
type Command struct {
	Op string `json:"op"`
}

// Controller receives control lines. Each returns an error to report back.
type Controller interface {
	Pause() error
	Resume() error
	Kill() error
}

// Bus fans events out to subscribers and keeps a bounded replay buffer so a
// late client sees how the session began.
type Bus struct {
	mu      sync.Mutex
	seq     uint64
	project string
	subs    map[*subscriber]struct{}
	replay  []Event
	closed  bool
}

const (
	replayMax = 256  // events a late subscriber receives on connect
	subBuffer = 1024 // events a slow subscriber may lag before it is dropped
)

type subscriber struct {
	ch chan Event
}

// NewBus creates a bus; project is stamped on every event.
func NewBus(project string) *Bus {
	return &Bus{project: project, subs: map[*subscriber]struct{}{}}
}

// Emit stamps and delivers ev. Slow subscribers are disconnected rather
// than allowed to stall the session.
func (b *Bus) Emit(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.seq++
	ev.Seq = b.seq
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	ev.Project = b.project
	b.replay = append(b.replay, ev)
	if len(b.replay) > replayMax {
		b.replay = b.replay[len(b.replay)-replayMax:]
	}
	for s := range b.subs {
		select {
		case s.ch <- ev:
		default:
			delete(b.subs, s)
			close(s.ch)
		}
	}
}

// Subscribe returns a channel that first replays recent history, then
// streams live events. The channel closes when the bus closes or the
// subscriber falls too far behind.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &subscriber{ch: make(chan Event, subBuffer+replayMax)}
	for _, ev := range b.replay {
		s.ch <- ev
	}
	if b.closed {
		close(s.ch)
		return s.ch, func() {}
	}
	b.subs[s] = struct{}{}
	return s.ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[s]; ok {
			delete(b.subs, s)
			close(s.ch)
		}
		b.mu.Unlock()
	}
}

// Close ends every subscription.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for s := range b.subs {
		delete(b.subs, s)
		close(s.ch)
	}
}

// Server serves a bus over a Unix socket.
type Server struct {
	Path string
	bus  *Bus
	ctl  Controller
	ln   net.Listener
	wg   sync.WaitGroup
}

// SocketPath is where a project's live session listens.
func SocketPath(home, project string) string {
	return filepath.Join(home, "run", project+".sock")
}

// Serve listens on path (owner-only), streaming bus to each client and
// routing control lines to ctl (nil refuses them).
func Serve(path string, bus *Bus, ctl Controller) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// A stale socket from a crashed session is only ever ours to remove;
	// a live one means another session for this project is running.
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, errors.New("another llok session for this project is running (" + path + ")")
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	s := &Server{Path: path, bus: bus, ctl: ctl, ln: ln}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.serve(c)
	}
}

func (s *Server) serve(c net.Conn) {
	defer s.wg.Done()
	defer c.Close()
	ch, cancel := s.bus.Subscribe()
	defer cancel()
	done := make(chan struct{})
	// Reader: control lines until the client hangs up.
	go func() {
		defer close(done)
		sc := bufio.NewScanner(c)
		sc.Buffer(make([]byte, 4096), 4096)
		for sc.Scan() {
			var cmd Command
			if json.Unmarshal(sc.Bytes(), &cmd) != nil || cmd.Op == "" {
				continue
			}
			s.control(cmd.Op)
		}
	}()
	enc := json.NewEncoder(c)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := enc.Encode(ev); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

func (s *Server) control(op string) {
	ack := func(err error) {
		ev := Event{Type: TypeControl, Op2: op}
		if err != nil {
			ev.Reason = err.Error()
		}
		s.bus.Emit(ev)
	}
	switch {
	case s.ctl == nil:
		ack(errors.New("no controller"))
	case op == "pause":
		ack(s.ctl.Pause())
	case op == "resume":
		ack(s.ctl.Resume())
	case op == "kill":
		// A kill ends the session, and with it this feed: acknowledge first
		// so the client sees the answer, and report a failure after.
		ack(nil)
		if err := s.ctl.Kill(); err != nil {
			ack(err)
		}
	default:
		ack(errors.New("unknown op"))
	}
}

// Close stops accepting, drops clients and removes the socket file.
func (s *Server) Close() {
	s.ln.Close()
	s.bus.Close()
	s.wg.Wait()
	os.Remove(s.Path)
}

// Dial connects to a project's feed and returns a line reader plus the
// connection for sending control lines.
func Dial(path string) (net.Conn, *bufio.Scanner, error) {
	c, err := net.Dial("unix", path)
	if err != nil {
		return nil, nil, err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<16), 1<<16)
	return c, sc, nil
}
