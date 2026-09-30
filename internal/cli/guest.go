package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/muhammadanas/llok/internal/guest"
)

func newGuestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guest",
		Short: "Manage the guest kernel and initramfs",
	}
	build := &cobra.Command{
		Use:   "build",
		Short: "Download the Alpine kernel and build the llok initramfs into ~/.llok/guest",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return guest.Build(cmd.Context())
		},
	}
	where := &cobra.Command{
		Use:   "path",
		Short: "Print the guest artifact paths",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, i, err := guest.Paths()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), k)
			fmt.Fprintln(cmd.OutOrStdout(), i)
			return nil
		},
	}
	cmd.AddCommand(build, where)
	return cmd
}
