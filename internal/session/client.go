package session

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Client is one terminal attached to a session.
type Client struct {
	conn net.Conn
	wmu  sync.Mutex
	// Hello is the server's snapshot, available after Dial.
	Hello Hello
}

// Dial attaches to the console socket at path, announcing the client's
// console size, and reads the server's snapshot.
func Dial(path string, size Size) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn}
	if err := c.sendJSON(frameClient, size); err != nil {
		conn.Close()
		return nil, err
	}
	t, payload, err := readFrame(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if t != frameHello {
		conn.Close()
		return nil, errors.New("unexpected first frame from session")
	}
	if err := json.Unmarshal(payload, &c.Hello); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) send(t byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.conn, t, payload)
}

func (c *Client) sendJSON(t byte, v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeJSON(c.conn, t, v)
}

// Input sends keystrokes to the guest.
func (c *Client) Input(p []byte) error { return c.send(frameInput, p) }

// Resize tells the server the console size changed.
func (c *Client) Resize(cols, rows int) error {
	return c.sendJSON(frameResize, Size{Cols: cols, Rows: rows})
}

// Answer replies to the current supervisor prompt.
func (c *Client) Answer(k byte) error { return c.send(frameAnswer, []byte{k}) }

// Op asks the server for poweroff, kill, pause or resume.
func (c *Client) Op(op string) error { return c.send(frameOp, []byte(op)) }

// Close detaches. The session keeps running.
func (c *Client) Close() error { return c.conn.Close() }

// Message is one frame from the server, decoded.
type Message struct {
	Output   []byte    // console bytes
	Activity *Activity // panel line
	Marker   string    // guest marker
	Prompt   *Prompt   // a question, or nil to clear
	Paused   *bool
	Exit     *Exit
	kind     byte
}

// Next reads the next server frame. io.EOF means the server went away
// without an Exit frame (it was killed, or the socket broke).
func (c *Client) Next() (Message, error) {
	t, payload, err := readFrame(c.conn)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = io.EOF
		}
		return Message{}, err
	}
	m := Message{kind: t}
	switch t {
	case frameOutput:
		m.Output = payload
	case frameActivity:
		var a Activity
		if json.Unmarshal(payload, &a) == nil {
			m.Activity = &a
		}
	case frameMarker:
		m.Marker = string(payload)
	case framePrompt:
		if len(payload) > 0 {
			var p Prompt
			if json.Unmarshal(payload, &p) == nil {
				m.Prompt = &p
			}
		}
	case framePaused:
		v := len(payload) > 0 && payload[0] == '1'
		m.Paused = &v
	case frameExit:
		var x Exit
		json.Unmarshal(payload, &x)
		m.Exit = &x
	}
	return m, nil
}

// IsPromptClear reports a frame that removes the current prompt.
func (m Message) IsPromptClear() bool { return m.kind == framePrompt && m.Prompt == nil }
