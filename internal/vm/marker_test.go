package vm

import (
	"bytes"
	"strings"
	"testing"
)

func runFilter(t *testing.T, chunks ...string) (out string, markers []string) {
	t.Helper()
	var buf bytes.Buffer
	f := &markerFilter{w: &buf, on: func(m string) { markers = append(markers, m) }}
	for _, c := range chunks {
		if _, err := f.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	f.Flush()
	return buf.String(), markers
}

func TestMarkerFilterStripsAndReports(t *testing.T) {
	out, m := runFilter(t, "hello \x1b]7777;LLOK_READY uptime=0.1s\x1b\\world\n")
	if out != "hello world\n" {
		t.Errorf("out = %q", out)
	}
	if len(m) != 1 || m[0] != "LLOK_READY uptime=0.1s" {
		t.Errorf("markers = %q", m)
	}
}

func TestMarkerFilterBELTerminator(t *testing.T) {
	out, m := runFilter(t, "a\x1b]7777;LLOK_EXIT 3\x07b")
	if out != "ab" || len(m) != 1 || m[0] != "LLOK_EXIT 3" {
		t.Errorf("out=%q markers=%q", out, m)
	}
}

func TestMarkerFilterSplitAcrossWrites(t *testing.T) {
	// Split inside the prefix, inside the payload, and inside the terminator.
	out, m := runFilter(t, "x\x1b]", "77", "77;LLOK_EX", "IT 7\x1b", "\\y")
	if out != "xy" {
		t.Errorf("out = %q", out)
	}
	if len(m) != 1 || m[0] != "LLOK_EXIT 7" {
		t.Errorf("markers = %q", m)
	}
}

func TestMarkerFilterPassesLookalikes(t *testing.T) {
	// A lone ESC or a different OSC must pass through untouched.
	out, m := runFilter(t, "\x1b[31mred\x1b[m \x1b]0;title\x07 \x1b")
	if out != "\x1b[31mred\x1b[m \x1b]0;title\x07 \x1b" {
		t.Errorf("out = %q", out)
	}
	if len(m) != 0 {
		t.Errorf("unexpected markers %q", m)
	}
}

func TestMarkerFilterRunaway(t *testing.T) {
	out, m := runFilter(t, "\x1b]7777;"+strings.Repeat("A", 5000), "after")
	if len(m) != 0 {
		t.Errorf("runaway should not produce a marker: %q", m)
	}
	if !strings.HasSuffix(out, "after") {
		t.Errorf("output after a runaway sequence should resume: %q", out)
	}
}

func TestKernelNoise(t *testing.T) {
	var buf bytes.Buffer
	k := &kernelNoise{w: &buf}
	k.Write([]byte("GUEST_OK\n[    0.12] reboot: Power down\r\nlast"))
	k.Flush()
	if got := buf.String(); got != "GUEST_OK\nlast" {
		t.Errorf("got %q", got)
	}
}
