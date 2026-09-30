package ui

import (
	"bytes"
	"io"
	"strconv"
	"strings"
)

// Mouse reporting: while the session owns the screen we ask the terminal to
// send mouse events to us instead of scrolling its own scrollback (which is
// what makes the UI look "scrollable" in macOS Terminal). 1000 = button and
// wheel events, 1006 = SGR encoding.
const (
	mouseOn  = "\x1b[?1000h\x1b[?1006h"
	mouseOff = "\x1b[?1006l\x1b[?1000l"
)

const wheelStep = 3

// Input wraps the host's stdin. It removes SGR mouse sequences, turning the
// wheel into console scrollback, and passes everything else to the guest.
func (s *Session) Input(r io.Reader) io.Reader {
	return &inputFilter{r: r, s: s}
}

type inputFilter struct {
	r       io.Reader
	s       *Session
	pending []byte // an incomplete mouse sequence carried to the next read
}

var sgrPrefix = []byte("\x1b[<")

func (f *inputFilter) Read(p []byte) (int, error) {
	for {
		buf := make([]byte, len(p))
		n, err := f.r.Read(buf)
		data := append(f.pending, buf[:n]...)
		f.pending = nil
		out := f.filter(data)
		if len(out) > 0 || err != nil {
			return copy(p, out), err
		}
		// Only mouse events arrived; read again rather than returning 0.
	}
}

func (f *inputFilter) filter(data []byte) []byte {
	var out []byte
	// While a prompt is up, every key goes to it and nothing reaches the guest.
	if f.s.prompt != nil {
		for _, b := range data {
			f.s.promptKey(b)
		}
		return nil
	}
	for len(data) > 0 {
		i := bytes.Index(data, sgrPrefix)
		if i < 0 {
			out = append(out, data...)
			break
		}
		out = append(out, data[:i]...)
		rest := data[i+len(sgrPrefix):]
		end := bytes.IndexAny(rest, "Mm")
		if end < 0 {
			f.pending = append([]byte(nil), data[i:]...)
			break
		}
		f.mouse(string(rest[:end]))
		data = rest[end+1:]
	}
	if len(out) > 0 {
		f.s.scrollTo(0) // typing returns to the live view
	}
	return out
}

// mouse handles one SGR mouse report body "b;x;y".
func (f *inputFilter) mouse(body string) {
	parts := strings.SplitN(body, ";", 3)
	if len(parts) != 3 {
		return
	}
	b, err := strconv.Atoi(parts[0])
	if err != nil {
		return
	}
	switch b &^ (4 | 8 | 16) { // ignore shift/meta/ctrl modifiers
	case 64:
		f.s.scrollBy(wheelStep)
	case 65:
		f.s.scrollBy(-wheelStep)
	}
}
