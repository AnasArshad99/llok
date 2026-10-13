package session

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AnasArshad99/llok/internal/guard"
	"github.com/AnasArshad99/llok/internal/ui"
)

func TestAttachRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := New("t", ui.Info{Project: "p", Agent: "shell"})
	var (
		mu      sync.Mutex
		resized []Size
		ops     []string
	)
	srv.OnResize = func(c, r int) { mu.Lock(); resized = append(resized, Size{c, r}); mu.Unlock() }
	srv.OnOp = func(op string) error { mu.Lock(); ops = append(ops, op); mu.Unlock(); return nil }
	sock := filepath.Join(dir, "c.sock")
	if err := srv.Listen(sock); err != nil {
		t.Fatal(err)
	}
	// Output and activity before anyone is attached are kept for replay.
	srv.Write([]byte("hello\r\n"))
	srv.Event("vm starting")
	srv.Marker("LLOK_READY uptime=0.1s")

	cl, err := Dial(sock, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	h := cl.Hello
	if h.Name != "t" || h.State != "running" || len(h.Activity) != 1 || len(h.Markers) != 1 || h.Info.Project != "p" {
		t.Fatalf("hello: %+v", h)
	}
	m, err := cl.Next()
	if err != nil || string(m.Output) != "hello\r\n" {
		t.Fatalf("replay: %+v %v", m, err)
	}
	mu.Lock()
	if len(resized) != 1 || resized[0].Cols != 80 {
		t.Errorf("resize on attach: %v", resized)
	}
	mu.Unlock()

	// Keys reach the VM's input reader.
	cl.Input([]byte("ls\r"))
	buf := make([]byte, 16)
	n, _ := srv.Input().Read(buf)
	if string(buf[:n]) != "ls\r" {
		t.Errorf("input: %q", buf[:n])
	}

	// A supervisor prompt is relayed and answered.
	answered := make(chan byte, 1)
	go func() {
		answered <- srv.Ask("hooks?", []guard.Option{{Key: 'r', Label: "resume"}, {Key: 'K', Label: "kill"}})
	}()
	m, _ = cl.Next()
	if m.Prompt == nil || m.Prompt.Question != "hooks?" || len(m.Prompt.Options) != 2 {
		t.Fatalf("prompt: %+v", m)
	}
	cl.Answer('r')
	select {
	case k := <-answered:
		if k != 'r' {
			t.Errorf("answer %c", k)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never answered")
	}
	if m, _ = cl.Next(); !m.IsPromptClear() {
		t.Errorf("expected prompt clear, got %+v", m)
	}

	cl.Op("poweroff")
	cl.Resize(100, 40)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(ops) != 1 || ops[0] != "poweroff" || len(resized) != 2 {
		t.Errorf("ops %v resized %v", ops, resized)
	}
	mu.Unlock()

	// Exit reaches the client, then the socket is gone.
	srv.Close(Exit{Code: 3, DurationMS: 10})
	for {
		m, err = cl.Next()
		if err != nil {
			t.Fatalf("no exit frame: %v", err)
		}
		if m.Exit != nil {
			break
		}
	}
	if m.Exit.Code != 3 {
		t.Errorf("exit %+v", m.Exit)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Error("socket should be removed")
	}
	if _, err := cl.Next(); err != io.EOF {
		t.Errorf("after close: %v", err)
	}
}

func TestScrollbackTrimsWholeLines(t *testing.T) {
	srv := New("t", ui.Info{})
	line := []byte("0123456789\n")
	for i := 0; i < scrollbackBytes/len(line)+50; i++ {
		srv.Write(line)
	}
	if len(srv.out) > scrollbackBytes || srv.out[0] != '0' {
		t.Errorf("ring: len %d first %q", len(srv.out), srv.out[0])
	}
}

func TestRecordsAndNames(t *testing.T) {
	home := t.TempDir()
	if n := FreeName(home, "app"); n != "app" {
		t.Errorf("first name %q", n)
	}
	WriteRecord(home, Record{Name: "app", Project: "app", Repo: "/r", PID: os.Getpid(), Attach: true})
	WriteRecord(home, Record{Name: "dead", Project: "app", Repo: "/r", PID: 999999999})
	if n := FreeName(home, "app"); n != "app-2" {
		t.Errorf("second name %q", n)
	}
	list := List(home)
	if len(list) != 1 || list[0].Name != "app" {
		t.Errorf("list %+v", list)
	}
	if _, err := os.Stat(Dir(home, "dead")); err == nil {
		t.Error("dead session's record should be removed")
	}
	if rs := ForRepo(home, "/r"); len(rs) != 1 {
		t.Errorf("for repo: %+v", rs)
	}
	RemoveRecord(home, "app")
	if _, ok := Find(home, "app"); ok {
		t.Error("removed record still found")
	}
}
