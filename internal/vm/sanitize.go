package vm

import "io"

// sanitizer passes guest console output to a real terminal with only the
// sequences a log or plain console needs: printable text, newlines, tabs,
// backspace, and SGR colour attributes (ESC [ ... m). Every other escape
// sequence is dropped: OSC (titles, clipboard), DCS/APC/PM/SOS, cursor and
// mode controls, and terminal queries whose replies would be typed into
// the host shell. The guest is untrusted; its bytes must not program the
// user's terminal.
type sanitizer struct {
	w   io.Writer
	buf []byte // bytes held back while a sequence is incomplete
}

func (s *sanitizer) Write(p []byte) (int, error) {
	data := append(s.buf, p...)
	s.buf = nil
	var out []byte
	i := 0
	for i < len(data) {
		b := data[i]
		switch {
		case b == 0x1b:
			n, keep, done := scanEscape(data[i:])
			if !done {
				s.buf = append([]byte(nil), data[i:]...)
				i = len(data)
				continue
			}
			if keep {
				out = append(out, data[i:i+n]...)
			}
			i += n
		case b == '\n' || b == '\r' || b == '\t' || b == '\b' || b >= 0x20 && b != 0x7f:
			out = append(out, b)
			i++
		default:
			i++ // other control bytes are dropped
		}
	}
	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// scanEscape classifies the escape sequence at the start of b. It returns
// its length, whether to keep it, and whether it is complete.
func scanEscape(b []byte) (n int, keep, done bool) {
	if len(b) < 2 {
		return 0, false, false
	}
	switch b[1] {
	case '[': // CSI: parameters/intermediates then a final byte 0x40..0x7e
		for i := 2; i < len(b); i++ {
			c := b[i]
			if c >= 0x40 && c <= 0x7e {
				keep = c == 'm' // SGR only
				return i + 1, keep, true
			}
			if c < 0x20 || c > 0x3f {
				return i, false, true // malformed: drop what we have
			}
			if i > 64 {
				return i, false, true
			}
		}
		return 0, false, false
	case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: until BEL or ESC \
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1, false, true
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2, false, true
			}
			if i > 4096 {
				return i, false, true
			}
		}
		return 0, false, false
	case '(', ')', '*', '+', '#', '%': // charset / line-size: ESC x y
		if len(b) < 3 {
			return 0, false, false
		}
		return 3, false, true
	default: // two-byte ESC sequences (RIS, DECSC, keypad modes, ...)
		return 2, false, true
	}
}

// Flush drops any incomplete sequence held back.
func (s *sanitizer) Flush() { s.buf = nil }
