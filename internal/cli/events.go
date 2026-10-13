package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/events"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/session"
)

func newEventsCmd() *cobra.Command {
	var (
		name string
		raw  bool
		send string
	)
	cmd := &cobra.Command{
		Use:   "events [session]",
		Short: "Follow a running session's event feed",
		Long: `Connects to a running session (the one for the repo in the current
directory, or the only one running, or the named one) and prints each event
as it happens: file changes in the repo, broker decisions, supervisor rules,
VM state changes. --json prints the raw lines, one JSON object each, for
other programs. Each session listens on ~/.llok/run/<session>/events.sock;
any local program can read that socket directly.

--send pause|resume|kill acts on the session and exits.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := guest.Home()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				name = args[0]
			}
			name, err = resolveSession(home, name)
			if err != nil {
				return err
			}
			if send != "" {
				if send != "pause" && send != "resume" && send != "kill" {
					return errors.New("--send takes pause, resume or kill")
				}
				if err := sendControl(home, name, send); err != nil {
					return fmt.Errorf("%s: %w", send, err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), send+": ok")
				return nil
			}
			conn, sc, err := events.Dial(session.EventsSocket(home, name))
			if err != nil {
				return fmt.Errorf("session %s is not reachable", name)
			}
			defer conn.Close()
			out := cmd.OutOrStdout()
			for sc.Scan() {
				if raw {
					fmt.Fprintln(out, string(sc.Bytes()))
					continue
				}
				var ev events.Event
				if json.Unmarshal(sc.Bytes(), &ev) != nil {
					continue
				}
				fmt.Fprintln(out, describeEvent(ev))
			}
			fmt.Fprintln(os.Stderr, styleDim.Render("session ended"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&raw, "json", false, "print raw JSON lines")
	cmd.Flags().StringVar(&send, "send", "", "send a control command: pause, resume or kill")
	return cmd
}

func describeEvent(ev events.Event) string {
	ts := styleDim.Render(ev.Time.Format("15:04:05"))
	switch ev.Type {
	case events.TypeSession:
		switch ev.Phase {
		case "start":
			return fmt.Sprintf("%s session %s · %s · %s → %s (pid %d)", ts, ev.Phase, ev.Agent, ev.Repo, ev.VMRepo, ev.PID)
		case "end":
			code := "?"
			if ev.Exit != nil {
				code = fmt.Sprint(*ev.Exit)
			}
			why := ""
			if ev.StoppedBy != "" {
				why = " · stopped by the supervisor: " + ev.StoppedBy
			}
			return fmt.Sprintf("%s session end · exit %s · %dms%s", ts, code, ev.DurationMS, why)
		}
		return fmt.Sprintf("%s session %s", ts, ev.Phase)
	case events.TypeFile:
		return fmt.Sprintf("%s file %s %s", ts, ev.Op, ev.Path)
	case events.TypeNet:
		d := ev.Decision
		if ev.Decision == "deny" {
			d = styleErr.Render("deny")
		}
		return fmt.Sprintf("%s net %s %s", ts, d, ev.Host)
	case events.TypeRule:
		what := ev.Path
		if ev.Host != "" {
			what = ev.Host
		}
		return fmt.Sprintf("%s %s rule %s → %s (%s)", ts, styleWarn.Render("!"), ev.Rule, ev.Action, what)
	case events.TypeVM:
		return fmt.Sprintf("%s vm %s · %s", ts, ev.State, ev.Reason)
	case events.TypeControl:
		if ev.Reason != "" {
			return fmt.Sprintf("%s control %s failed: %s", ts, ev.Op2, ev.Reason)
		}
		return fmt.Sprintf("%s control %s", ts, ev.Op2)
	}
	return ts + " " + string(ev.Type)
}
