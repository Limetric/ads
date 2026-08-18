package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/term"
)

// prompter is the wizard's input surface. The real implementation reads a TTY;
// tests inject a fake with scripted answers.
type prompter interface {
	line(prompt string) (string, error)
	secret(prompt string) (string, error)
	confirm(prompt string, def bool) (bool, error)
}

// ttyPrompter reads line-oriented input from a terminal. It reads one byte at a
// time (no buffering ahead) so a masked term.ReadPassword on the same fd never
// loses input that a buffered reader would have swallowed.
type ttyPrompter struct {
	in  io.Reader
	out io.Writer
	fd  int // terminal fd for masked reads; <0 means "not a terminal"
}

func newTTYPrompter(in io.Reader, out io.Writer, fd int) *ttyPrompter {
	return &ttyPrompter{in: in, out: out, fd: fd}
}

// readLine reads up to (not including) the next '\n'. Returns io.EOF only when
// the stream ends with no data read (so the caller can treat it as an abort).
func readLine(r io.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return sb.String(), nil
			}
			if buf[0] != '\r' {
				sb.WriteByte(buf[0])
			}
		}
		if err != nil {
			if err == io.EOF {
				if sb.Len() == 0 {
					return "", io.EOF
				}
				return sb.String(), nil
			}
			return "", err
		}
	}
}

func (p *ttyPrompter) line(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	s, err := readLine(p.in)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (p *ttyPrompter) secret(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	if p.fd >= 0 && term.IsTerminal(p.fd) {
		b, err := term.ReadPassword(p.fd)
		if err != nil {
			return "", err
		}
		fmt.Fprintln(p.out) // ReadPassword swallows the newline; restore it on success
		return strings.TrimSpace(string(b)), nil
	}
	s, err := readLine(p.in)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (p *ttyPrompter) confirm(prompt string, def bool) (bool, error) {
	suffix := " [y/N]: "
	if def {
		suffix = " [Y/n]: "
	}
	fmt.Fprint(p.out, prompt+suffix)
	s, err := readLine(p.in)
	if err != nil {
		return false, err
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return def, nil
	}
	return s == "y" || s == "yes", nil
}

// secretHint returns a non-revealing suffix hint for an existing secret.
// Secrets here are ASCII (OAuth client IDs, developer tokens), so byte slicing is safe.
func secretHint(s string) string {
	if len(s) <= 6 {
		return "…"
	}
	return "…" + s[len(s)-6:]
}

// expandHome expands a leading ~/ and strips surrounding quotes/space (so a
// drag-and-dropped path pastes cleanly).
func expandHome(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "'\"")
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// dashCustomerID formats a 10-digit customer ID as 123-456-7890 for display.
func dashCustomerID(id string) string {
	if len(id) == 10 {
		return id[0:3] + "-" + id[3:6] + "-" + id[6:10]
	}
	return id
}

const (
	urlEnableAPI   = "https://console.cloud.google.com/apis/library/googleads.googleapis.com"
	urlCredentials = "https://console.cloud.google.com/apis/credentials"
	urlConsent     = "https://console.cloud.google.com/apis/credentials/consent"
	urlAPICenter   = "https://ads.google.com/aw/apicenter"
)

// offerToOpen prints an instruction + URL and (unless --no-browser) offers to
// open it, then waits for the user to press Enter.
func offerToOpen(p prompter, out io.Writer, instruction, url string, openFn func(string) error) error {
	fmt.Fprintf(out, "   %s\n   → %s\n", instruction, url)
	if loginNoBrowser {
		_, err := p.line("   Press Enter when done.")
		return err
	}
	open, err := p.confirm("   Open this now?", true)
	if err != nil {
		return err
	}
	if open {
		if e := openFn(url); e != nil {
			fmt.Fprintf(out, "   (couldn't open a browser: %v — open the URL above manually)\n", e)
		}
	}
	_, err = p.line("   Press Enter when done.")
	return err
}

// confirmBrowserOpen returns an openFn for the loopback sign-in that always shows
// the consent URL first and, unless --no-browser, asks before launching a browser
// — mirroring offerToOpen so Step 3 doesn't pop a browser unannounced. Declining
// (or --no-browser) leaves the URL on screen for the user to open manually; the
// loopback server waits for the callback either way.
func confirmBrowserOpen(p prompter, out io.Writer, port int, openFn func(string) error) func(string) error {
	return func(u string) error {
		fmt.Fprintf(out, "   Sign in to Google to authorize ads.\n   → %s\n", u)
		if !loginNoBrowser {
			open, err := p.confirm("   Open this now?", true)
			if err != nil {
				return err
			}
			if open {
				if e := openFn(u); e != nil {
					fmt.Fprintf(out, "   (couldn't open a browser: %v — open the URL above manually)\n", e)
				}
			}
		}
		fmt.Fprintf(out, "   Waiting for callback on %s …\n", loopbackRedirectURL(port))
		return nil
	}
}

// wizardGatherClient resolves the OAuth client: reuse an existing one from cfg,
// or guide the user to download a Desktop-app JSON and read it (re-prompting on
// a bad path).
func wizardGatherClient(p prompter, out io.Writer, cfg *GoogleConfig, openFn func(string) error) (clientCreds, error) {
	if cfg.ClientID != "" && cfg.ClientSecret != "" {
		keep, err := p.confirm(fmt.Sprintf("   Found an OAuth client (%s). Keep it?", secretHint(cfg.ClientID)), true)
		if err != nil {
			return clientCreds{}, err
		}
		if keep {
			return clientCreds{clientID: cfg.ClientID, clientSecret: cfg.ClientSecret, kind: "config"}, nil
		}
	}
	if err := offerToOpen(p, out, "Create Credentials → OAuth client ID → \"Desktop app\" → Download JSON.", urlCredentials, openFn); err != nil {
		return clientCreds{}, err
	}
	for {
		raw, err := p.line("   Path to the downloaded JSON: ")
		if err != nil {
			return clientCreds{}, err
		}
		path := expandHome(raw)
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(out, "   couldn't read %q: %v — try again.\n", path, err)
			continue
		}
		creds, err := parseCredentialsJSON(data)
		if err != nil {
			fmt.Fprintf(out, "   %v — try again.\n", err)
			continue
		}
		if creds.clientID == "" || creds.clientSecret == "" {
			fmt.Fprintln(out, "   that file has no client_id/client_secret — try again.")
			continue
		}
		if creds.kind == "web" {
			fmt.Fprintln(out, "   note: this is a Web-application client; a Desktop-app client is recommended. Continuing.")
		}
		fmt.Fprintf(out, "   ✓ Read client %q\n", secretHint(creds.clientID))
		return creds, nil
	}
}

// wizardGatherDeveloperToken reuses an existing developer token or prompts for a
// new one (masked), re-prompting until non-empty.
func wizardGatherDeveloperToken(p prompter, out io.Writer, cfg *GoogleConfig, openFn func(string) error) (string, error) {
	if cfg.DeveloperToken != "" {
		keep, err := p.confirm(fmt.Sprintf("   Found a developer token (%s). Keep it?", secretHint(cfg.DeveloperToken)), true)
		if err != nil {
			return "", err
		}
		if keep {
			return cfg.DeveloperToken, nil
		}
	}
	if err := offerToOpen(p, out, "In Google Ads: Tools & Settings → API Center.", urlAPICenter, openFn); err != nil {
		return "", err
	}
	for {
		tok, err := p.secret("   Paste your developer token: ")
		if err != nil {
			return "", err
		}
		if tok != "" {
			return tok, nil
		}
		fmt.Fprintln(out, "   developer token can't be empty — try again.")
	}
}

// wizardGatherLoginCustomerID prompts for an optional manager (MCC) account ID,
// defaulting to the existing value and stripping dashes.
func wizardGatherLoginCustomerID(p prompter, out io.Writer, cfg *GoogleConfig) (string, error) {
	prompt := "   Login customer ID [skip]: "
	if cfg.LoginCustomerID != "" {
		prompt = fmt.Sprintf("   Login customer ID [%s]: ", cfg.LoginCustomerID)
	}
	v, err := p.line(prompt)
	if err != nil {
		return "", err
	}
	if v == "" {
		return cfg.LoginCustomerID, nil
	}
	return normalizeCustomerID(v), nil
}

// wizardGatherRefreshToken reuses an existing sign-in or runs the loopback OAuth
// flow to mint a new refresh token.
func wizardGatherRefreshToken(ctx context.Context, p prompter, out io.Writer, creds clientCreds, cfg *GoogleConfig, openFn func(string) error, port int) (string, error) {
	if creds.kind == "authorized_user" && creds.refreshToken != "" {
		return creds.refreshToken, nil
	}
	// Reuse is only offered when the saved sign-in belongs to this OAuth client.
	// Otherwise the wizard would stage a token that cannot authenticate — and
	// `ads login google` is what the mismatch warning tells the user to run, so
	// it has to be the thing that fixes it.
	if cfg.RefreshToken != "" && creds.kind == "config" && googleSavedSignInUsableWith(creds.clientID) {
		choice, err := p.line("   Reuse your existing sign-in, or sign in again? [reuse/new]: ")
		if err != nil {
			return "", err
		}
		if choice == "" || strings.HasPrefix(strings.ToLower(choice), "r") {
			fmt.Fprintln(out, "   reusing existing sign-in.")
			return cfg.RefreshToken, nil
		}
	}
	conf := &oauth2.Config{
		ClientID:     creds.clientID,
		ClientSecret: creds.clientSecret,
		Endpoint:     googleOAuthEndpoint,
		RedirectURL:  loopbackRedirectURL(port),
		Scopes:       []string{adwordsScope},
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listen on %s: %w — is the port busy? pass --port", addr, err)
	}
	code, err := runLoopbackOAuth(ctx, conf, confirmBrowserOpen(p, out, port, openFn), ln)
	if err != nil {
		return "", err
	}
	rt, err := exchangeRefreshToken(ctx, conf, code)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(out, "   ✓ Signed in. Got your refresh token.")
	return rt, nil
}

// wizardSetupComplete reports whether cfg already holds everything the wizard
// would otherwise walk the user through — an OAuth client and a developer token.
// When it does, `ads login google` is being run to (re)establish the sign-in,
// not to set up from scratch, and the wizard skips straight to the browser step.
func wizardSetupComplete(cfg *GoogleConfig) bool {
	return cfg.ClientID != "" && cfg.ClientSecret != "" && cfg.DeveloperToken != ""
}

// runLoginWizard guides first-time setup end to end: prerequisites, OAuth client,
// sign-in, developer token, optional MCC id, then writes config and verifies with
// a live API call.
//
// When the config already holds an OAuth client and a developer token, it
// offers to keep them and only run the sign-in — the case after a token store
// went missing, or a revoked grant — instead of replaying the five-step
// first-time script.
func runLoginWizard(ctx context.Context, out io.Writer, p prompter, cfg *GoogleConfig, openFn func(string) error, port int) error {
	if wizardSetupComplete(cfg) {
		fmt.Fprintln(out, "Found an existing Google Ads setup:")
		fmt.Fprintf(out, "   OAuth client:      %s\n", secretHint(cfg.ClientID))
		fmt.Fprintf(out, "   developer token:   %s\n", secretHint(cfg.DeveloperToken))
		if cfg.LoginCustomerID != "" {
			fmt.Fprintf(out, "   login customer ID: %s\n", dashCustomerID(cfg.LoginCustomerID))
		}
		keep, err := p.confirm("Keep these and just sign in?", true)
		if err != nil {
			return err
		}
		if keep {
			return runSignInOnly(ctx, out, p, cfg, openFn, port)
		}
		fmt.Fprintln(out, "OK — walking through the full setup. Press Enter to keep an existing value.")
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, "Welcome to ads. Let's get you connected to Google Ads.")
	fmt.Fprintln(out, "You'll need: a Google Cloud project, a Desktop-app OAuth client, and a")
	fmt.Fprintln(out, "Google Ads developer token. I'll walk you through each — about 5 minutes.")
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Step 1/5 · Google Cloud project + Google Ads API")
	if err := offerToOpen(p, out, "Sign in, pick or create a project, and click Enable.", urlEnableAPI, openFn); err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 2/5 · Desktop-app OAuth client")
	creds, err := wizardGatherClient(p, out, cfg, openFn)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 3/5 · Sign in (browser)")
	// Any existing sign-in lives in the token store, so consult it before
	// offering to reuse one. This is also where a user upgrading from a
	// config.toml refresh_token gets migrated.
	if err := cfg.resolveRefreshToken(); err != nil {
		return err
	}
	refreshToken, err := wizardGatherRefreshToken(ctx, p, out, creds, cfg, openFn, port)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 4/5 · Developer token")
	devToken, err := wizardGatherDeveloperToken(p, out, cfg, openFn)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "\nStep 5/5 · Manager (MCC) account ID — optional")
	loginCID, err := wizardGatherLoginCustomerID(p, out, cfg)
	if err != nil {
		return err
	}

	return wizardSaveAndVerify(ctx, out, cfg, creds, refreshToken, devToken, loginCID)
}

// runSignInOnly is the wizard's short form: the OAuth client and developer
// token in cfg are kept as they are, and only the sign-in is (re)established.
// A usable saved sign-in is still offered for reuse, so running it against a
// working setup is harmless.
func runSignInOnly(ctx context.Context, out io.Writer, p prompter, cfg *GoogleConfig, openFn func(string) error, port int) error {
	creds := clientCreds{clientID: cfg.ClientID, clientSecret: cfg.ClientSecret, kind: "config"}
	fmt.Fprintln(out, "\nSign in (browser)")
	if err := cfg.resolveRefreshToken(); err != nil {
		return err
	}
	refreshToken, err := wizardGatherRefreshToken(ctx, p, out, creds, cfg, openFn, port)
	if err != nil {
		return err
	}
	return wizardSaveAndVerify(ctx, out, cfg, creds, refreshToken, cfg.DeveloperToken, cfg.LoginCustomerID)
}

// wizardSaveAndVerify writes the gathered credentials — client and token to
// their homes, developer token and MCC id into config — and then proves the
// setup works with a live API call.
func wizardSaveAndVerify(ctx context.Context, out io.Writer, cfg *GoogleConfig, creds clientCreds, refreshToken, devToken, loginCID string) error {
	target, err := configWriteTarget(configPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nSaving to %s …\n", target)
	if err := saveGoogleCredentials(target, creds, refreshToken); err != nil {
		return err
	}
	if err := mergeConfigValues(target, map[string]string{
		"developer_token":   devToken,
		"login_customer_id": loginCID,
	}); err != nil {
		return err
	}

	// Verify with the live config (config is already saved — nothing is lost on failure).
	// In production this also exercises the live refresh-token→access-token exchange;
	// under a test base URL the token source is static, so the offline test covers
	// transport + parsing only.
	final := *cfg
	final.ClientID = creds.clientID
	final.ClientSecret = creds.clientSecret
	// The token was just written to the store, so mark it resolved: re-reading
	// the store here would only re-derive the same value, and would replay the
	// deprecation notices in the middle of the wizard's own output.
	final.RefreshToken = refreshToken
	final.refreshTokenResolved = true
	final.DeveloperToken = devToken
	final.LoginCustomerID = loginCID
	fmt.Fprint(out, "Verifying… ")
	client, err := NewClient(ctx, &final)
	if err == nil {
		var ids []string
		ids, err = client.ListAccessibleCustomers(ctx)
		if err == nil {
			dashed := make([]string, len(ids))
			for i, id := range ids {
				dashed[i] = dashCustomerID(id)
			}
			fmt.Fprintf(out, "✓ Connected — %d accessible account(s): %s\n", len(ids), strings.Join(dashed, ", "))
			fmt.Fprintln(out, "\nYou're ready. Try:  ads google accounts")
			return nil
		}
	}
	fmt.Fprintln(out, "✗")
	fmt.Fprintf(out, "Saved your config, but verification failed: %v\n", err)
	fmt.Fprintln(out, "Likely: the developer token isn't approved yet or was mistyped, or an OAuth problem.")
	fmt.Fprintln(out, "Fix it and re-run `ads login google`, or run `ads doctor google`.")
	return fmt.Errorf("verification failed: %w", err)
}
