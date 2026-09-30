// Package control is the host side of the guest control channel: a vsock
// port the guest agent connects to, carrying small JSON commands such as
// terminal resize and graceful shutdown. It is separate from the broker port
// so the two never share a connection.
package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// Port is the vsock port the guest agent dials.
const Port = 3129

// Command is one message host -> guest.
type Command struct {
	Op   string `json:"op"`             // resize | shutdown | ping
	Cols int    `json:"cols,omitempty"` // resize
	Rows int    `json:"rows,omitempty"` // resize
}

// Reply is one message guest -> host.
type Reply struct {
	Op  string `json:"op"`
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

// Host owns the current guest connection (the latest one wins).
type Host struct {
	mu      sync.Mutex
	conn    net.Conn
	enc     *json.Encoder
	pending Command // last resize, replayed when the guest (re)connects
	ready   chan struct{}
	once    sync.Once
	OnReply func(Reply)
}

// New returns an unconnected Host.
func New() *Host { return &Host{ready: make(chan struct{})} }

// Serve accepts guest connections until the listener closes.
func (h *Host) Serve(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		h.attach(c)
	}
}

func (h *Host) attach(c net.Conn) {
	h.mu.Lock()
	if h.conn != nil {
		h.conn.Close()
	}
	h.conn = c
	h.enc = json.NewEncoder(c)
	replay := h.pending
	h.mu.Unlock()
	h.once.Do(func() { close(h.ready) })
	if replay.Op == "resize" {
		h.send(replay)
	}
	go h.readReplies(c)
}

func (h *Host) readReplies(c net.Conn) {
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		var r Reply
		if json.Unmarshal(sc.Bytes(), &r) == nil && h.OnReply != nil {
			h.OnReply(r)
		}
	}
	h.mu.Lock()
	if h.conn == c {
		h.conn, h.enc = nil, nil
	}
	h.mu.Unlock()
}

// Connected reports whether a guest agent is attached.
func (h *Host) Connected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn != nil
}

// WaitConnected blocks until a guest attaches or the timeout passes.
func (h *Host) WaitConnected(d time.Duration) bool {
	select {
	case <-h.ready:
		return true
	case <-time.After(d):
		return false
	}
}

var errNotConnected = errors.New("guest control agent not connected")

func (h *Host) send(c Command) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.enc == nil {
		return errNotConnected
	}
	h.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return h.enc.Encode(c)
}

// Resize tells the guest its terminal is now cols x rows. Remembered and
// replayed if the guest connects later.
func (h *Host) Resize(cols, rows int) error {
	c := Command{Op: "resize", Cols: cols, Rows: rows}
	h.mu.Lock()
	h.pending = c
	h.mu.Unlock()
	return h.send(c)
}

// Shutdown asks the guest to sync and power off.
func (h *Host) Shutdown() error { return h.send(Command{Op: "shutdown"}) }
