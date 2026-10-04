package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
Subscription logins are driven on this Mac with the vendor's own CLI and the
result is captured: anthropic via "claude setup-token", openai via "codex
login" (ChatGPT). All providers accept an API key at a hidden prompt with
--api-key. Use whichever you have.

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
			if apiKey {
				return storeAPIKey(p)
			}
			if p.Name == "openai" {
				return chatgptLogin(p)
			}
			if p.Login == nil {
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

// chatgptLogin stores a ChatGPT login for Codex. If codex is installed it
// runs "codex login" (browser flow, callback on this Mac); either way it
// then imports the tokens Codex wrote to ~/.codex/auth.json. The access and
// refresh tokens stay on the host; the account id and identity token are
// kept as metadata the guest needs alongside the placeholder.
func chatgptLogin(p agents.Provider) error {
	authFile := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	if bin, err := exec.LookPath("codex"); err == nil {
		fmt.Fprintln(os.Stderr, styleDim.Render("running codex login — sign in with ChatGPT when the browser opens"))
		c := exec.Command(bin, "login")
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("codex login failed: %w  (or use: llok auth openai --api-key)", err)
		}
	} else if _, err := os.Stat(authFile); err != nil {
		return errors.New("codex is not installed on this Mac and no ChatGPT login was found; install Codex and run llok auth openai again, or use: llok auth openai --api-key")
	} else {
		fmt.Fprintln(os.Stderr, styleDim.Render("codex is not on PATH; importing the ChatGPT login Codex already saved on this Mac"))
	}
	data, err := os.ReadFile(authFile)
	if err != nil {
		return err
	}
	var f struct {
		AuthMode string  `json:"auth_mode"`
		APIKey   *string `json:"OPENAI_API_KEY"`
		Tokens   *struct {
			ID, Access, Refresh, Account string `json:"-"`
			IDToken                      string `json:"id_token"`
			AccessToken                  string `json:"access_token"`
			RefreshToken                 string `json:"refresh_token"`
			AccountID                    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("could not read %s: %w", authFile, err)
	}
	switch {
	case f.Tokens != nil && f.Tokens.AccessToken != "":
		c := creds.Credential{Kind: "chatgpt", Secret: f.Tokens.AccessToken,
			Meta: map[string]string{"account_id": f.Tokens.AccountID, "id_token": f.Tokens.IDToken, "refresh_token": ""}}
		delete(c.Meta, "refresh_token")
		if err := creds.Set(p.Name, c); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, ok("stored ChatGPT login for Codex in Keychain ("+creds.Masked(c.Secret)+")"))
		fmt.Fprintln(os.Stderr, styleDim.Render("the VM only ever sees placeholders; when the token expires, run llok auth openai again"))
	case f.APIKey != nil && *f.APIKey != "":
		if err := creds.Set(p.Name, creds.Credential{Kind: "apikey", Secret: *f.APIKey}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, ok("stored the API key Codex had saved in Keychain ("+creds.Masked(*f.APIKey)+")"))
	default:
		return errors.New("no usable login in " + authFile + "; run codex login, or use: llok auth openai --api-key")
	}
	return nil
}
