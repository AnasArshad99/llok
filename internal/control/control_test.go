package control

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestResizeReplayedAndShutdown(t *testing.T) {
	h := New()
	// Resize before any guest is attached is remembered.
	if err := h.Resize(100, 40); err == nil {
		t.Error("expected not-connected error before attach")
	}

	hostSide, guestSide := net.Pipe()
	go h.attach(hostSide)

	sc := bufio.NewScanner(guestSide)
	read := func() Command {
		guestSide.SetReadDeadline(time.Now().Add(time.Second))
		if !sc.Scan() {
			t.Fatal("no command received")
		}
		var c Command
		json.Unmarshal(sc.Bytes(), &c)
		return c
	}
	if c := read(); c.Op != "resize" || c.Cols != 100 || c.Rows != 40 {
		t.Errorf("replayed resize = %+v", c)
	}
	if !h.WaitConnected(time.Second) || !h.Connected() {
		t.Error("host should report connected")
	}

	go h.Shutdown()
	if c := read(); c.Op != "shutdown" {
		t.Errorf("got %+v, want shutdown", c)
	}

	// A reply reaches OnReply.
	got := make(chan Reply, 1)
	h.OnReply = func(r Reply) { got <- r }
	json.NewEncoder(guestSide).Encode(Reply{Op: "shutdown", OK: true})
	select {
	case r := <-got:
		if r.Op != "shutdown" || !r.OK {
			t.Errorf("reply = %+v", r)
		}
	case <-time.After(time.Second):
		t.Error("no reply delivered")
	}
}
