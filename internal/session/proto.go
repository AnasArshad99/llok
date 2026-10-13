// Package session lets a VM outlive the terminal it was started from. The
// VM runs in a background llok process (the server) that owns the console
// and the supervisor; any terminal can attach to it over a local socket,
// detach, and come back. This is tmux's shape: the server keeps the bytes,
// the client keeps the screen.
package session

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/AnasArshad99/llok/internal/ui"
)

// Frames are [type:1][length:4 big-endian][payload]. Payloads are raw bytes
// for console traffic and JSON for everything else.
const (
	// server -> client
	frameOutput   byte = 'o' // console bytes
	frameHello    byte = 'i' // Hello (JSON), first frame after a client's Hello
	frameActivity byte = 'a' // one panel activity line
	frameMarker   byte = 'm' // one guest marker
	framePrompt   byte = 'p' // Prompt (JSON); empty payload clears it
	framePaused   byte = 'z' // "1" paused, "0" running
	frameExit     byte = 'x' // Exit (JSON); the session is over

	// client -> server
	frameInput  byte = 'I' // keystrokes for the guest (and emulator replies)
	frameResize byte = 'R' // Size (JSON)
	frameAnswer byte = 'A' // one byte answering the current prompt
	frameOp     byte = 'O' // "poweroff" | "kill" | "pause" | "resume"
	frameClient byte = 'H' // Size (JSON): the client's console size, sent first
)

const maxFrame = 1 << 20

// Size is a terminal size.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Hello is the server's snapshot for a freshly attached client.
type Hello struct {
	Name     string     `json:"name"`
	Info     ui.Info    `json:"info"`
	State    string     `json:"state"` // booting | running | paused | stopped
	Started  time.Time  `json:"started"`
	Markers  []string   `json:"markers"`
	Activity []Activity `json:"activity"`
	Prompt   *Prompt    `json:"prompt,omitempty"`
	Others   int        `json:"others"` // other sessions running on this machine
}

// Activity is one panel line with the time it happened.
type Activity struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Prompt is a question the supervisor is waiting on.
type Prompt struct {
	Question string      `json:"question"`
	Options  []ui.Option `json:"options"`
}

// Exit is how the session ended.
type Exit struct {
	Code       int    `json:"code"`
	Err        string `json:"err,omitempty"`
	Forced     bool   `json:"forced"`
	StoppedBy  string `json:"stopped_by,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

func writeFrame(w io.Writer, t byte, payload []byte) error {
	hdr := [5]byte{t}
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

func writeJSON(w io.Writer, t byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(w, t, b)
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, errors.New("frame too large")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}
