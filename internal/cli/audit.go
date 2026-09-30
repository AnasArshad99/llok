package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/broker"
	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/guest"
)

func newAuditCmd() *cobra.Command {
	var (
		project string
		all     bool
		since   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Summarise what the broker allowed and refused",
		Long: `Reads ~/.llok/audit/broker.jsonl and shows, per host, what the agent reached
and what was refused, with a ready-to-paste llok.yaml snippet for the refused
hosts you decide to permit. Defaults to the project in the current directory.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := guest.Home()
			if err != nil {
				return err
			}
			f, err := os.Open(filepath.Join(home, "audit", "broker.jsonl"))
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintln(os.Stderr, styleDim.Render("no audit log yet; run a session with network.mode: broker first"))
				return nil
			}
			if err != nil {
				return err
			}
			defer f.Close()

			if !all && project == "" {
				cfg, err := config.Load(".")
				if err != nil {
					return err
				}
				project = cfg.Name
			}
			var from time.Time
			if since > 0 {
				from = time.Now().Add(-since)
			}
			s := broker.Summarize(f, project, from)

			title := "llok audit · " + s.Project
			if s.Project == "" {
				title = "llok audit · all projects"
			}
			if s.Total == 0 {
				msg := "no events"
				if len(s.Projects) > 0 {
					msg += " for this project; known projects: " + strings.Join(s.Projects, ", ") + "  (use --project or --all)"
				}
				printBox(title, styleDim.Render(msg))
				return nil
			}
			window := fmt.Sprintf("%d requests · %s → %s", s.Total, s.From.Local().Format("Jan 2 15:04"), s.To.Local().Format("Jan 2 15:04"))
			printBox(title, window)

			printHosts("allowed", s.Allowed, styleOK)
			printHosts("refused", s.Denied, styleErr)
			if snippet := s.SuggestAllow(); snippet != "" {
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, styleDim.Render("to permit refused hosts, add to "+config.FileName+" and restart the VM:"))
				fmt.Fprint(os.Stderr, indent(snippet, "  "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project name (default: from ./llok.yaml)")
	cmd.Flags().BoolVar(&all, "all", false, "all projects")
	cmd.Flags().DurationVar(&since, "since", 0, "only events newer than this, e.g. 24h")
	return cmd
}

func printHosts(label string, hosts []broker.HostStat, style interface{ Render(...string) string }) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, style.Render(label)+styleDim.Render(fmt.Sprintf("  %d host(s)", len(hosts))))
	if len(hosts) == 0 {
		return
	}
	w := 0
	for _, h := range hosts {
		if len(h.Host) > w {
			w = len(h.Host)
		}
	}
	for _, h := range hosts {
		line := fmt.Sprintf("  %-*s  %5d  %s", w, h.Host, h.Count, styleDim.Render("last "+h.Last.Local().Format("Jan 2 15:04")))
		if codes := statusSummary(h.Statuses); codes != "" {
			line += "  " + styleDim.Render(codes)
		}
		fmt.Fprintln(os.Stderr, line)
	}
}

func statusSummary(m map[int]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d×%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}
