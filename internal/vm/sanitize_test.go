package vm

import (
	"bytes"
	"testing"
)

func sanitize(t *testing.T, chunks ...string) string {
	t.Helper()
	var buf bytes.Buffer
	s := &sanitizer{w: &buf}
	for _, c := range chunks {
		s.Write([]byte(c))
	}
	s.Flush()
	return buf.String()
}

func TestSanitizerKeepsTextAndColours(t *testing.T) {
	in := "ok \x1b[31mred\x1b[0m\r\n\ttab\b"
	if got := sanitize(t, in); got != in {
		t.Errorf("got %q", got)
	}
}

func TestSanitizerDropsDangerousSequences(t *testing.T) {
	cases := map[string]string{
		"a\x1b]0;evil title\x07b":    "ab", // window title
		"a\x1b]52;c;ZXZpbA==\x1b\\b": "ab", // clipboard write
		"a\x1b[6nb":                  "ab", // cursor position query
		"a\x1b[2J\x1b[H\x1b[?1049hb": "ab", // clear, home, alt screen
		"a\x1bcb":                    "ab", // full reset
		"a\x1bP+q544e\x1b\\b":        "ab", // DCS query
		"a\x1b_payload\x1b\\b":       "ab", // APC
		"a\x1b(Bb":                   "ab", // charset
		"a\x00\x01\x7fb":             "ab", // other control bytes
	}
	for in, want := range cases {
		if got := sanitize(t, in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizerSplitSequences(t *testing.T) {
	if got := sanitize(t, "a\x1b]0;ti", "tle\x07b\x1b[3", "1mc"); got != "ab\x1b[31mc" {
		t.Errorf("got %q", got)
	}
}
