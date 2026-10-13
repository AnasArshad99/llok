package ui

import (
	"io"
	"strings"
	"testing"
)

func TestDragSelectCopiesText(t *testing.T) {
	s := New(Info{})
	var copied string
	s.copy = func(text string) error { copied = text; return nil }
	s.Write([]byte("hello world\r\nsecond line here\r\n"))

	// Press at (0,0), drag to (5,1), release: "hello world" + "second".
	in := "\x1b[<0;1;1M" + "\x1b[<32;6;2M" + "\x1b[<0;6;2m"
	io.ReadAll(s.Input(strings.NewReader(in)))
	if copied != "hello world\nsecond" {
		t.Errorf("copied %q", copied)
	}
	if !s.sel.active || s.sel.dragging {
		t.Error("selection should stay highlighted after release")
	}
	// Typing clears it.
	io.ReadAll(s.Input(strings.NewReader("x")))
	if s.sel.active {
		t.Error("a key should clear the selection")
	}
	// A plain click copies nothing.
	copied = ""
	io.ReadAll(s.Input(strings.NewReader("\x1b[<0;3;1M\x1b[<0;3;1m")))
	if copied != "" || s.sel.active {
		t.Error("a click without drag must not copy or select")
	}
}

func TestSelectionRendersReverseVideo(t *testing.T) {
	s := New(Info{})
	s.Write([]byte("abc\r\n"))
	s.selectStart(0, 0)
	s.selectExtend(1, 0)
	row := s.highlightedRow(0)
	if !strings.Contains(row, "\x1b[7ma\x1b[27m") || !strings.Contains(row, "\x1b[7mb\x1b[27m") || strings.Contains(row, "\x1b[7mc") {
		t.Errorf("row = %q", row)
	}
}
