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
	Agent     string   // agent name shown in the panel
	Providers []string // providers with credentials for this session
	Network   string   // "none" or "broker"
	Allow     int      // extra allowed hosts
	Project   string
	RepoHost  string
	RepoVM    string
	CPUs      uint
	MemoryMB  uint64
	Kernel    string
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
	scroll   int // lines scrolled back from the live view (0 = live)

	stop chan struct{}
	done chan struct{}

	// OnResize is called (outside the lock) when the console pane changes size.
	OnResize func(cols, rows int)

	promptMu sync.Mutex // serialises prompts
	prompt   *prompt
}

// Option is one answer a prompt offers.
type Option struct {
	Key   byte
	Label string
}

type prompt struct {
	question string
	opts     []Option
	answer   chan byte
}

// Ask shows a question in the panel, rings the bell, and blocks until one
// of the option keys is pressed. Keys are not forwarded to the guest while
// a prompt is showing.
func (s *Session) Ask(question string, opts []Option) byte {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	p := &prompt{question: question, opts: opts, answer: make(chan byte, 1)}
	s.mu.Lock()
	s.prompt = p
	s.dirty = true
	s.mu.Unlock()
	s.out.WriteString("\a")
	k := <-p.answer
	s.mu.Lock()
	s.prompt = nil
	s.dirty = true
	s.mu.Unlock()
	return k
}

// promptKey feeds a keypress to the active prompt. It reports whether the
// key was consumed (any key is swallowed while a prompt is up).
func (s *Session) promptKey(b byte) bool {
	s.mu.Lock()
	p := s.prompt
	s.mu.Unlock()
	if p == nil {
		return false
	}
	for _, o := range p.opts {
		if o.Key == b {
			select {
			case p.answer <- o.Key:
			default:
			}
			return true
		}
	}
	return true
}

func (s *Session) hasPrompt() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prompt != nil
}

// SetPaused reflects the VM's paused state in the panel.
func (s *Session) SetPaused(v bool) {
	s.mu.Lock()
	if v {
		s.state = "paused"
	} else if s.state == "paused" {
		s.state = "running"
	}
	s.dirty = true
	s.mu.Unlock()
}

const (
	minPanelCols = 90 // below this width the panel is hidden
	panelWidth   = 32
)

var (
	colAccent = lipgloss.Color("#B57BEE")
	colDim    = lipgloss.Color("#7C7C86")
	colFaint  = lipgloss.Color("#4A4A52")
	colOK     = lipgloss.Color("#5AD48A")
	colWarn   = lipgloss.Color("#F2C14E")
	colErr    = lipgloss.Color("#F26D6D")
	colText   = lipgloss.Color("#C8C8D0")

	sAccent = lipgloss.NewStyle().Foreground(colAccent)
	sDim    = lipgloss.NewStyle().Foreground(colDim)
	sFaint  = lipgloss.NewStyle().Foreground(colFaint)
	sText   = lipgloss.NewStyle().Foreground(colText)
	sKey    = lipgloss.NewStyle().Foreground(colDim).Width(8).MaxWidth(8)
	sOK     = lipgloss.NewStyle().Foreground(colOK)
	sWarn   = lipgloss.NewStyle().Foreground(colWarn)
	sErr    = lipgloss.NewStyle().Foreground(colErr)
	sSep    = lipgloss.NewStyle().Foreground(colFaint)
)

// logo is the llok wordmark shown at the top of the panel.
var logo = []string{
	"██  ██   ██████  ██  ██",
	"██  ██  ██    ██  ████ ",
	"██  ██  ██    ██  ██ ██",
	"██████   ██████  ██  ██",
}

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

func termSize() (cols, rows int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols < 20 || rows < 5 {
		return 120, 40
	}
	return cols, rows
}

func (s *Session) layout() {
	s.cols, s.rows = termSize()
	if s.cols >= minPanelCols {
		s.panelW = panelWidth
		s.consoleW = s.cols - panelWidth - 1
	} else {
		s.panelW = 0
		s.consoleW = s.cols
	}
}

// ConsoleSize is the size the guest terminal should be told it has.
func (s *Session) ConsoleSize() (cols, rows int) { return s.consoleW, s.rows }

// Start takes over the terminal and begins rendering.
//
// Before switching to the alternate screen we reset the terminal's
// scroll region and origin mode. A previous full-screen program (Claude
// Code's own UI sets a scroll region) can leave them behind, and every
// absolute cursor move would then land inside that strip at the bottom
// of the window instead of on a clean full screen.
func (s *Session) Start() {
	fmt.Fprint(s.out, termReset+"\x1b[?1049h"+termReset+"\x1b[?25l\x1b[2J\x1b[H")
	s.event("vm starting")
	go s.loop()
}

// termReset clears scroll margins, origin mode and insert mode, and turns
// autowrap on. It does not touch the alternate-screen state.
const termReset = "\x1b[r\x1b[?6l\x1b[4l\x1b[?7h"

// Stop restores the terminal.
func (s *Session) Stop() {
	close(s.stop)
	<-s.done
	fmt.Fprint(s.out, termReset+"\x1b[?25h\x1b[?1049l"+termReset)
}

// Write feeds guest console bytes into the emulator.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	n, err := s.emu.Write(p)
	s.dirty = true
	s.mu.Unlock()
	return n, err
}

func (s *Session) scrollBy(n int) {
	s.mu.Lock()
	s.setScrollLocked(s.scroll + n)
	s.mu.Unlock()
}

func (s *Session) scrollTo(n int) {
	s.mu.Lock()
	if s.scroll != n {
		s.setScrollLocked(n)
	}
	s.mu.Unlock()
}

func (s *Session) setScrollLocked(n int) {
	max := s.emu.ScrollbackLen()
	if n > max {
		n = max
	}
	if n < 0 {
		n = 0
	}
	if n != s.scroll {
		s.scroll = n
		s.dirty = true
	}
}

// consoleLines returns exactly s.rows rendered lines for the console pane,
// taking the scrollback offset into account.
func (s *Session) consoleLines() []string {
	screen := strings.Split(s.emu.Render(), "\n")
	for len(screen) < s.rows {
		screen = append(screen, "")
	}
	screen = screen[:s.rows]
	if s.scroll == 0 {
		return screen
	}
	var all []string
	for _, l := range s.emu.Scrollback().Lines() {
		all = append(all, l.Render())
	}
	n := len(all)
	if s.scroll > n {
		s.scroll = n
	}
	all = append(all, screen...)
	start := n - s.scroll
	return all[start : start+s.rows]
}

// Replies returns the stream of answers the emulator generates for terminal
// queries the guest makes (cursor position, device attributes). It must be
// drained into the guest's input or the emulator blocks.
func (s *Session) Replies() io.Reader { return s.emu }

// Marker handles control markers the guest sends out of band.
func (s *Session) Marker(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasPrefix(m, "LLOK_INFO"):
		if i := strings.Index(m, "rootfs="); i >= 0 {
			s.rootfs = clean(m[i+7:])
		}
	case strings.HasPrefix(m, "LLOK_READY"):
		s.state = "running"
		if i := strings.Index(m, "uptime="); i >= 0 {
			s.bootTime = strings.TrimSuffix(m[i+7:], "s")
		}
		s.eventLocked("guest ready in " + s.bootTime + "s")
		s.eventLocked("repo mounted at " + s.info.RepoVM)
	case strings.HasPrefix(m, "LLOK_EXIT"):
		if c, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(m, "LLOK_EXIT"))); err == nil {
			s.exitCode = c
		}
		s.state = "stopped"
		s.eventLocked("guest powering off")
	}
	s.dirty = true
}

// Event appends a line to the activity log (broker decisions, etc).
func (s *Session) Event(e string) { s.event(e) }

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

// clean strips control characters and escapes from text that may originate
// in the guest (file names, hosts, markers) before it is drawn on the host
// terminal outside the emulator.
func clean(t string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x9b || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, t)
}

func (s *Session) eventLocked(e string) {
	ts := time.Since(s.start).Round(time.Second)
	s.events = append(s.events, sFaint.Render(fmtDur(ts))+" "+sDim.Render(clean(e)))
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

	// Re-detect the terminal size each frame: some hosts report the wrong
	// size at startup and never send SIGWINCH, which would leave the UI
	// smaller than the screen and looking scrollable.
	if cols, rows := termSize(); cols != s.cols || rows != s.rows {
		s.layout()
		s.emu.Resize(s.consoleW, s.rows)
		s.out.WriteString("\x1b[2J")
		force = true
		if s.OnResize != nil {
			go s.OnResize(s.consoleW, s.rows)
		}
	}
	if !force && !s.dirty {
		return
	}
	s.dirty = false

	lines := s.consoleLines()
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
	if s.scroll > 0 {
		tag := sWarn.Render(fmt.Sprintf(" ↑ %d lines · scroll or type to return ", s.scroll))
		fmt.Fprintf(&b, "\x1b[1;%dH%s\x1b[m", max(1, s.consoleW-ansi.StringWidth(tag)+1), tag)
	}
	cur := s.emu.CursorPosition()
	if s.scroll == 0 && s.cursorOn && cur.X < s.consoleW && cur.Y < s.rows {
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
	var dot, word string
	switch s.state {
	case "booting":
		dot, word = sWarn.Render("●"), sWarn.Render("booting")
	case "running":
		dot, word = sOK.Render("●"), sOK.Render("running")
	case "paused":
		dot, word = sWarn.Render("●"), sWarn.Render("paused")
	default:
		dot, word = sErr.Render("●"), sErr.Render("stopped")
	}
	kv := func(k, v string) string { return " " + sKey.Render(k) + " " + sText.Render(v) }
	sep := " " + sSep.Render(strings.Repeat("─", s.panelW-2))

	mem := fmt.Sprintf("%d MiB", s.info.MemoryMB)
	if s.info.MemoryMB >= 1024 && s.info.MemoryMB%1024 == 0 {
		mem = fmt.Sprintf("%d GiB", s.info.MemoryMB/1024)
	}
	boot := "…"
	if s.bootTime != "" {
		boot = s.bootTime + "s"
	}

	var lines []string
	lines = append(lines, "")
	for _, l := range logo {
		lines = append(lines, " "+sAccent.Render(l))
	}
	lines = append(lines,
		"",
		" "+dot+" "+word,
	)
	if s.prompt != nil {
		lines = append(lines, s.promptLines()...)
	}
	lines = append(lines,
		sep,
		kv("project", s.info.Project),
		kv("agent", s.info.Agent),
		kv("repo", shortPath(s.info.RepoHost, s.panelW-11)),
		kv("mount", s.info.RepoVM+sDim.Render(" rw")),
		kv("vm", fmt.Sprintf("%d vCPU · %s", s.info.CPUs, mem)),
		kv("kernel", s.info.Kernel),
		kv("rootfs", shortRootfs(s.rootfs)),
		kv("network", s.networkDesc()),
		kv("uptime", fmtDur(time.Since(s.start).Round(time.Second))),
		kv("boot", boot),
		sep,
		kv("exit", "power off"),
		kv("C-] ×2", "force off"),
	)
	lines = append(lines,
		sep,
		" "+sDim.Render("activity"),
	)
	avail := s.rows - len(lines)
	if avail > 0 && len(s.events) > 0 {
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

func (s *Session) networkDesc() string {
	if s.info.Network != "broker" {
		return sWarn.Render("none") + sDim.Render(" · agent cannot sign in")
	}
	d := sAccent.Render("broker")
	if len(s.info.Providers) > 0 {
		d += sDim.Render(" · " + strings.Join(s.info.Providers, ", "))
	}
	if s.info.Allow > 0 {
		d += sDim.Render(fmt.Sprintf(" +%d", s.info.Allow))
	}
	return d
}

// promptLines renders the active question as a highlighted block.
func (s *Session) promptLines() []string {
	w := s.panelW - 2
	box := lipgloss.NewStyle().Foreground(lipgloss.Color("#1e2030")).Background(colWarn).Width(w)
	var out []string
	for _, l := range wrap(s.prompt.question, w-2) {
		out = append(out, " "+box.Render(" "+l))
	}
	var keys []string
	for _, o := range s.prompt.opts {
		keys = append(keys, fmt.Sprintf("[%c] %s", o.Key, o.Label))
	}
	out = append(out, " "+box.Render(" "+strings.Join(keys, "  ")))
	return out
}

func wrap(s string, w int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line != "" && ansi.StringWidth(line)+1+ansi.StringWidth(word) > w {
				lines = append(lines, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		lines = append(lines, line)
	}
	return lines
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

// shortRootfs turns "alpine 3.22.6 · claude-code 2.1.92 · overlay, ..." into
// the part that fits on one panel row.
func shortRootfs(s string) string {
	if s == "" {
		return "…"
	}
	if i := strings.Index(s, " · overlay"); i >= 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "initramfs") {
		return "initramfs only"
	}
	// Keep just the distro version; the full string lives nowhere else needed.
	if i := strings.Index(s, " · "); i >= 0 {
		return s[:i]
	}
	return s
}
