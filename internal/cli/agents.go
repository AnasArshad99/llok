package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/agents"
	"github.com/AnasArshad99/llok/internal/creds"
)

func newAgentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List the agents llok can run and whether their providers are signed in",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(os.Stderr, styleTitle.Render("agents")+styleDim.Render("  set one with  agent: <name>  in llok.yaml"))
			for _, name := range agents.Names() {
				a, _ := agents.Resolve(name, nil)
				var provs []string
				for i, p := range a.Providers {
					_, err := creds.Get(p)
					switch {
					case err == nil:
						provs = append(provs, ok(p))
					case i == 0:
						provs = append(provs, bad(p+" (llok auth "+p+")"))
					default:
						provs = append(provs, styleDim.Render(p))
					}
				}
				line := fmt.Sprintf("  %-12s %s", name, styleDim.Render(agents.Note(name)))
				if len(provs) > 0 {
					line += "\n               " + strings.Join(provs, "  ")
				}
				fmt.Fprintln(os.Stderr, line)
			}
			return nil
		},
	}
}

// agentPicker asks which agent to use, for first-run setup and for a
// session whose llok.yaml has no agent set.
func agentPicker(def string) (string, error) {
	var opts []option[string]
	idx := 0
	for i, name := range agents.Names() {
		label := name
		if note := agents.Note(name); note != "" {
			label += "  " + styleDim.Render(note)
		}
		opts = append(opts, option[string]{label, name})
		if name == def {
			idx = i
		}
	}
	if !isTTY() {
		return "", errors.New("no agent chosen; set agent: in llok.yaml")
	}
	return selectPrompt("Agent", "Which coding agent should this VM be set up for?", opts, idx)
}
