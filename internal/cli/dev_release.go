//go:build !dev

package cli

import "github.com/spf13/cobra"

// Release builds have no developer flags: nothing here can attach a network
// device to a session VM, whatever the environment says.
func registerDevFlags(*cobra.Command) {}

func devNATEnabled(*cobra.Command) bool { return false }
