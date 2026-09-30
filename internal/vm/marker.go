package vm

import (
	"bytes"
	"io"
	"regexp"
)

// The guest sends control markers as a private OSC sequence:
//
//	ESC ] 7777 ; <payload> ESC \      (or BEL as terminator)
//
// Real terminals ignore unknown OSCs, but we strip them so logs stay clean
// and hand the payload to a callback.
var (
	markerPrefix = []byte("\x1b]7777;")
	stTerm       = []byte("\x1b\\")
)

type markerFilter struct {
	w     io.Writer
	on    func(string)
	buf   []byte
	inSeq bool
}

func (f *markerFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		if f.inSeq {
			end, tlen := -1, 0
			if i := bytes.IndexByte(f.buf, 0x07); i >= 0 {
				end, tlen = i, 1
			}
			if i := bytes.Index(f.buf, stTerm); i >= 0 && (end < 0 || i < end) {
				end, tlen = i, 2
			}
			if end < 0 {
				if len(f.buf) > 4096 { // runaway: give up on this sequence
					f.buf = f.buf[:0]
					f.inSeq = false
				}
				return len(p), nil
			}
			if f.on != nil {
				f.on(string(f.buf[:end]))
			}
			f.buf = append([]byte(nil), f.buf[end+tlen:]...)
			f.inSeq = false
			continue
		}
		i := bytes.Index(f.buf, markerPrefix)
		if i < 0 {
			keep := partialSuffix(f.buf, markerPrefix)
			out := f.buf[:len(f.buf)-keep]
			if len(out) > 0 {
				if _, err := f.w.Write(out); err != nil {
					return 0, err
				}
			}
			f.buf = append([]byte(nil), f.buf[len(out):]...)
			return len(p), nil
		}
		if i > 0 {
			if _, err := f.w.Write(f.buf[:i]); err != nil {
				return 0, err
			}
		}
		f.buf = append([]byte(nil), f.buf[i+len(markerPrefix):]...)
		f.inSeq = true
	}
}

// Flush writes out any bytes held back as a possible marker prefix.
func (f *markerFilter) Flush() {
	if !f.inSeq && len(f.buf) > 0 {
		f.w.Write(f.buf)
		f.buf = f.buf[:0]
	}
}

// partialSuffix returns the length of the longest suffix of b that is a
// proper prefix of pat.
func partialSuffix(b, pat []byte) int {
	max := len(pat) - 1
	if max > len(b) {
		max = len(b)
	}
	for n := max; n > 0; n-- {
		if bytes.Equal(b[len(b)-n:], pat[:n]) {
			return n
		}
	}
	return 0
}

// kernelNoise drops the few kernel lines that still reach the console at
// loglevel=3, such as "reboot: Power down", so command output stays clean.
type kernelNoise struct {
	w   io.Writer
	buf []byte
}

var noiseRe = regexp.MustCompile(`^\[\s*[0-9.]+\] reboot: `)

func (k *kernelNoise) Write(p []byte) (int, error) {
	k.buf = append(k.buf, p...)
	for {
		i := bytes.IndexByte(k.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := k.buf[:i+1]
		if !noiseRe.Match(bytes.TrimRight(line, "\r\n")) {
			if _, err := k.w.Write(line); err != nil {
				return 0, err
			}
		}
		k.buf = append([]byte(nil), k.buf[i+1:]...)
	}
}

func (k *kernelNoise) Flush() {
	if len(k.buf) > 0 && !noiseRe.Match(k.buf) {
		k.w.Write(k.buf)
	}
	k.buf = nil
}
