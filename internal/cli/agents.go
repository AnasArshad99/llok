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

// agentPicker asks which agents to set the VM up for. Several may be
// chosen; the first in list order becomes the default.
func agentPicker(def []string) ([]string, error) {
	var opts []option[string]
	var checked []bool
	for _, name := range agents.Names() {
		if name == "shell" {
			continue
		}
		label := name
		if note := agents.Note(name); note != "" {
			label += "  " + styleDim.Render(note)
		}
		opts = append(opts, option[string]{label, name})
		on := false
		for _, d := range def {
			if d == name {
				on = true
			}
		}
		checked = append(checked, on)
	}
	if len(def) == 0 {
		checked[0] = true
	}
	if !isTTY() {
		return nil, errors.New("no agent chosen; set agent: in llok.yaml")
	}
	return multiPrompt("Agents", "Which coding agents should this VM be set up for? (space to toggle; you can run any of them inside)", opts, checked)
}
