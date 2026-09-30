package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Minimal prompts on raw stdin. We deliberately avoid bubbletea/huh: they
// probe the terminal on import and that read on stdin fights the VM console.

var errAborted = errors.New("aborted")

var (
	stylePromptTitle = lipgloss.NewStyle().Bold(true)
	stylePromptDesc  = lipgloss.NewStyle().Foreground(colDim)
	styleCursor      = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleSelected    = lipgloss.NewStyle().Foreground(colAccent)
)

type option[T any] struct {
	Label string
	Value T
}

// selectPrompt renders a vertical list and returns the chosen value.
func selectPrompt[T any](title, desc string, opts []option[T], def int) (T, error) {
	var zero T
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return opts[def].Value, nil
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return zero, err
	}
	defer term.Restore(fd, old)

	cur := def
	lines := 0
	render := func() {
		if lines > 0 {
			fmt.Fprintf(os.Stderr, "\x1b[%dA\x1b[J", lines)
		}
		var b strings.Builder
		b.WriteString(stylePromptTitle.Render(title) + "\r\n")
		if desc != "" {
			b.WriteString(stylePromptDesc.Render(desc) + "\r\n")
		}
		for i, o := range opts {
			if i == cur {
				b.WriteString(styleCursor.Render("  ❯ ") + styleSelected.Render(o.Label) + "\r\n")
			} else {
				b.WriteString("    " + o.Label + "\r\n")
			}
		}
		b.WriteString(stylePromptDesc.Render("  ↑/↓ move · enter select · esc cancel") + "\r\n")
		s := b.String()
		lines = strings.Count(s, "\n")
		fmt.Fprint(os.Stderr, s)
	}
	render()

	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return zero, errAborted
		}
		for _, key := range splitKeys(buf[:n]) {
			switch key {
			case "\r", "\n":
				fmt.Fprintf(os.Stderr, "\x1b[%dA\x1b[J", lines)
				fmt.Fprintf(os.Stderr, "%s %s\r\n", stylePromptTitle.Render(title), styleSelected.Render(opts[cur].Label))
				return opts[cur].Value, nil
			case "\x1b[A", "k":
				if cur > 0 {
					cur--
				}
			case "\x1b[B", "j":
				if cur < len(opts)-1 {
					cur++
				}
			case "\x03", "\x1b", "q":
				fmt.Fprint(os.Stderr, "\r\n")
				return zero, errAborted
			}
		}
		render()
	}
}

// splitKeys breaks a raw read into individual keys: CSI escape sequences
// (ESC [ ... final byte) stay together, everything else is one byte each.
func splitKeys(b []byte) []string {
	var keys []string
	for i := 0; i < len(b); {
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '[' {
			j := i + 2
			for j < len(b) && !(b[j] >= 0x40 && b[j] <= 0x7e) {
				j++
			}
			if j < len(b) {
				keys = append(keys, string(b[i:j+1]))
				i = j + 1
				continue
			}
		}
		keys = append(keys, string(b[i:i+1]))
		i++
	}
	return keys
}

// inputPrompt reads one line, returning def when the user just presses enter.
func inputPrompt(title, desc, def string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return def, nil
	}
	fmt.Fprintln(os.Stderr, stylePromptTitle.Render(title))
	if desc != "" {
		fmt.Fprintln(os.Stderr, stylePromptDesc.Render(desc))
	}
	fmt.Fprintf(os.Stderr, "  %s ", styleCursor.Render("❯")+stylePromptDesc.Render(" ["+def+"]"))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", errAborted
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	fmt.Fprintf(os.Stderr, "\x1b[1A\x1b[J%s %s\n", stylePromptTitle.Render(title), styleSelected.Render(line))
	return line, nil
}
