package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/muhammadanas/llok/internal/guest"
)

func newGuestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guest",
		Short: "Manage the guest kernel, initramfs and root disk",
	}
	var kernelOnly, rootfsOnly bool
	build := &cobra.Command{
		Use:   "build",
		Short: "Build the guest: Alpine kernel + initramfs, then a root disk with Claude Code",
		Long: `Downloads Alpine's kernel and assembles the llok initramfs, then boots a
one-off builder VM (the only VM that ever gets a network device) which installs
Alpine, git, Node, ripgrep and Claude Code onto ~/.llok/guest/rootfs.img.
Session VMs boot that disk read-only with no network.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !rootfsOnly {
				if err := guest.Build(cmd.Context()); err != nil {
					return err
				}
			}
			if !kernelOnly {
				return guest.BuildRootfs(cmd.Context())
			}
			return nil
		},
	}
	build.Flags().BoolVar(&kernelOnly, "kernel-only", false, "only the kernel and initramfs")
	build.Flags().BoolVar(&rootfsOnly, "rootfs-only", false, "only the root disk (kernel must exist)")

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
			if r, ok := guest.RootfsPath(); ok {
				fmt.Fprintln(cmd.OutOrStdout(), r)
			}
			return nil
		},
	}
	cmd.AddCommand(build, where)
	return cmd
}
