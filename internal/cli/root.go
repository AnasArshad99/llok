// Package cli wires the llok command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/AnasArshad99/llok/internal/guest"
)

// Version is injected at build time via -ldflags.
var Version = "dev"

// Execute runs the root command.
func Execute() error {
	err := newRoot().Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "llok:", err)
	}
	return err
}

func newRoot() *cobra.Command {
	guest.LlokVersion = Version
	up := newUpCmd()
	root := &cobra.Command{
		Use:           "llok",
		Short:         "Run coding agents inside locked microVMs",
		Long:          "llok boots a throwaway microVM with your repo inside and nothing else: no network, no host access, one audited channel out.\n\nRunning llok with no arguments is the same as llok up.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		// Bare `llok` starts a session, like `llok up`.
		RunE: func(cmd *cobra.Command, args []string) error {
			return up.RunE(up, args)
		},
		PersistentPreRun: func(*cobra.Command, []string) {
			saneTerminal()
		},
	}
	// Bare llok takes up's flags too (llok --agent codex, llok -s review).
	root.Flags().AddFlagSet(up.Flags())
	root.AddCommand(newInitCmd(), up, newAttachCmd(), newSessionsCmd(), newKillCmd(), newServeCmd(), newAuthCmd(), newAuditCmd(), newEventsCmd(), newRulesCmd(), newTrustCmd(), newAgentsCmd(), newGuestCmd(), newDoctorCmd(), newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the llok version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "llok", Version)
		},
	}
}
