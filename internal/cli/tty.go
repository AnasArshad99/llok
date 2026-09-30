package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// saneTerminal repairs a terminal that a killed full-screen program left in
// raw mode, the same way `stty sane` does, so our own output prints normally.
// It only touches flags that raw mode clears; a healthy terminal is untouched.
func saneTerminal() {
	fd := int(os.Stdin.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return // not a terminal
	}
	if t.Lflag&unix.ICANON != 0 && t.Oflag&unix.OPOST != 0 {
		return // already cooked
	}
	t.Iflag |= unix.ICRNL | unix.BRKINT | unix.IXON
	t.Oflag |= unix.OPOST | unix.ONLCR
	t.Lflag |= unix.ICANON | unix.ECHO | unix.ECHOE | unix.ISIG | unix.IEXTEN
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	_ = unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
	// Also leave the alternate screen in case it was never exited.
	os.Stdout.WriteString("\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[?1049l")
}
