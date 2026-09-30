package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/AnasArshad99/llok/internal/creds"
)

var oauthTokenRe = regexp.MustCompile(`sk-ant-oat01-[A-Za-z0-9_\-]{20,}`)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Store provider credentials on the host (the VM never sees them)",
	}

	var apiKey bool
	claude := &cobra.Command{
		Use:   "claude",
		Short: "Sign in to Claude: runs Claude Code's own login and keeps the token in your Keychain",
		Long: `Runs "claude setup-token" on this Mac. Your browser opens, you sign in with
your Claude subscription, and llok captures the long-lived token it prints and
stores it in the macOS Keychain. Nothing is copied by hand.

With --api-key, llok asks for an Anthropic API key at a hidden prompt instead.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if apiKey {
				return storeAPIKey()
			}
			return captureSetupToken()
		},
	}
	claude.Flags().BoolVar(&apiKey, "api-key", false, "store an API key instead of signing in")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show which credentials are stored",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := creds.Get("anthropic")
			if errors.Is(err, creds.ErrNotFound) {
				fmt.Fprintln(os.Stderr, warn("anthropic: not configured (run: llok auth claude)"))
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, ok(fmt.Sprintf("anthropic: %s %s", c.Kind, creds.Masked(c.Secret))))
			return nil
		},
	}

	logout := &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored Claude credential",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := creds.Delete("anthropic"); err != nil {
				return errors.New("nothing to remove")
			}
			fmt.Fprintln(os.Stderr, ok("removed anthropic credential from Keychain"))
			return nil
		},
	}
	cmd.AddCommand(claude, status, logout)
	return cmd
}

func storeAPIKey() error {
	fmt.Fprint(os.Stderr, "Anthropic API key (input hidden): ")
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	k := string(key)
	if len(k) < 20 {
		return errors.New("that does not look like an API key")
	}
	if err := creds.Set("anthropic", creds.Credential{Kind: "apikey", Secret: k}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, ok("stored Anthropic API key in Keychain ("+creds.Masked(k)+")"))
	return nil
}

func captureSetupToken() error {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("claude is not installed on this Mac; install it, or use: llok auth claude --api-key")
	}
	fmt.Fprintln(os.Stderr, styleDim.Render("running claude setup-token — sign in when the browser opens"))
	fmt.Fprintln(os.Stderr)

	c := exec.Command(claudeBin, "setup-token")
	ptmx, err := pty.Start(c)
	if err != nil {
		return err
	}
	defer ptmx.Close()

	// Mirror the process to the user's terminal while capturing its output.
	stdinFD := int(os.Stdin.Fd())
	if term.IsTerminal(stdinFD) {
		if old, err := term.MakeRaw(stdinFD); err == nil {
			defer term.Restore(stdinFD, old)
		}
		if w, h, err := term.GetSize(stdinFD); err == nil {
			pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)})
		}
	}
	go io.Copy(ptmx, os.Stdin)

	var captured []byte
	buf := make([]byte, 4096)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			captured = append(captured, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	c.Wait()
	fmt.Fprint(os.Stderr, "\r\n")

	tok := oauthTokenRe.Find(captured)
	if tok == nil {
		return errors.New("no token found in claude's output; try again or use --api-key")
	}
	if err := creds.Set("anthropic", creds.Credential{Kind: "oauth", Secret: string(tok)}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, ok("stored Claude token in Keychain ("+creds.Masked(string(tok))+")"))
	fmt.Fprintln(os.Stderr, styleDim.Render("the VM will only ever see a placeholder; llok injects this token on the way out"))
	return nil
}
