package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/events"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/session"
)

func newSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List running sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := guest.Home()
			if err != nil {
				return err
			}
			list := session.List(home)
			if len(list) == 0 {
				fmt.Fprintln(os.Stderr, styleDim.Render("no sessions running; llok starts one"))
				return nil
			}
			var rows [][2]string
			for _, r := range list {
				mode := styleDim.Render("llok attach " + r.Name)
				if !r.Attach {
					mode = styleDim.Render("foreground in another terminal")
				}
				rows = append(rows, [2]string{r.Name, fmt.Sprintf("%s · %s · up %s · %d vCPU %d MiB · %s",
					shortHome(r.Repo), r.Agent, fmtUptime(r), r.CPUs, r.MemoryMB, mode)})
			}
			fmt.Print(strings.TrimRight(kv(rows), "\n") + "\n")
			return nil
		},
	}
}

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach [session]",
		Short: "Attach to a running session",
		Long: `Attaches this terminal to a detached session. With no name, the session
for the repo in the current directory, or the only one running.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := guest.Home()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			name, err = resolveSession(home, name)
			if err != nil {
				return err
			}
			r, _ := session.Find(home, name)
			if !r.Attach {
				return fmt.Errorf("session %s runs in the foreground of another terminal and cannot be attached", name)
			}
			if !isTTY() {
				return errors.New("attach needs a terminal")
			}
			return attachLoop(home, name)
		},
	}
}

func newKillCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "kill [session]",
		Short: "Stop a running session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := guest.Home()
			if err != nil {
				return err
			}
			var names []string
			switch {
			case all:
				for _, r := range session.List(home) {
					names = append(names, r.Name)
				}
				if len(names) == 0 {
					fmt.Fprintln(os.Stderr, styleDim.Render("no sessions running"))
					return nil
				}
			default:
				name := ""
				if len(args) == 1 {
					name = args[0]
				}
				name, err = resolveSession(home, name)
				if err != nil {
					return err
				}
				names = []string{name}
			}
			for _, n := range names {
				if err := sendControl(home, n, "kill"); err != nil {
					return fmt.Errorf("%s: %w", n, err)
				}
				fmt.Fprintln(os.Stderr, styleTitle.Render("llok")+" · killed "+n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "stop every running session")
	return cmd
}

// resolveSession picks a session: the named one, else the one for the
// repo in the current directory, else the only one running.
func resolveSession(home, name string) (string, error) {
	if name != "" {
		if _, ok := session.Find(home, name); !ok {
			return "", fmt.Errorf("no running session named %s (llok sessions lists them)", name)
		}
		return name, nil
	}
	if cwd, err := os.Getwd(); err == nil {
		if abs, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = abs
		}
		if here := session.ForRepo(home, cwd); len(here) == 1 {
			return here[0].Name, nil
		} else if len(here) > 1 {
			return "", fmt.Errorf("several sessions run for this repo (%s); name one", joinNames(here))
		}
	}
	all := session.List(home)
	switch len(all) {
	case 0:
		return "", errors.New("no session is running")
	case 1:
		return all[0].Name, nil
	}
	return "", fmt.Errorf("several sessions are running (%s); name one", joinNames(all))
}

func joinNames(rs []session.Record) string {
	var n []string
	for _, r := range rs {
		n = append(n, r.Name)
	}
	return strings.Join(n, ", ")
}

// sendControl sends one control line over a session's events socket and
// waits for the acknowledgement.
func sendControl(home, name, op string) error {
	conn, sc, err := events.Dial(session.EventsSocket(home, name))
	if err != nil {
		return errors.New("session not reachable")
	}
	defer conn.Close()
	b, _ := json.Marshal(events.Command{Op: op})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	for sc.Scan() {
		var ev events.Event
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Type == events.TypeControl && ev.Op2 == op {
			if ev.Reason != "" {
				return errors.New(ev.Reason)
			}
			return nil
		}
	}
	return errors.New("session closed before answering")
}

func shortHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
