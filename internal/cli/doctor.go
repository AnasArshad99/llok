package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/muhammadanas/llok/internal/guest"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this Mac can run llok VMs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rows []string
			fail := false

			out, _ := exec.Command("sysctl", "-n", "kern.hv_support").Output()
			if strings.TrimSpace(string(out)) == "1" {
				rows = append(rows, ok("hypervisor support"))
			} else {
				rows = append(rows, bad("hypervisor not supported on this machine"))
				fail = true
			}

			out, _ = exec.Command("sw_vers", "-productVersion").Output()
			ver := strings.TrimSpace(string(out))
			if strings.HasPrefix(ver, "26") || strings.HasPrefix(ver, "15") {
				rows = append(rows, ok("macOS "+ver))
			} else {
				rows = append(rows, warn("macOS "+ver+" (tested on 26)"))
			}

			exe, _ := os.Executable()
			out, err := exec.Command("codesign", "-d", "--entitlements", "-", exe).CombinedOutput()
			if err == nil && strings.Contains(string(out), "com.apple.security.virtualization") {
				rows = append(rows, ok("binary signed with virtualization entitlement"))
			} else {
				rows = append(rows, bad("binary lacks virtualization entitlement (run: make build)"))
				fail = true
			}

			if k, i, err := guest.Paths(); err == nil {
				ks, _ := os.Stat(k)
				is, _ := os.Stat(i)
				rows = append(rows, ok(fmt.Sprintf("guest image built (kernel %d MiB, initrd %d MiB)",
					ks.Size()>>20, is.Size()>>20)))
			} else {
				rows = append(rows, bad("guest image missing (run: llok guest build)"))
				fail = true
			}

			printBox("llok doctor", strings.Join(rows, "\n"))
			if fail {
				return fmt.Errorf("some checks failed")
			}
			return nil
		},
	}
}
