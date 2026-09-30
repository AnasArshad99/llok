package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/guard"
)

func newRulesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rules",
		Short: "Show the supervisor rules in effect for this repo",
		Long: `Lists the built-in rules merged with the rules: section of ./llok.yaml.
Each line shows where the rule comes from, what it watches, and what it does.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			defaults := map[string]guard.Rule{}
			for _, r := range guard.Defaults() {
				defaults[r.Name] = r
			}
			local := map[string]bool{}
			for _, r := range cfg.Rules {
				local[r.Name] = true
			}
			rules := cfg.GuardRules()

			printBox("llok rules · "+cfg.Name, fmt.Sprintf("%d in effect · built-in defaults + %s", len(rules), config.FileName))
			fmt.Fprintln(os.Stderr)
			for _, r := range rules {
				src := styleDim.Render("built-in ")
				if local[r.Name] {
					if _, isDefault := defaults[r.Name]; isDefault {
						src = styleWarn.Render("override ")
					} else {
						src = styleOK.Render("llok.yaml")
					}
				}
				fmt.Fprintf(os.Stderr, "  %s  %-14s %s\n", src, r.Name, describe(r))
				if r.Message != "" {
					fmt.Fprintf(os.Stderr, "             %s\n", styleDim.Render(r.Message))
				}
			}
			for name := range defaults {
				if _, present := findRule(rules, name); !present && local[name] {
					fmt.Fprintf(os.Stderr, "  %s  %-14s %s\n", styleErr.Render("off      "), name, styleDim.Render("disabled in "+config.FileName))
				}
			}
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, styleDim.Render("defaults are defined in llok's source (internal/guard/guard.go); add or change rules under rules: in "+config.FileName))
			return nil
		},
	}
}

func findRule(rs []guard.Rule, name string) (guard.Rule, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return guard.Rule{}, false
}

// describe renders "when ... → action" for one rule.
func describe(r guard.Rule) string {
	var when []string
	switch r.On {
	case guard.KindFile:
		what := "any file"
		if r.Path != "" {
			what = r.Path
		}
		if r.Op != "" {
			what = r.Op + " " + what
		}
		when = append(when, what)
	case guard.KindNet:
		what := "any host"
		if r.Host != "" {
			what = r.Host
		}
		when = append(when, "broker sees "+what)
	}
	if r.Burst != nil {
		when = append(when, fmt.Sprintf("%d+ within %ds", r.Burst.Count, r.Burst.Seconds))
	}
	action := string(r.Action)
	switch r.Action {
	case guard.ActPause:
		action = styleWarn.Render("pause VM and ask")
	case guard.ActKill:
		action = styleErr.Render("kill VM")
	case guard.ActNotify:
		action = styleOK.Render("notify")
	}
	return strings.Join(when, ", ") + "  →  " + action
}
