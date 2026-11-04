package session

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AnasArshad99/llok/internal/guard"
	"github.com/AnasArshad99/llok/internal/ui"
)

const (
	scrollbackBytes = 512 << 10 // console history replayed to a new client
	activityMax     = 300
	markersMax      = 64
)

// Server stands in for the terminal on the VM side: it is the VM's output
// writer and input reader, receives markers, activity and prompts exactly
// as the full-screen UI does, and relays all of it to attached clients.
type Server struct {
	Name string
	Info ui.Info

	// OnResize is told the attached client's console size.
	OnResize func(cols, rows int)
	// OnOp receives client requests: poweroff, kill, pause, resume.
	OnOp func(op string) error
	// Others reports how many other sessions are running, for the panel.
	Others func() int
	// Touch is called on any activity (output, input), for idle tracking.
	Touch func()

	mu       sync.Mutex
	out      []byte // ring of recent console output
	markers  []string
	activity []Activity
	state    string
	started  time.Time
	prompt   *pending
	clients  map[*client]struct{}
	size     Size

	inR *io.PipeReader
	inW *io.PipeWriter
	ln  net.Listener
	wg  sync.WaitGroup
}

type pending struct {
	Prompt
	answer chan byte
}

type client struct {
	conn net.Conn
	wmu  sync.Mutex
}

func (c *client) send(t byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return writeFrame(c.conn, t, payload)
}

// New creates a server for the named session.
func New(name string, info ui.Info) *Server {
	r, w := io.Pipe()
	return &Server{
		Name:    name,
		Info:    info,
		state:   "booting",
		started: time.Now(),
		clients: map[*client]struct{}{},
		inR:     r,
		inW:     w,
	}
}

// Input is the guest's console input: whatever attached clients type.
func (s *Server) Input() io.Reader { return s.inR }

// Listen serves attachments on a Unix socket at path (owner-only).
func (s *Server) Listen(path string) error {
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	s.ln = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go s.serve(c)
		}
	}()
	return nil
}

// Close ends the session for clients: sends the exit frame, drops them,
// removes the socket.
func (s *Server) Close(x Exit) {
	s.mu.Lock()
	s.state = "stopped"
	cs := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	s.mu.Unlock()
	b, _ := json.Marshal(x)
	for _, c := range cs {
		c.send(frameExit, b)
		c.conn.Close()
	}
	if s.ln != nil {
		s.ln.Close()
		os.Remove(s.ln.Addr().String())
	}
	s.inW.Close()
	s.wg.Wait()
}

// Attached reports whether any client is connected.
func (s *Server) Attached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients) > 0
}

// State is the VM state shown to clients.
func (s *Server) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Write is the VM console output.
func (s *Server) Write(p []byte) (int, error) {
	if s.Touch != nil {
		s.Touch()
	}
	s.mu.Lock()
	s.out = append(s.out, p...)
	if len(s.out) > scrollbackBytes {
		// Drop whole lines from the front so a replay never starts inside
		// an escape sequence more often than it has to.
		cut := len(s.out) - scrollbackBytes
		if i := bytes.IndexByte(s.out[cut:], '\n'); i >= 0 {
			cut += i + 1
		}
		s.out = append([]byte(nil), s.out[cut:]...)
	}
	cs := s.clientList()
	s.mu.Unlock()
	for _, c := range cs {
		if c.send(frameOutput, p) != nil {
			s.drop(c)
		}
	}
	return len(p), nil
}

func (s *Server) clientList() []*client {
	cs := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		cs = append(cs, c)
	}
	return cs
}

func (s *Server) broadcast(t byte, payload []byte) {
	s.mu.Lock()
	cs := s.clientList()
	s.mu.Unlock()
	for _, c := range cs {
		if c.send(t, payload) != nil {
			s.drop(c)
		}
	}
}

func (s *Server) drop(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	c.conn.Close()
}

// Marker records a guest marker and relays it.
func (s *Server) Marker(m string) {
	s.mu.Lock()
	s.markers = append(s.markers, m)
	if len(s.markers) > markersMax {
		s.markers = s.markers[len(s.markers)-markersMax:]
	}
	switch {
	case len(m) >= 10 && m[:10] == "LLOK_READY":
		s.state = "running"
	case len(m) >= 9 && m[:9] == "LLOK_EXIT":
		s.state = "stopped"
	}
	s.mu.Unlock()
	s.broadcast(frameMarker, []byte(m))
}

// Event appends a panel activity line and relays it.
func (s *Server) Event(line string) {
	a := Activity{At: time.Now(), Text: line}
	s.mu.Lock()
	s.activity = append(s.activity, a)
	if len(s.activity) > activityMax {
		s.activity = s.activity[len(s.activity)-activityMax:]
	}
	s.mu.Unlock()
	b, _ := json.Marshal(a)
	s.broadcast(frameActivity, b)
}

// SetPaused mirrors the supervisor's pause state.
func (s *Server) SetPaused(v bool) {
	s.mu.Lock()
	if v {
		s.state = "paused"
	} else if s.state == "paused" {
		s.state = "running"
	}
	s.mu.Unlock()
	if v {
		s.broadcast(framePaused, []byte("1"))
	} else {
		s.broadcast(framePaused, []byte("0"))
	}
}

// Ask shows a question to whoever is attached and blocks until one of
// them answers. With nobody attached the VM stays paused and the question
// waits; the next client to attach sees it.
func (s *Server) Ask(question string, opts []guard.Option) byte {
	p := &pending{answer: make(chan byte, 1)}
	p.Question = question
	for _, o := range opts {
		p.Options = append(p.Options, ui.Option{Key: o.Key, Label: o.Label})
	}
	s.mu.Lock()
	s.prompt = p
	s.mu.Unlock()
	b, _ := json.Marshal(p.Prompt)
	s.broadcast(framePrompt, b)
	k := <-p.answer
	s.mu.Lock()
	s.prompt = nil
	s.mu.Unlock()
	s.broadcast(framePrompt, nil)
	return k
}

func (s *Server) serve(conn net.Conn) {
	defer s.wg.Done()
	c := &client{conn: conn}
	defer conn.Close()
	// The client speaks first: its console size.
	t, payload, err := readFrame(conn)
	if err != nil || t != frameClient {
		return
	}
	var size Size
	json.Unmarshal(payload, &size)

	s.mu.Lock()
	h := Hello{Name: s.Name, Info: s.Info, State: s.state, Started: s.started,
		Markers: append([]string(nil), s.markers...), Activity: append([]Activity(nil), s.activity...)}
	if s.prompt != nil {
		pr := s.prompt.Prompt
		h.Prompt = &pr
	}
	replay := append([]byte(nil), s.out...)
	s.clients[c] = struct{}{}
	s.size = size
	s.mu.Unlock()
	if s.Others != nil {
		h.Others = s.Others()
	}
	if hb, err := json.Marshal(h); err != nil || c.send(frameHello, hb) != nil {
		s.drop(c)
		return
	}
	if len(replay) > 0 && c.send(frameOutput, replay) != nil {
		s.drop(c)
		return
	}
	if s.OnResize != nil && size.Cols > 0 {
		s.OnResize(size.Cols, size.Rows)
	}

	for {
		t, payload, err := readFrame(conn)
		if err != nil {
			s.drop(c)
			return
		}
		switch t {
		case frameInput:
			if s.Touch != nil {
				s.Touch()
			}
			s.inW.Write(payload)
		case frameResize:
			var sz Size
			if json.Unmarshal(payload, &sz) == nil && sz.Cols > 0 && s.OnResize != nil {
				s.mu.Lock()
				s.size = sz
				s.mu.Unlock()
				s.OnResize(sz.Cols, sz.Rows)
			}
		case frameAnswer:
			if len(payload) == 1 {
				s.mu.Lock()
				p := s.prompt
				s.mu.Unlock()
				if p != nil {
					select {
					case p.answer <- payload[0]:
					default:
					}
				}
			}
		case frameOp:
			if s.OnOp != nil {
				if err := s.OnOp(string(payload)); err != nil {
					s.Event("! " + string(payload) + ": " + err.Error())
				}
			}
		}
	}
}

// Record describes a session on disk, for listing and attaching.
type Record struct {
	Name     string    `json:"name"`
	Project  string    `json:"project"`
	Repo     string    `json:"repo"`
	Agent    string    `json:"agent"`
	PID      int       `json:"pid"`
	Started  time.Time `json:"started"`
	Attach   bool      `json:"attach"` // false for a foreground (plain or -c) run
	ReadOnly bool      `json:"read_only,omitempty"`
	CPUs     uint      `json:"cpus"`
	MemoryMB uint64    `json:"memory_mb"`
}

// Dir is the per-session run directory.
func Dir(home, name string) string { return filepath.Join(home, "run", name) }

// ConsoleSocket, EventsSocket and RecordPath name the files inside Dir.
func ConsoleSocket(home, name string) string { return filepath.Join(Dir(home, name), "console.sock") }
func EventsSocket(home, name string) string  { return filepath.Join(Dir(home, name), "events.sock") }
func RecordPath(home, name string) string    { return filepath.Join(Dir(home, name), "session.json") }
func LogPath(home, name string) string       { return filepath.Join(Dir(home, name), "server.log") }

// WriteRecord creates the run directory and its record.
func WriteRecord(home string, r Record) error {
	if err := os.MkdirAll(Dir(home, r.Name), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(RecordPath(home, r.Name), b, 0o600)
}

// RemoveRecord retires a session: everything in its run directory goes
// except server.log, which is what to read when a session ended badly.
func RemoveRecord(home, name string) {
	d := Dir(home, name)
	entries, _ := os.ReadDir(d)
	for _, e := range entries {
		if e.Name() != "server.log" {
			os.RemoveAll(filepath.Join(d, e.Name()))
		}
	}
	if rest, _ := os.ReadDir(d); len(rest) == 0 {
		os.Remove(d)
	}
}

// List returns the sessions whose process is alive, removing records of
// dead ones.
func List(home string) []Record {
	dirs, _ := filepath.Glob(filepath.Join(home, "run", "*", "session.json"))
	var out []Record
	for _, p := range dirs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil || r.Name == "" {
			continue
		}
		if !alive(r.PID) {
			RemoveRecord(home, r.Name)
			continue
		}
		out = append(out, r)
	}
	return out
}

// Find returns the live record for name.
func Find(home, name string) (Record, bool) {
	for _, r := range List(home) {
		if r.Name == name {
			return r, true
		}
	}
	return Record{}, false
}

// ForRepo returns the live sessions mounted from repo.
func ForRepo(home, repo string) []Record {
	var out []Record
	for _, r := range List(home) {
		if r.Repo == repo {
			out = append(out, r)
		}
	}
	return out
}

// FreeName picks project, then project-2, project-3, ... whichever is unused.
func FreeName(home, project string) string {
	used := map[string]bool{}
	for _, r := range List(home) {
		used[r.Name] = true
	}
	if !used[project] {
		return project
	}
	for i := 2; ; i++ {
		n := project + "-" + itoa(i)
		if !used[n] {
			return n
		}
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(sigZero) == nil
}
