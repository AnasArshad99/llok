package ui

import (
	"bytes"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// While the session owns the screen we own the mouse too, as opencode does:
// the terminal then cannot scroll away from the UI, the wheel scrolls the
// VM's own history, and click-drag selects console text which llok copies
// to the clipboard itself. 1000 = buttons, 1002 = drag motion, 1006 = SGR.
const (
	mouseOn  = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	mouseOff = "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
)

const wheelStep = 3

// Input wraps the host's stdin. Mouse reports are consumed here; keys go to
// an active prompt when one is up, otherwise to the guest.
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
	if f.s.hasPrompt() {
		for _, b := range data {
			if b != 0x1b { // leave mouse sequences out of the prompt
				f.s.promptKey(b)
			}
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
		f.mouse(string(rest[:end]), rest[end] == 'M')
		data = rest[end+1:]
	}
	if len(out) > 0 {
		f.s.scrollTo(0)      // typing returns to the live view
		f.s.clearSelection() // and drops any selection
	}
	return out
}

// mouse handles one SGR mouse report "b;x;y" (press when press is true,
// release otherwise). Coordinates are 1-based terminal cells.
func (f *inputFilter) mouse(body string, press bool) {
	parts := strings.SplitN(body, ";", 3)
	if len(parts) != 3 {
		return
	}
	b, err1 := strconv.Atoi(parts[0])
	x, err2 := strconv.Atoi(parts[1])
	y, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	motion := b&32 != 0
	button := b &^ (4 | 8 | 16 | 32) // drop shift/meta/ctrl and motion bits
	switch {
	case button == 64 && press:
		f.s.scrollBy(wheelStep)
	case button == 65 && press:
		f.s.scrollBy(-wheelStep)
	case button == 0 && press && !motion:
		f.s.selectStart(x-1, y-1)
	case button == 0 && press && motion:
		f.s.selectExtend(x-1, y-1)
	case button == 0 && !press:
		f.s.selectFinish(x-1, y-1)
	}
}

// copyToClipboard puts text on the macOS clipboard. The terminal's own
// selection is unavailable while we own the mouse, so this replaces it.
func copyToClipboard(text string) error {
	c := exec.Command("pbcopy")
	c.Stdin = strings.NewReader(text)
	return c.Run()
}
