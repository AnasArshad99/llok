//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
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
	Op   string `json:"op"`
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
	Code int    `json:"code,omitempty"` // op "exit": the session's exit status
}

// exitSock is a root-only unix socket; init reports the session's exit
// status here and the control agent relays it to the host. The agent user
// cannot write to it, so it cannot forge a status.
const exitSock = "/run/llok-exit.sock"

// sendExit is the "llok-guest exit N" subcommand.
func sendExit(args []string) {
	if len(args) < 1 {
		os.Exit(2)
	}
	code, err := strconv.Atoi(args[0])
	if err != nil {
		os.Exit(2)
	}
	c, err := net.Dial("unix", exitSock)
	if err != nil {
		os.Exit(1)
	}
	defer c.Close()
	json.NewEncoder(c).Encode(reply{Op: "exit", OK: true, Code: code})
}

// serveExitSocket accepts exit reports from root and forwards them to ch.
func serveExitSocket(ch chan<- reply) {
	os.Remove(exitSock)
	old := syscall.Umask(0o077)
	l, err := net.Listen("unix", exitSock)
	syscall.Umask(old)
	if err != nil {
		return
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var r reply
			if json.NewDecoder(c).Decode(&r) == nil && r.Op == "exit" {
				ch <- r
			}
		}()
	}
}

// controlLoop dials the host's control port and executes commands until the
// connection drops, then reconnects. It runs for the life of the VM.
func controlLoop(args []string) {
	fs := flag.NewFlagSet("control", flag.ExitOnError)
	port := fs.Uint("port", 3129, "host vsock control port")
	tty := fs.String("tty", "/dev/hvc0", "console tty to resize")
	fs.Parse(args)
	exits := make(chan reply, 4)
	go serveExitSocket(exits)
	for {
		c, err := vsock.Dial(vsock.Host, uint32(*port), nil)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		serveControl(c, *tty, exits)
		time.Sleep(200 * time.Millisecond)
	}
}

func serveControl(c net.Conn, tty string, exits <-chan reply) {
	defer c.Close()
	var encMu sync.Mutex
	enc := json.NewEncoder(c)
	send := func(r reply) error { encMu.Lock(); defer encMu.Unlock(); return enc.Encode(r) }
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case r := <-exits:
				send(r)
			}
		}
	}()
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
			send(r)
			unix.Sync()
			unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
			return
		default:
			r.OK, r.Err = false, "unknown op"
		}
		send(r)
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
