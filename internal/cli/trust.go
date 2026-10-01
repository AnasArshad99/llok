package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/config"
	"github.com/AnasArshad99/llok/internal/guard"
	"github.com/AnasArshad99/llok/internal/guest"
	"github.com/AnasArshad99/llok/internal/trust"
)

// widens reports whether cfg grants anything beyond llok's defaults.
func widens(cfg *config.Config) bool {
	return len(cfg.Network.Allow) > 0 || len(cfg.Rules) > 0
}

func trustStore() (*trust.Store, error) {
	home, err := guest.Home()
	if err != nil {
		return nil, err
	}
	return trust.Open(filepath.Join(home, "trust.json"))
}

// describePolicy renders the parts of a policy that need approval.
func describePolicy(cfg *config.Config) string {
	var b strings.Builder
	if len(cfg.Network.Allow) > 0 {
		b.WriteString("  extra hosts the agent may reach:\n")
		for _, h := range cfg.Network.Allow {
			b.WriteString("    " + styleWarn.Render(h) + "\n")
		}
	}
	if len(cfg.Rules) > 0 {
		b.WriteString("  supervisor rule changes:\n")
		for _, r := range cfg.Rules {
			if r.Action == guard.ActOff {
				b.WriteString("    " + styleErr.Render(r.Name+": off") + "\n")
			} else {
				b.WriteString("    " + r.Name + " → " + string(r.Action) + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ensureTrusted checks that a policy which widens access has been approved
// for this repo, asking interactively when possible.
func ensureTrusted(cfg *config.Config, absRepo string) error {
	if !widens(cfg) {
		return nil
	}
	store, err := trustStore()
	if err != nil {
		return err
	}
	fp := trust.Fingerprint(cfg.Network.Allow, cfg.Rules)
	if store.Approved(absRepo, fp) {
		return nil
	}
	fmt.Fprintln(os.Stderr, styleWarn.Render("!")+" "+config.FileName+" in this repo grants more than llok's defaults:")
	fmt.Fprintln(os.Stderr, describePolicy(cfg))
	fmt.Fprintln(os.Stderr, styleDim.Render("  policy files travel with the repo; for a cloned repo someone else wrote this."))
	if !isTTY() {
		return errors.New("policy not approved for this repo; run: llok trust")
	}
	ans, err := inputPrompt("Trust this policy for "+absRepo+"?", "yes to approve and remember; anything else to abort", "no")
	if err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(ans)) != "yes" && strings.ToLower(strings.TrimSpace(ans)) != "y" {
		return errors.New("policy not approved; edit " + config.FileName + " or run: llok trust")
	}
	return store.Approve(absRepo, fp)
}

func newTrustCmd() *cobra.Command {
	var revoke, yes bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Approve this repo's llok.yaml (its allowlist and rule changes)",
		Long: `A llok.yaml that adds allowed hosts or changes supervisor rules must be
approved once per repo before llok up applies it. Approval is tied to the
policy's content, so any later change asks again. --revoke forgets it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			absRepo, err := filepath.Abs(".")
			if err != nil {
				return err
			}
			store, err := trustStore()
			if err != nil {
				return err
			}
			if revoke {
				if err := store.Revoke(absRepo); err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, ok("forgot approval for "+absRepo))
				return nil
			}
			cfg, err := config.Load(".")
			if err != nil {
				return err
			}
			if !widens(cfg) {
				fmt.Fprintln(os.Stderr, ok(config.FileName+" uses only llok's defaults; nothing to approve"))
				return nil
			}
			fp := trust.Fingerprint(cfg.Network.Allow, cfg.Rules)
			if store.Approved(absRepo, fp) {
				fmt.Fprintln(os.Stderr, ok("already approved"))
				return nil
			}
			fmt.Fprintln(os.Stderr, describePolicy(cfg))
			if !yes {
				if !isTTY() {
					return errors.New("not a terminal; pass --yes to approve")
				}
				ans, err := inputPrompt("Approve?", "", "no")
				if err != nil {
					return err
				}
				if a := strings.ToLower(strings.TrimSpace(ans)); a != "yes" && a != "y" {
					return errors.New("not approved")
				}
			}
			if err := store.Approve(absRepo, fp); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, ok("approved for "+absRepo))
			return nil
		},
	}
	cmd.Flags().BoolVar(&revoke, "revoke", false, "forget the approval for this repo")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "approve without asking")
	return cmd
}
