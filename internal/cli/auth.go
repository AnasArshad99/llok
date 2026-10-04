package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/AnasArshad99/llok/internal/agents"
	"github.com/AnasArshad99/llok/internal/creds"
)

func newAuthCmd() *cobra.Command {
	var apiKey bool
	cmd := &cobra.Command{
		Use:   "auth <provider>",
		Short: "Store a provider credential on the host (the VM never sees it)",
		Long: `Signs in to a provider and keeps the credential in the macOS Keychain.
Providers that support a subscription login (anthropic, via Claude Code's
own setup-token) are driven on this Mac and the token is captured; all
providers accept an API key at a hidden prompt with --api-key.

Providers: ` + strings.Join(agents.ProviderNames(), ", ") + `. "claude" is an alias for anthropic.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "claude" {
				name = "anthropic"
			}
			p, ok := agents.Lookup(name)
			if !ok {
				return fmt.Errorf("unknown provider %q; known: %s", name, strings.Join(agents.ProviderNames(), ", "))
			}
			if apiKey || p.Login == nil {
				return storeAPIKey(p)
			}
			return captureLogin(p)
		},
	}
	cmd.Flags().BoolVar(&apiKey, "api-key", false, "store an API key instead of signing in")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show which provider credentials are stored",
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, name := range agents.ProviderNames() {
				c, err := creds.Get(name)
				switch {
				case errors.Is(err, creds.ErrNotFound):
					fmt.Fprintln(os.Stderr, styleDim.Render("  "+name+": not configured (llok auth "+name+")"))
				case err != nil:
					fmt.Fprintln(os.Stderr, bad(name+": "+err.Error()))
				default:
					fmt.Fprintln(os.Stderr, ok(fmt.Sprintf("%s: %s %s", name, c.Kind, creds.Masked(c.Secret))))
				}
			}
			return nil
		},
	}

	logout := &cobra.Command{
		Use:   "logout <provider>",
		Short: "Remove a stored credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "claude" {
				name = "anthropic"
			}
			if err := creds.Delete(name); err != nil {
				return errors.New("nothing stored for " + name)
			}
			fmt.Fprintln(os.Stderr, ok("removed "+name+" credential from Keychain"))
			return nil
		},
	}
	cmd.AddCommand(status, logout)
	return cmd
}

func storeAPIKey(p agents.Provider) error {
	fmt.Fprintf(os.Stderr, "%s API key (input hidden): ", p.Name)
	key, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	k := strings.TrimSpace(string(key))
	if len(k) < 16 {
		return errors.New("that does not look like an API key")
	}
	if err := creds.Set(p.Name, creds.Credential{Kind: "apikey", Secret: k}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, ok("stored "+p.Name+" API key in Keychain ("+creds.Masked(k)+")"))
	return nil
}

// captureLogin runs the provider's own sign-in command on this Mac in a
// pty, mirrors it to the user's terminal, and captures the token it prints.
func captureLogin(p agents.Provider) error {
	bin, err := exec.LookPath(p.Login.Needs)
	if err != nil {
		return fmt.Errorf("%s is not installed on this Mac; install it, or use: llok auth %s --api-key", p.Login.Needs, p.Name)
	}
	fmt.Fprintln(os.Stderr, styleDim.Render("running "+strings.Join(p.Login.Command, " ")+" — sign in when the browser opens"))
	fmt.Fprintln(os.Stderr)

	c := exec.Command(bin, p.Login.Command[1:]...)
	ptmx, err := pty.Start(c)
	if err != nil {
		return err
	}
	defer ptmx.Close()

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

	tok := regexp.MustCompile(p.Login.Pattern).Find(captured)
	if tok == nil {
		return fmt.Errorf("no token found in the output; try again or use: llok auth %s --api-key", p.Name)
	}
	if err := creds.Set(p.Name, creds.Credential{Kind: "oauth", Secret: string(tok)}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, ok("stored "+p.Name+" token in Keychain ("+creds.Masked(string(tok))+")"))
	fmt.Fprintln(os.Stderr, styleDim.Render("the VM only ever sees a placeholder; llok injects this token on the way out"))
	return nil
}
