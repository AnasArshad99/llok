//go:build dev

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Developer builds (go build -tags dev) carry --dev-nat for debugging the
// guest with a real network device. It still needs LLOK_DEV=1.
func registerDevFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("dev-nat", false, "DEVELOPMENT ONLY: attach a NAT network device")
	_ = cmd.Flags().MarkHidden("dev-nat")
}

func devNATEnabled(cmd *cobra.Command) bool {
	on, _ := cmd.Flags().GetBool("dev-nat")
	if !on {
		return false
	}
	if os.Getenv("LLOK_DEV") != "1" {
		fmt.Fprintln(os.Stderr, "llok: --dev-nat ignored (set LLOK_DEV=1 in a dev build to use it)")
		return false
	}
	return true
}
