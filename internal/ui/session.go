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
	uv "github.com/charmbracelet/ultraviolet"
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
	Session   string    // session name, when the VM runs detached from this terminal
	Started   time.Time // when the VM started (zero: now)
	Others    int       // other sessions running on this machine
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
	hidden   int // paths actually covered inside the guest
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

	sel  selection
	copy func(string) error // clipboard writer; replaced in tests
}

// selection is a click-drag range in console-pane cells of the current
// view (live screen or scrolled-back history).
type selection struct {
	active   bool
	dragging bool
	ax, ay   int // anchor (where the button went down)
	bx, by   int // current end
}

// bounds returns the selection as (top-left, bottom-right) in reading order.
func (sl selection) bounds() (sx, sy, ex, ey int) {
	sx, sy, ex, ey = sl.ax, sl.ay, sl.bx, sl.by
	if sy > ey || (sy == ey && sx > ex) {
		sx, sy, ex, ey = ex, ey, sx, sy
	}
	return
}

// contains reports whether cell (x, y) is inside the selection.
func (sl selection) contains(x, y int) bool {
	if !sl.active {
		return false
	}
	sx, sy, ex, ey := sl.bounds()
	switch {
	case y < sy || y > ey:
		return false
	case y == sy && y == ey:
		return x >= sx && x <= ex
	case y == sy:
		return x >= sx
	case y == ey:
		return x <= ex
	}
	return true
}

func (s *Session) selectStart(x, y int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x >= s.consoleW || y >= s.rows {
		s.sel = selection{}
		s.dirty = true
		return // clicks on the panel clear any selection
	}
	s.sel = selection{active: true, dragging: true, ax: x, ay: y, bx: x, by: y}
	s.dirty = true
}

func (s *Session) selectExtend(x, y int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sel.dragging {
		return
	}
	s.sel.bx, s.sel.by = clamp(x, 0, s.consoleW-1), clamp(y, 0, s.rows-1)
	s.dirty = true
}

// selectFinish ends a drag and copies the selected text. A click with no
// drag just clears the selection.
func (s *Session) selectFinish(x, y int) {
	s.mu.Lock()
	if !s.sel.dragging {
		s.mu.Unlock()
		return
	}
	s.sel.dragging = false
	s.sel.bx, s.sel.by = clamp(x, 0, s.consoleW-1), clamp(y, 0, s.rows-1)
	if s.sel.ax == s.sel.bx && s.sel.ay == s.sel.by {
		s.sel = selection{}
		s.dirty = true
		s.mu.Unlock()
		return
	}
	text := s.selectedTextLocked()
	s.dirty = true
	s.mu.Unlock()
	if strings.TrimSpace(text) == "" {
		return
	}
	cp := s.copy
	if cp == nil {
		cp = copyToClipboard
	}
	if err := cp(text); err != nil {
		s.event("! copy failed: " + err.Error())
		return
	}
	s.event(fmt.Sprintf("copied %d chars", len([]rune(text))))
}

func (s *Session) clearSelection() {
	s.mu.Lock()
	if s.sel.active {
		s.sel = selection{}
		s.dirty = true
	}
	s.mu.Unlock()
}

// cellText returns the text of one cell in the current view, "" for a
// wide-character continuation or out of range.
func (s *Session) cellText(x, y int) string {
	n := s.emu.ScrollbackLen()
	idx := n - s.scroll + y // absolute line index in scrollback+screen
	var c *uv.Cell
	if idx < n {
		c = s.emu.ScrollbackCellAt(x, idx)
	} else {
		c = s.emu.CellAt(x, idx-n)
	}
	if c == nil || c.Width == 0 {
		return ""
	}
	if c.Content == "" {
		return " "
	}
	return c.Content
}

// selectedTextLocked joins the selected cells, one line per row, trailing
// spaces trimmed.
func (s *Session) selectedTextLocked() string {
	sx, sy, ex, ey := s.sel.bounds()
	var lines []string
	for y := sy; y <= ey; y++ {
		from, to := 0, s.consoleW-1
		if y == sy {
			from = sx
		}
		if y == ey {
			to = ex
		}
		var b strings.Builder
		for x := from; x <= to; x++ {
			b.WriteString(s.cellText(x, y))
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// highlightedRow re-renders one console row from its cells with the
// selected range in reverse video.
func (s *Session) highlightedRow(y int) string {
	var b strings.Builder
	n := s.emu.ScrollbackLen()
	idx := n - s.scroll + y
	for x := 0; x < s.consoleW; x++ {
		var c *uv.Cell
		if idx < n {
			c = s.emu.ScrollbackCellAt(x, idx)
		} else {
			c = s.emu.CellAt(x, idx-n)
		}
		if c == nil || c.Width == 0 {
			continue
		}
		text := c.Content
		if text == "" {
			text = " "
		}
		if s.sel.contains(x, y) {
			b.WriteString("\x1b[7m" + text + "\x1b[27m")
		} else {
			b.WriteString(c.Style.Styled(text))
		}
	}
	return b.String()
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Option is one answer a prompt offers.
type Option struct {
	Key   byte   `json:"key"`
	Label string `json:"label"`
	Show  string `json:"show,omitempty"` // how the key is displayed, when not the key itself
}

type prompt struct {
	question string
	opts     []Option
	answer   chan byte
	menu     bool // any other key dismisses it (answer 0)
}

// Ask shows a question in the panel, rings the bell, and blocks until one
// of the option keys is pressed. Keys are not forwarded to the guest while
// a prompt is showing.
func (s *Session) Ask(question string, opts []Option) byte {
	return s.ask(question, opts, false)
}

// Menu is a prompt any other key dismisses; it returns 0 then.
func (s *Session) Menu(question string, opts []Option) byte {
	return s.ask(question, opts, true)
}

// CancelPrompt dismisses the current prompt, if any, with answer 0.
func (s *Session) CancelPrompt() {
	s.mu.Lock()
	p := s.prompt
	s.mu.Unlock()
	if p != nil {
		select {
		case p.answer <- 0:
		default:
		}
	}
}

func (s *Session) ask(question string, opts []Option, menu bool) byte {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	p := &prompt{question: question, opts: opts, answer: make(chan byte, 1), menu: menu}
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
	if p.menu {
		select {
		case p.answer <- 0:
		default:
		}
	}
	return true
}

// SetState sets the VM state shown in the panel (booting, running, paused,
// stopped), for a client that attaches to a session already under way.
func (s *Session) SetState(state string) {
	s.mu.Lock()
	s.state = state
	s.dirty = true
	s.mu.Unlock()
}

// SetOthers updates the count of other running sessions.
func (s *Session) SetOthers(n int) {
	s.mu.Lock()
	s.info.Others = n
	s.dirty = true
	s.mu.Unlock()
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
	if !info.Started.IsZero() {
		s.start = info.Started
	}
	s.layout()
	s.emu = vt.NewSafeEmulator(s.consoleW, s.rows)
	return s
}

// TermSize is the host terminal's size, with a sane fallback.
func TermSize() (cols, rows int) { return termSize() }

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
	fmt.Fprint(s.out, termReset+"\x1b[?1049h"+termReset+"\x1b[?25l\x1b[2J\x1b[H"+mouseOn)
	go s.loop()
}

// termReset clears scroll margins, origin mode and insert mode, and turns
// autowrap on. It does not touch the alternate-screen state.
const termReset = "\x1b[r\x1b[?6l\x1b[4l\x1b[?7h"

// Stop restores the terminal.
func (s *Session) Stop() {
	close(s.stop)
	<-s.done
	fmt.Fprint(s.out, mouseOff+termReset+"\x1b[?25h\x1b[?1049l"+termReset)
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
		if i := strings.Index(m, "hidden="); i >= 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(m[i+7:])); err == nil {
				s.hidden = n
				if n > 0 {
					s.eventLocked(fmt.Sprintf("%d path(s) hidden from the agent", n))
				}
			}
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

// EventAt appends an activity line stamped with when it happened, for
// lines replayed from a session that started before this terminal joined.
func (s *Session) EventAt(at time.Time, e string) {
	s.mu.Lock()
	s.eventAtLocked(at, e)
	s.mu.Unlock()
}

func (s *Session) eventLocked(e string) { s.eventAtLocked(time.Now(), e) }

func (s *Session) eventAtLocked(at time.Time, e string) {
	ts := at.Sub(s.start).Round(time.Second)
	if ts < 0 {
		ts = 0
	}
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
	if s.sel.active {
		_, sy, _, ey := s.sel.bounds()
		for y := sy; y <= ey && y < len(lines); y++ {
			if y >= 0 {
				lines[y] = s.highlightedRow(y)
			}
		}
	}
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
	lines = append(lines, sep)
	if s.info.Session != "" {
		ses := s.info.Session
		if s.info.Others > 0 {
			ses += sDim.Render(fmt.Sprintf(" +%d more", s.info.Others))
		}
		lines = append(lines, kv("session", ses))
	}
	lines = append(lines,
		kv("project", s.info.Project),
		kv("agent", s.info.Agent),
		kv("repo", shortPath(s.info.RepoHost, s.panelW-11)),
		kv("mount", s.info.RepoVM+sDim.Render(" rw")+s.hiddenDesc()),
		kv("vm", fmt.Sprintf("%d vCPU · %s", s.info.CPUs, mem)),
		kv("kernel", s.info.Kernel),
		kv("rootfs", shortRootfs(s.rootfs)),
		kv("network", s.networkDesc()),
		kv("uptime", fmtDur(time.Since(s.start).Round(time.Second))),
		kv("boot", boot),
		sep,
		kv("exit", "power off"),
	)
	if s.info.Session != "" {
		lines = append(lines,
			kv("C-]", "d detach · s switch"),
			kv("", "n new · K kill · C-] off"),
		)
	} else {
		lines = append(lines, kv("C-] ×2", "force off"))
	}
	lines = append(lines,
		kv("wheel", "scroll history"),
		kv("drag", "select · copies"),
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

func (s *Session) hiddenDesc() string {
	if s.hidden > 0 {
		return sWarn.Render(fmt.Sprintf(" · %d hidden", s.hidden))
	}
	return ""
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
		show := o.Show
		if show == "" {
			show = string(o.Key)
		}
		keys = append(keys, fmt.Sprintf("[%s] %s", show, o.Label))
	}
	for _, l := range wrap(strings.Join(keys, "  "), w-2) {
		out = append(out, " "+box.Render(" "+l))
	}
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
