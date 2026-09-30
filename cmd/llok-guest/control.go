//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"net"
	"os"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

// command / reply mirror internal/control on the host.
type command struct {
	Op   string `json:"op"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type reply struct {
	Op  string `json:"op"`
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

// controlLoop dials the host's control port and executes commands until the
// connection drops, then reconnects. It runs for the life of the VM.
func controlLoop(args []string) {
	fs := flag.NewFlagSet("control", flag.ExitOnError)
	port := fs.Uint("port", 3129, "host vsock control port")
	tty := fs.String("tty", "/dev/hvc0", "console tty to resize")
	fs.Parse(args)
	for {
		c, err := vsock.Dial(vsock.Host, uint32(*port), nil)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		serveControl(c, *tty)
		time.Sleep(200 * time.Millisecond)
	}
}

func serveControl(c net.Conn, tty string) {
	defer c.Close()
	enc := json.NewEncoder(c)
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		var cmd command
		if err := json.Unmarshal(sc.Bytes(), &cmd); err != nil {
			continue
		}
		r := reply{Op: cmd.Op, OK: true}
		switch cmd.Op {
		case "ping":
		case "resize":
			if err := resizeTTY(tty, cmd.Cols, cmd.Rows); err != nil {
				r.OK, r.Err = false, err.Error()
			}
		case "shutdown":
			enc.Encode(r)
			unix.Sync()
			unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
			return
		default:
			r.OK, r.Err = false, "unknown op"
		}
		enc.Encode(r)
	}
}

// resizeTTY sets the console window size; the kernel then sends SIGWINCH to
// whatever is running on it (the shell, or the agent's TUI).
func resizeTTY(tty string, cols, rows int) error {
	f, err := os.OpenFile(tty, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.IoctlSetWinsize(int(f.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
}
