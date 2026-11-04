package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/AnasArshad99/llok/internal/guard"
	"github.com/AnasArshad99/llok/internal/session"
	"github.com/AnasArshad99/llok/internal/ui"
)

type guardOption = guard.Option

// attachLoop attaches to name and follows the user from session to session
// until they detach or a session they are watching ends.
func attachLoop(home, name string) error {
	for {
		out, err := attachOnce(home, name)
		if err != nil {
			return err
		}
		switch out.action {
		case "detach":
			fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · detached from "+name+styleDim.Render(" (still running) · llok attach "+name))
			return nil
		case "switch":
			name = out.target
		case "new":
			r, _ := session.Find(home, name)
			if r.Repo == "" {
				return errors.New("cannot start a new session: the current one is gone")
			}
			spec, err := prepareRun(r.Repo, "", r.CPUs, r.MemoryMB, "", false)
			if err != nil {
				return err
			}
			spec.name = session.FreeName(home, spec.cfg.Name)
			spec.readOnly = spec.cfg.Mount.ReadOnly || r.ReadOnly
			if err := startServer(home, spec, nil); err != nil {
				return err
			}
			name = spec.name
		case "exit":
			x := out.exit
			if x == nil {
				return errors.New("session " + name + " ended unexpectedly (see " + session.LogPath(home, name) + ")")
			}
			if x.StoppedBy != "" {
				why := "vm stopped by the supervisor: " + x.StoppedBy
				if strings.HasPrefix(x.StoppedBy, "killed by you") || strings.HasPrefix(x.StoppedBy, "idle") {
					why = "vm " + x.StoppedBy
				}
				fmt.Fprintln(os.Stderr, styleErr.Render("llok")+" · "+why)
				os.Exit(125)
			}
			if x.Err != "" {
				return errors.New(x.Err)
			}
			dur := (time.Duration(x.DurationMS) * time.Millisecond).Round(100 * time.Millisecond)
			switch {
			case x.Forced:
				fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · vm powered off on request after "+dur.String())
			case x.Code != 0:
				fmt.Fprintln(os.Stderr, styleErr.Render("llok")+fmt.Sprintf(" · vm ran %s · exit %d", dur, x.Code))
				os.Exit(x.Code)
			default:
				fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · vm ran "+dur.String()+" · powered off")
			}
			return nil
		}
	}
}

type attachOutcome struct {
	action string // detach | switch | new | exit
	target string
	exit   *session.Exit
}

// stdinPump reads the terminal once for the whole process, so attaching to
// one session after another never leaves two readers on stdin.
var stdinPump struct {
	once   sync.Once
	chunks chan []byte
}

func startStdinPump() {
	stdinPump.chunks = make(chan []byte, 64)
	go func() {
		for {
			buf := make([]byte, 4096)
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				stdinPump.chunks <- buf[:n]
			}
			if err != nil {
				close(stdinPump.chunks)
				return
			}
		}
	}()
}

// pumpReader hands one attachment the pump's chunks until it is released.
type pumpReader struct {
	done chan struct{}
}

func (r *pumpReader) Read(p []byte) (int, error) {
	select {
	case <-r.done:
		return 0, io.EOF
	case c, ok := <-stdinPump.chunks:
		if !ok {
			return 0, io.EOF
		}
		return copy(p, c), nil
	}
}

const prefixKey = 0x1d // Ctrl-]

// attachOnce runs the full-screen UI against one session until the user
// leaves it or it ends.
func attachOnce(home, name string) (attachOutcome, error) {
	stdinPump.once.Do(startStdinPump)
	cols, rows := ui.TermSize()
	if cols >= 90 {
		cols -= 33
	}
	cl, err := session.Dial(session.ConsoleSocket(home, name), session.Size{Cols: cols, Rows: rows})
	if err != nil {
		return attachOutcome{}, fmt.Errorf("no running session named %s", name)
	}
	defer cl.Close()
	h := cl.Hello
	h.Info.Session = name
	sess := ui.New(h.Info)
	sess.SetState(h.State)
	for _, m := range h.Markers {
		sess.Marker(m)
	}
	for _, a := range h.Activity {
		sess.EventAt(a.At, a.Text)
	}

	var (
		out    attachOutcome
		once   sync.Once
		done   = make(chan struct{})
		finish = func(o attachOutcome) { once.Do(func() { out = o; close(done) }) }
		// Emulator replies (cursor position etc.) go to the guest only once
		// the replay is in: answering replayed queries would feed stale
		// bytes to whatever is running inside.
		forward atomic.Bool
	)
	go func() {
		buf := make([]byte, 256)
		rd := sess.Replies()
		for {
			n, err := rd.Read(buf)
			if n > 0 && forward.Load() {
				cl.Input(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return attachOutcome{}, err
	}
	defer term.Restore(fd, old)
	sess.OnResize = func(c, r int) { cl.Resize(c, r) }
	sess.Start()
	defer sess.Stop()

	// A prompt the supervisor is waiting on: show it, relay the answer.
	showPrompt := func(p *session.Prompt) {
		go func() {
			if k := sess.Ask(p.Question, p.Options); k != 0 {
				cl.Answer(k)
			}
		}()
	}
	if h.Prompt != nil {
		showPrompt(h.Prompt)
	}

	// Server frames -> screen. The first output frame is the replay.
	go func() {
		first := true
		for {
			m, err := cl.Next()
			if err != nil {
				finish(attachOutcome{action: "exit"})
				return
			}
			switch {
			case m.Output != nil:
				sess.Write(m.Output)
				if first {
					first = false
					forward.Store(true)
				}
			case m.Activity != nil:
				sess.EventAt(m.Activity.At, m.Activity.Text)
			case m.Marker != "":
				sess.Marker(m.Marker)
			case m.Prompt != nil:
				showPrompt(m.Prompt)
			case m.IsPromptClear():
				sess.CancelPrompt()
			case m.Paused != nil:
				sess.SetPaused(*m.Paused)
			case m.Exit != nil:
				x := *m.Exit
				finish(attachOutcome{action: "exit", exit: &x})
				return
			}
		}
	}()
	// A session with no output yet has nothing to replay; forward from now.
	go func() {
		time.Sleep(200 * time.Millisecond)
		forward.Store(true)
	}()

	// Keys -> guest, except the Ctrl-] prefix, which opens the session menu.
	pr := &pumpReader{done: done}
	go func() {
		in := sess.Input(pr)
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				data := buf[:n]
				if i := strings.IndexByte(string(data), prefixKey); i >= 0 {
					if i > 0 {
						cl.Input(data[:i])
					}
					go sessionMenu(home, name, sess, cl, finish)
				} else {
					cl.Input(data)
				}
			}
			if err != nil {
				return
			}
		}
	}()

	<-done
	return out, nil
}

// sessionMenu is what Ctrl-] opens: leave, switch, start another, or end
// this one. Any other key closes it.
func sessionMenu(home, name string, sess *ui.Session, cl *session.Client, finish func(attachOutcome)) {
	k := sess.Menu("session "+name, []ui.Option{
		{Key: 'd', Label: "detach"},
		{Key: 's', Label: "switch"},
		{Key: 'n', Label: "new"},
		{Key: 'K', Label: "kill"},
		{Key: prefixKey, Label: "power off", Show: "C-]"},
		{Key: ']', Label: "send C-]"},
	})
	switch k {
	case 'd':
		finish(attachOutcome{action: "detach"})
	case 's':
		var others []session.Record
		for _, r := range session.List(home) {
			if r.Name != name && r.Attach {
				others = append(others, r)
			}
		}
		if len(others) == 0 {
			sess.Event("no other session to switch to (n starts one)")
			return
		}
		var opts []ui.Option
		for i, r := range others {
			if i >= 9 {
				break
			}
			opts = append(opts, ui.Option{Key: byte('1' + i), Label: r.Name})
		}
		if c := sess.Menu("switch to", opts); c != 0 {
			finish(attachOutcome{action: "switch", target: others[c-'1'].Name})
		}
	case 'n':
		finish(attachOutcome{action: "new"})
	case 'K':
		cl.Op("kill")
	case prefixKey:
		cl.Op("poweroff")
	case ']':
		cl.Input([]byte{prefixKey})
	}
}

func fmtUptime(r session.Record) string {
	d := time.Since(r.Started).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func splitLines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }
func joinLines(l []string) string  { return strings.Join(l, "\n") }
