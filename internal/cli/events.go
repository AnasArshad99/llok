package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/events"
	"github.com/AnasArshad99/llok/internal/guest"
)

func newEventsCmd() *cobra.Command {
	var (
		project string
		raw     bool
		send    string
	)
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Follow a running session's event feed",
		Long: `Connects to the live session for the project in the current directory (or
--project) and prints each event as it happens: file changes in the repo,
broker decisions, supervisor rules, VM state changes. --json prints the raw
lines, one JSON object each, for other programs. Each session listens on
~/.llok/run/<project>.sock; any local program can read that socket directly.

--send pause|resume|kill acts on the session and exits.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := guest.Home()
			if err != nil {
				return err
			}
			if project == "" {
				project, err = projectHere(home)
				if err != nil {
					return err
				}
			}
			path := events.SocketPath(home, project)
			conn, sc, err := events.Dial(path)
			if err != nil {
				return fmt.Errorf("no running session for %q (%s)", project, path)
			}
			defer conn.Close()
			if send != "" {
				if send != "pause" && send != "resume" && send != "kill" {
					return errors.New("--send takes pause, resume or kill")
				}
				b, _ := json.Marshal(events.Command{Op: send})
				if _, err := conn.Write(append(b, '\n')); err != nil {
					return err
				}
				// Wait for the session's answer so an error is reported.
				for sc.Scan() {
					var ev events.Event
					if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Type == events.TypeControl && ev.Op2 == send {
						if ev.Reason != "" {
							return errors.New(send + ": " + ev.Reason)
						}
						fmt.Fprintln(cmd.OutOrStdout(), send+": ok")
						return nil
					}
				}
				return errors.New("session closed before answering")
			}
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
	cmd.Flags().StringVar(&project, "project", "", "project name (default: the llok.yaml in this directory)")
	cmd.Flags().BoolVar(&raw, "json", false, "print raw JSON lines")
	cmd.Flags().StringVar(&send, "send", "", "send a control command: pause, resume or kill")
	return cmd
}

// projectHere names the project from llok.yaml in the working directory,
// or the only running session when there is exactly one.
func projectHere(home string) (string, error) {
	if cfg, err := config.Load("."); err == nil && cfg.Name != "" {
		if _, err := os.Stat(config.FileName); err == nil {
			return cfg.Name, nil
		}
	}
	socks, _ := filepath.Glob(filepath.Join(home, "run", "*.sock"))
	var live []string
	for _, s := range socks {
		if c, _, err := events.Dial(s); err == nil {
			c.Close()
			live = append(live, strings.TrimSuffix(filepath.Base(s), ".sock"))
		}
	}
	switch len(live) {
	case 0:
		return "", errors.New("no running session; run from a repo with llok.yaml or pass --project")
	case 1:
		return live[0], nil
	}
	return "", fmt.Errorf("several sessions are running (%s); pass --project", strings.Join(live, ", "))
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
