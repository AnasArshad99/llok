// Package cli wires the llok command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
	root := &cobra.Command{
		Use:           "llok",
		Short:         "Run coding agents inside locked microVMs",
		Long:          "llok boots a throwaway microVM with your repo inside and nothing else: no network, no host access, one audited channel out.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newInitCmd(), newUpCmd(), newGuestCmd(), newDoctorCmd(), newVersionCmd())
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
