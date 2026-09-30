// Package ui renders the VM console in a full-screen layout: the guest's
// terminal on the left (via a virtual terminal emulator) and a status panel
// on the right, the way tmux or opencode take over the terminal.
package ui

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/term"
)

// Info is the static information shown in the panel.
type Info struct {
	Project  string
	RepoHost string
	RepoVM   string
	CPUs     uint
	MemoryMB uint64
	Kernel   string
}

// Session owns the terminal while the VM runs.
type Session struct {
	info Info
	out  *os.File

	mu       sync.Mutex
	emu      *vt.SafeEmulator
	cols     int
	rows     int
	consoleW int
	panelW   int
	dirty    bool
	state    string // booting | running | stopped
	bootTime string
	rootfs   string
	exitCode int
	events   []string
	start    time.Time
	cursorOn bool

	stop chan struct{}
	done chan struct{}
}

const (
	minPanelCols = 90 // below this width the panel is hidden
	panelWidth   = 34
)

var (
	colAccent = lipgloss.Color("#B57BEE")
	colDim    = lipgloss.Color("#8A8A8A")
	colOK     = lipgloss.Color("#5AD48A")
	colWarn   = lipgloss.Color("#F2C14E")
	colErr    = lipgloss.Color("#F26D6D")

	sTitle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	sDim   = lipgloss.NewStyle().Foreground(colDim)
	sKey   = lipgloss.NewStyle().Foreground(colDim).Width(9).MaxWidth(9)
	sOK    = lipgloss.NewStyle().Foreground(colOK)
	sWarn  = lipgloss.NewStyle().Foreground(colWarn)
	sErr   = lipgloss.NewStyle().Foreground(colErr)
	sSep   = lipgloss.NewStyle().Foreground(lipgloss.Color("#3A3A3A"))
	sHint  = lipgloss.NewStyle().Foreground(colOK)
)

// New creates a session sized to the current terminal.
func New(info Info) *Session {
	s := &Session{
		info:     info,
		out:      os.Stdout,
		state:    "booting",
		exitCode: -1,
		cursorOn: true,
		start:    time.Now(),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	s.layout()
	s.emu = vt.NewSafeEmulator(s.consoleW, s.rows)
	return s
}

func (s *Session) layout() {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols < 20 || rows < 5 {
		cols, rows = 120, 40
	}
	s.cols, s.rows = cols, rows
	if cols >= minPanelCols {
		s.panelW = panelWidth
		s.consoleW = cols - panelWidth - 1
	} else {
		s.panelW = 0
		s.consoleW = cols
	}
}

// ConsoleSize is the size the guest terminal should be told it has.
func (s *Session) ConsoleSize() (cols, rows int) { return s.consoleW, s.rows }

// Start takes over the terminal and begins rendering.
func (s *Session) Start() {
	fmt.Fprint(s.out, "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H")
	s.event("vm starting")
	go s.loop()
}

// Stop restores the terminal.
func (s *Session) Stop() {
	close(s.stop)
	<-s.done
	fmt.Fprint(s.out, "\x1b[?25h\x1b[?1049l")
}

// Replies returns the stream of answers the emulator generates for terminal
// queries the guest makes (cursor position, device attributes). It must be
// drained into the guest's input or the emulator blocks.
func (s *Session) Replies() io.Reader { return s.emu }

// Write feeds guest console bytes into the emulator.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	n, err := s.emu.Write(p)
	s.dirty = true
	s.mu.Unlock()
	return n, err
}

// Marker handles control markers the guest sends out of band.
func (s *Session) Marker(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasPrefix(m, "LLOK_READY"):
		s.state = "running"
		if i := strings.Index(m, "uptime="); i >= 0 {
			s.bootTime = strings.TrimSuffix(m[i+7:], "s")
		}
		s.eventLocked("guest ready in " + s.bootTime + "s")
		s.eventLocked("repo mounted at " + s.info.RepoVM)
	case strings.HasPrefix(m, "LLOK_INFO"):
		for _, kv := range strings.Fields(strings.TrimPrefix(m, "LLOK_INFO")) {
			if v, ok := strings.CutPrefix(kv, "rootfs="); ok {
				s.rootfs = v
			}
		}
		// rootfs= value may contain spaces; take everything after the key.
		if i := strings.Index(m, "rootfs="); i >= 0 {
			s.rootfs = m[i+7:]
		}
	case strings.HasPrefix(m, "LLOK_EXIT"):
		if c, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(m, "LLOK_EXIT"))); err == nil {
			s.exitCode = c
		}
		s.state = "stopped"
		s.eventLocked("guest powering off")
	}
	s.dirty = true
}

// Stopped marks the VM as gone (forced stop or crash).
func (s *Session) Stopped(reason string) {
	s.mu.Lock()
	s.state = "stopped"
	s.eventLocked(reason)
	s.dirty = true
	s.mu.Unlock()
}

func (s *Session) event(e string) {
	s.mu.Lock()
	s.eventLocked(e)
	s.mu.Unlock()
}

func (s *Session) eventLocked(e string) {
	ts := time.Since(s.start).Round(time.Second)
	s.events = append(s.events, fmt.Sprintf("%s %s", sDim.Render(fmtDur(ts)), e))
	if len(s.events) > 200 {
		s.events = s.events[len(s.events)-200:]
	}
	s.dirty = true
}

func (s *Session) loop() {
	defer close(s.done)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	frame := time.NewTicker(33 * time.Millisecond)
	defer frame.Stop()
	clock := time.NewTicker(time.Second)
	defer clock.Stop()
	s.render(true)
	for {
		select {
		case <-s.stop:
			return
		case <-winch:
			s.mu.Lock()
			s.layout()
			s.emu.Resize(s.consoleW, s.rows)
			s.dirty = true
			s.mu.Unlock()
			s.render(true)
		case <-clock.C:
			s.render(true)
		case <-frame.C:
			s.render(false)
		}
	}
}

func (s *Session) render(force bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && !s.dirty {
		return
	}
	s.dirty = false

	lines := strings.Split(s.emu.Render(), "\n")
	panel := s.panelLines()

	var b strings.Builder
	b.WriteString("\x1b[?2026h") // synchronized output (ignored where unsupported)
	for y := 0; y < s.rows; y++ {
		line := ""
		if y < len(lines) {
			line = lines[y]
		}
		line = ansi.Truncate(line, s.consoleW, "")
		pad := s.consoleW - ansi.StringWidth(line)
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[m", y+1, line)
		if pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		if s.panelW > 0 {
			b.WriteString(sSep.Render("│"))
			p := ""
			if y < len(panel) {
				p = panel[y]
			}
			p = ansi.Truncate(p, s.panelW, "")
			b.WriteString(p + "\x1b[m")
			if pp := s.panelW - ansi.StringWidth(p); pp > 0 {
				b.WriteString(strings.Repeat(" ", pp))
			}
		}
	}
	cur := s.emu.CursorPosition()
	if s.cursorOn && cur.X < s.consoleW && cur.Y < s.rows {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", cur.Y+1, cur.X+1)
	} else {
		b.WriteString("\x1b[?25l")
	}
	b.WriteString("\x1b[?2026l")
	s.out.WriteString(b.String())
}

func (s *Session) panelLines() []string {
	if s.panelW == 0 {
		return nil
	}
	var st string
	switch s.state {
	case "booting":
		st = sWarn.Render("● booting")
	case "running":
		st = sOK.Render("● running")
	default:
		st = sErr.Render("● stopped")
	}
	kv := func(k, v string) string { return " " + sKey.Render(k) + " " + v }
	sep := " " + sSep.Render(strings.Repeat("─", s.panelW-2))
	mem := fmt.Sprintf("%d MiB", s.info.MemoryMB)
	if s.info.MemoryMB >= 1024 && s.info.MemoryMB%1024 == 0 {
		mem = fmt.Sprintf("%d GiB", s.info.MemoryMB/1024)
	}
	up := fmtDur(time.Since(s.start).Round(time.Second))
	boot := s.bootTime
	if boot == "" {
		boot = "…"
	} else {
		boot += "s"
	}
	lines := []string{
		" " + sTitle.Render("llok") + "  " + st,
		"",
		kv("project", s.info.Project),
		kv("repo", shortPath(s.info.RepoHost, s.panelW-11)),
		kv("mount", s.info.RepoVM+sDim.Render(" (rw)")),
		kv("vm", fmt.Sprintf("%d vCPU · %s", s.info.CPUs, mem)),
		kv("kernel", s.info.Kernel),
		kv("rootfs", shortRootfs(s.rootfs)),
		kv("network", sOK.Render("none")),
		kv("uptime", up),
		kv("boot", boot),
		"",
		sep,
		" " + sDim.Render("keys"),
		kv("exit", "power off"),
		kv("Ctrl-] ×2", "force off"),
		"",
		sep,
		" " + sDim.Render("activity"),
	}
	avail := s.rows - len(lines)
	if avail > 0 {
		ev := s.events
		if len(ev) > avail {
			ev = ev[len(ev)-avail:]
		}
		for _, e := range ev {
			lines = append(lines, " "+e)
		}
	}
	return lines
}

// shortRootfs turns "alpine 3.22.6 · claude-code 2.1.92 · overlay, ..." into
// the part that fits on one panel row.
func shortRootfs(s string) string {
	if s == "" {
		return sDim.Render("…")
	}
	if i := strings.Index(s, " · overlay"); i >= 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "initramfs") {
		return "initramfs only"
	}
	return s
}

func fmtDur(d time.Duration) string {
	sec := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, (sec%3600)/60, sec%60)
}

func shortPath(p string, max int) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	if ansi.StringWidth(p) > max && max > 1 {
		p = "…" + p[len(p)-(max-1):]
	}
	return p
}
