package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// Never let the renderer query the terminal (OSC 11 background probe): that
// read on stdin would swallow keystrokes meant for the VM console.
func init() {
	r := lipgloss.DefaultRenderer()
	if isTTY() {
		r.SetColorProfile(termenv.ANSI256)
	} else {
		r.SetColorProfile(termenv.Ascii)
	}
	r.SetHasDarkBackground(true)
}

var (
	colAccent = lipgloss.Color("#B57BEE") // llok purple
	colDim    = lipgloss.Color("#8A8A8A")
	colOK     = lipgloss.Color("#5AD48A")
	colWarn   = lipgloss.Color("#F2C14E")
	colErr    = lipgloss.Color("#F26D6D")

	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleDim   = lipgloss.NewStyle().Foreground(colDim)
	styleOK    = lipgloss.NewStyle().Foreground(colOK)
	styleWarn  = lipgloss.NewStyle().Foreground(colWarn)
	styleErr   = lipgloss.NewStyle().Foreground(colErr)
	styleKey   = lipgloss.NewStyle().Foreground(colDim).Width(9)
	styleBox   = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent).
			Padding(0, 1)
)

func isTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// kv renders aligned "key  value" rows.
func kv(rows [][2]string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(styleKey.Render(r[0]) + " " + r[1] + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func box(title, body string) string {
	return styleBox.Render(styleTitle.Render(title) + "\n" + body)
}

func printBox(title, body string) {
	if !isTTY() {
		fmt.Fprintf(os.Stderr, "%s\n%s\n", title, body)
		return
	}
	fmt.Fprintln(os.Stderr, box(title, body))
}

func ok(s string) string   { return styleOK.Render("✓ " + s) }
func warn(s string) string { return styleWarn.Render("! " + s) }
func bad(s string) string  { return styleErr.Render("✗ " + s) }
