package events

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBusReplayAndOrder(t *testing.T) {
	b := NewBus("p")
	b.Emit(Event{Type: TypeSession, Phase: "start"})
	b.Emit(Event{Type: TypeFile, Path: "a", Op: "write"})
	ch, cancel := b.Subscribe()
	defer cancel()
	first := <-ch
	if first.Seq != 1 || first.Project != "p" || first.Phase != "start" {
		t.Fatalf("replay: %+v", first)
	}
	if second := <-ch; second.Seq != 2 || second.Path != "a" {
		t.Fatalf("replay order: %+v", second)
	}
	b.Emit(Event{Type: TypeNet, Host: "h", Decision: "allow"})
	if live := <-ch; live.Seq != 3 || live.Host != "h" {
		t.Fatalf("live: %+v", live)
	}
	b.Close()
	if _, ok := <-ch; ok {
		t.Error("channel must close with the bus")
	}
}

func TestBusDropsSlowSubscriber(t *testing.T) {
	b := NewBus("p")
	ch, cancel := b.Subscribe()
	defer cancel()
	for i := 0; i < subBuffer+replayMax+10; i++ {
		b.Emit(Event{Type: TypeFile})
	}
	// Drain: the channel must end (closed) rather than block the emitter.
	n := 0
	for range ch {
		n++
	}
	if n == 0 {
		t.Error("expected buffered events before the drop")
	}
}

type fakeCtl struct {
	mu   sync.Mutex
	ops  []string
	fail bool
}

func (f *fakeCtl) rec(op string) error {
	f.mu.Lock()
	f.ops = append(f.ops, op)
	f.mu.Unlock()
	if f.fail {
		return errors.New("nope")
	}
	return nil
}
func (f *fakeCtl) Pause() error  { return f.rec("pause") }
func (f *fakeCtl) Resume() error { return f.rec("resume") }
func (f *fakeCtl) Kill() error   { return f.rec("kill") }

func TestServerStreamsAndControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sock")
	b := NewBus("p")
	ctl := &fakeCtl{}
	srv, err := Serve(path, b, ctl)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	b.Emit(Event{Type: TypeSession, Phase: "start"})

	if _, err := Serve(path, b, ctl); err == nil {
		t.Error("a second server on a live socket must be refused")
	}

	c, sc, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !sc.Scan() {
		t.Fatal("no replayed event")
	}
	var ev Event
	json.Unmarshal(sc.Bytes(), &ev)
	if ev.Phase != "start" {
		t.Fatalf("got %+v", ev)
	}
	c.Write([]byte(`{"op":"kill"}` + "\n"))
	if !sc.Scan() {
		t.Fatal("no control event")
	}
	json.Unmarshal(sc.Bytes(), &ev)
	if ev.Type != TypeControl || ev.Op2 != "kill" || ev.Reason != "" {
		t.Fatalf("control echo: %+v", ev)
	}
	ctl.mu.Lock()
	got := append([]string(nil), ctl.ops...)
	ctl.mu.Unlock()
	if len(got) != 1 || got[0] != "kill" {
		t.Fatalf("controller saw %v", got)
	}
	c.Write([]byte(`{"op":"dance"}` + "\n"))
	sc.Scan()
	json.Unmarshal(sc.Bytes(), &ev)
	if ev.Reason == "" {
		t.Error("unknown op must report a reason")
	}
}

func TestServerCloseRemovesSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sock")
	srv, err := Serve(path, NewBus("p"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, sc, _ := Dial(path)
	defer c.Close()
	srv.Close()
	deadline := time.Now().Add(2 * time.Second)
	for sc.Scan() && time.Now().Before(deadline) {
	}
	if _, _, err := Dial(path); err == nil {
		t.Error("socket must be gone after Close")
	}
}
