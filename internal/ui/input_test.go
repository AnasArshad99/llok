package ui

import (
	"bytes"
	"io"
	"testing"
)

func TestInputFilterStripsMouseAndScrolls(t *testing.T) {
	s := New(Info{})
	// Fill the emulator with lines so there is scrollback to move through.
	for i := 0; i < 200; i++ {
		s.Write([]byte("line\r\n"))
	}
	r := s.Input(bytes.NewReader([]byte("ab\x1b[<64;10;5Mc\x1b[<64;10;5Md")))
	got, _ := io.ReadAll(r)
	if string(got) != "abcd" {
		t.Errorf("guest got %q, mouse sequences should be stripped", got)
	}
	// Typing after a wheel event returns to the live view.
	if s.scroll != 0 {
		t.Errorf("typing should reset scroll, got %d", s.scroll)
	}

	r = s.Input(bytes.NewReader([]byte("\x1b[<64;1;1M\x1b[<64;1;1M")))
	io.ReadAll(r)
	if s.scroll != 2*wheelStep {
		t.Errorf("two wheel-ups should scroll %d, got %d", 2*wheelStep, s.scroll)
	}
	r = s.Input(bytes.NewReader([]byte("\x1b[<65;1;1M")))
	io.ReadAll(r)
	if s.scroll != wheelStep {
		t.Errorf("wheel-down should scroll back to %d, got %d", wheelStep, s.scroll)
	}
}

func TestInputFilterSequenceSplitAcrossReads(t *testing.T) {
	s := New(Info{})
	for i := 0; i < 50; i++ {
		s.Write([]byte("x\r\n"))
	}
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("\x1b[<6"))
		pw.Write([]byte("4;3;3Mok"))
		pw.Close()
	}()
	got, _ := io.ReadAll(s.Input(pr))
	if string(got) != "ok" {
		t.Errorf("got %q", got)
	}
}
