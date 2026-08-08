package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/term"
)

const adwordsScope = "https://www.googleapis.com/auth/adwords"

// loginCallbackTimeout bounds how long the loopback server waits for Google's
// redirect. First-time consent flows involve an account picker and often 2FA,
// so this is deliberately generous.
const loginCallbackTimeout = 5 * time.Minute

// loopbackRedirectURL builds the OAuth redirect URI for the loopback flow.
// Google's guidance is the literal IP http://127.0.0.1:<port> — "localhost"
// can resolve to ::1 while the listener binds the IPv4 loopback, in which case
// the redirect never arrives (issue #11).
func loopbackRedirectURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// clientCreds is the OAuth client identity used to mint a refresh token. kind is
// "installed" (Desktop app), "web", "authorized_user" (already-tokened file), or
// "config" (taken from env/TOML). refreshToken is set only for authorized_user.
type clientCreds struct {
	clientID     string
	clientSecret string
	refreshToken string
	kind         string
}

type oauthClientBlock struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// parseCredentialsJSON reads a Google Cloud OAuth client JSON. It accepts a
// Desktop-app ("installed") or Web ("web") client, or an already-authorized
// ("authorized_user") file that carries its own refresh token.
func parseCredentialsJSON(data []byte) (clientCreds, error) {
	var doc struct {
		Installed    *oauthClientBlock `json:"installed"`
		Web          *oauthClientBlock `json:"web"`
		Type         string            `json:"type"`
		ClientID     string            `json:"client_id"`
		ClientSecret string            `json:"client_secret"`
		RefreshToken string            `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return clientCreds{}, fmt.Errorf("parse credentials JSON: %w", err)
	}
	switch {
	case doc.Installed != nil:
		return clientCreds{clientID: doc.Installed.ClientID, clientSecret: doc.Installed.ClientSecret, kind: "installed"}, nil
	case doc.Web != nil:
		return clientCreds{clientID: doc.Web.ClientID, clientSecret: doc.Web.ClientSecret, kind: "web"}, nil
	case doc.Type == "authorized_user":
		return clientCreds{clientID: doc.ClientID, clientSecret: doc.ClientSecret, refreshToken: doc.RefreshToken, kind: "authorized_user"}, nil
	default:
		return clientCreds{}, errors.New("unrecognized credentials format — expected a Desktop-app OAuth client (an \"installed\" block). Download from Google Cloud Console → APIs & Services → Credentials → OAuth 2.0 Client ID → Desktop app")
	}
}

// mergeConfigValues merges the non-empty entries of values into the TOML config
// at path, preserving any keys already present. Empty values are skipped (so a
// skipped optional field never overwrites an existing one). 0600 file, 0700 dir.
func mergeConfigValues(path string, values map[string]string) error {
	m := map[string]any{}
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &m); err != nil {
			return fmt.Errorf("read existing config %q: %w", path, err)
		}
	}
	for k, v := range values {
		if v != "" {
			m[k] = v
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

// writeOAuthToConfig merges the OAuth client id/secret and refresh token into the
// TOML config at path, preserving any other keys already present.
func writeOAuthToConfig(path string, c clientCreds, refreshToken string) error {
	return mergeConfigValues(path, map[string]string{
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"refresh_token": refreshToken,
	})
}

// configWriteTarget returns the file `ads login google` should write to: the explicit
// --config path if given, otherwise the default per-user config.toml.
func configWriteTarget(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(dir, defaultConfigFile), nil
}

// resolveLoginCreds picks the client credentials: an explicit --credentials file
// wins; otherwise fall back to the already-resolved env/TOML config.
func resolveLoginCreds(cfg *GoogleConfig, credentialsPath string) (clientCreds, error) {
	if credentialsPath != "" {
		data, err := os.ReadFile(credentialsPath)
		if err != nil {
			return clientCreds{}, fmt.Errorf("read credentials file %q: %w", credentialsPath, err)
		}
		creds, err := parseCredentialsJSON(data)
		if err != nil {
			return clientCreds{}, fmt.Errorf("credentials file %q: %w", credentialsPath, err)
		}
		if creds.clientID == "" || creds.clientSecret == "" {
			return clientCreds{}, fmt.Errorf("credentials file %q is missing client_id/client_secret", credentialsPath)
		}
		return creds, nil
	}
	if cfg.ClientID != "" && cfg.ClientSecret != "" {
		return clientCreds{clientID: cfg.ClientID, clientSecret: cfg.ClientSecret, kind: "config"}, nil
	}
	return clientCreds{}, errors.New("no OAuth client credentials found — pass --credentials <desktop-app.json>, or set GOOGLE_ADS_CLIENT_ID and GOOGLE_ADS_CLIENT_SECRET. Create a Desktop-app client at Google Cloud Console → APIs & Services → Credentials → OAuth 2.0 Client ID → Desktop app")
}

// runLoopbackOAuth opens the browser to Google's consent screen and captures the
// authorization code on a loopback HTTP server. conf.RedirectURL and ln must
// agree on the port. It returns once the callback arrives, errors, or times out.
func runLoopbackOAuth(ctx context.Context, conf *oauth2.Config, openFn func(string) error, ln net.Listener) (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}
	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)
	// send delivers the first result and drops any later ones, so a stray second
	// callback (browser retry, favicon hitting the catch-all) can't block its
	// handler goroutine forever on a full channel.
	send := func(r result) {
		select {
		case resultCh <- r:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// A request carrying none of the OAuth callback parameters is not the
		// callback — a browser preconnect, favicon fetch, port scanner, or the
		// user opening the bare URL. Treating those as failed callbacks used to
		// abort the whole login before the real redirect arrived (issue #11).
		if q.Get("error") == "" && q.Get("state") == "" && q.Get("code") == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch {
		case q.Get("error") != "":
			msg := q.Get("error") + ": " + q.Get("error_description")
			writeCallbackPage(w, false, msg)
			send(result{err: fmt.Errorf("authorization failed: %s", msg)})
		case q.Get("state") != state:
			writeCallbackPage(w, false, "state mismatch")
			send(result{err: errors.New("state parameter mismatch — aborting (possible CSRF)")})
		case q.Get("code") == "":
			writeCallbackPage(w, false, "no authorization code in callback")
			send(result{err: errors.New("no authorization code in callback")})
		default:
			writeCallbackPage(w, true, "")
			send(result{code: q.Get("code")})
		}
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	// Graceful shutdown lets the in-flight callback response (the "Authorization
	// successful" page) flush before we tear down. Shutdown also closes ln, so
	// the listener is still closed exactly once before returning.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := openFn(authURL); err != nil {
		return "", err
	}

	timer := time.NewTimer(loginCallbackTimeout)
	defer timer.Stop()
	select {
	case res := <-resultCh:
		return res.code, res.err
	case <-timer.C:
		return "", fmt.Errorf("no authorization received within %s — did you approve in the browser?", loginCallbackTimeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func writeCallbackPage(w http.ResponseWriter, ok bool, msg string) {
	w.Header().Set("Content-Type", "text/html")
	if ok {
		_, _ = io.WriteString(w, "<h1>Authorization successful</h1><p>You can close this tab and return to the terminal.</p>")
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, "<h1>Authorization failed</h1><p>"+html.EscapeString(msg)+"</p>")
}

// exchangeRefreshToken trades an authorization code for tokens and returns the
// refresh token. A missing refresh token almost always means a misconfigured
// OAuth client, so the error spells out the usual causes.
func exchangeRefreshToken(ctx context.Context, conf *oauth2.Config, code string) (string, error) {
	tok, err := conf.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("exchange authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		return "", errors.New("no refresh_token in response — common causes: wrong OAuth client type (need Desktop app, not Web application), the loopback redirect URI is not authorized, or the Google Ads API is not enabled in the project")
	}
	return tok.RefreshToken, nil
}

func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	return exec.Command(name, args...).Start()
}

// loadLoginConfig loads Google configuration for `login`. Unlike loadGoogleConfig, it
// tolerates an explicit --config path that does not exist yet: that file is
// the one login will create, so a missing target means "load env only".
func loadLoginConfig(path string) (*GoogleConfig, error) {
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				cfg := &GoogleConfig{}
				cfg.finalize()
				return cfg, nil
			}
			return nil, fmt.Errorf("stat config %q: %w", path, err)
		}
	}
	return loadGoogleConfig(path)
}

var (
	loginCredentialsPath string
	loginPort            int
	loginNoBrowser       bool
	loginNoInput         bool
)

// isInteractiveLogin reports whether `ads login google` should run the guided wizard:
// stdin is a real terminal, --no-input was not passed, and the non-interactive
// --credentials shortcut was not used.
func isInteractiveLogin() bool {
	if loginNoInput || loginCredentialsPath != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd()))
}

var googleLoginCmd = &cobra.Command{
	Use:   "google",
	Short: "Sign in to Google Ads via OAuth2 and save a refresh token",
	Long:  "Runs Google's loopback OAuth2 flow: it opens your browser, captures the\nauthorization code on localhost, exchanges it for a refresh token, and writes\nthe credentials into your ads config. The developer token is still required\nseparately (see `ads doctor google`).",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if isInteractiveLogin() {
			cfg, err := loadLoginConfig(configPath)
			if err != nil {
				return err
			}
			openFn := openBrowser
			if loginNoBrowser {
				openFn = func(u string) error {
					fmt.Fprintf(cmd.OutOrStdout(), "Open this URL:\n  %s\n", u)
					return nil
				}
			}
			p := newTTYPrompter(os.Stdin, cmd.OutOrStdout(), int(os.Stdin.Fd()))
			return runLoginWizard(cmd.Context(), cmd.OutOrStdout(), p, cfg, openFn, loginPort)
		}

		// --- non-interactive path (unchanged) ---
		cfg, err := loadLoginConfig(configPath)
		if err != nil {
			return err
		}
		creds, err := resolveLoginCreds(cfg, loginCredentialsPath)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "=== Google Ads OAuth2 sign-in ===")

		refreshToken := creds.refreshToken
		if creds.kind == "authorized_user" {
			if refreshToken == "" {
				return errors.New("authorized_user credentials file has no refresh_token")
			}
			fmt.Fprintln(out, "Credentials file already contains a refresh token; skipping browser sign-in.")
		} else {
			if creds.kind == "web" {
				fmt.Fprintln(out, "Warning: this is a Web-application client; loopback sign-in expects a Desktop-app client. Trying anyway.")
			}
			conf := &oauth2.Config{
				ClientID:     creds.clientID,
				ClientSecret: creds.clientSecret,
				Endpoint:     googleOAuthEndpoint,
				RedirectURL:  loopbackRedirectURL(loginPort),
				Scopes:       []string{adwordsScope},
			}
			addr := fmt.Sprintf("127.0.0.1:%d", loginPort)
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w — is the port busy? pass --port", addr, err)
			}
			openFn := func(u string) error {
				// A missing browser opener (headless Linux without xdg-open)
				// must not abort the login — fall back to printing the URL,
				// like the wizard path does (issue #11).
				if err := openBrowser(u); err != nil {
					fmt.Fprintf(out, "Could not open a browser (%v).\nOpen this URL manually:\n  %s\n", err, u)
				}
				return nil
			}
			if loginNoBrowser {
				openFn = func(u string) error {
					fmt.Fprintf(out, "Open this URL in your browser:\n  %s\n", u)
					return nil
				}
			} else {
				fmt.Fprintln(out, "Opening browser for Google sign-in…")
			}
			fmt.Fprintf(out, "Waiting for callback on %s …\n", loopbackRedirectURL(loginPort))
			code, err := runLoopbackOAuth(cmd.Context(), conf, openFn, ln)
			if err != nil {
				return err
			}
			refreshToken, err = exchangeRefreshToken(cmd.Context(), conf, code)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "✓ Authorized. Exchanged code for refresh token.")
		}

		target, err := configWriteTarget(configPath)
		if err != nil {
			return err
		}
		if err := writeOAuthToConfig(target, creds, refreshToken); err != nil {
			return err
		}
		fmt.Fprintf(out, "✓ Wrote credentials to %s\n\n", target)
		fmt.Fprintln(out, "For CI / MCP host config, set:")
		fmt.Fprintf(out, "  export GOOGLE_ADS_CLIENT_ID=%q\n", creds.clientID)
		fmt.Fprintf(out, "  export GOOGLE_ADS_CLIENT_SECRET=%q\n", creds.clientSecret)
		fmt.Fprintf(out, "  export GOOGLE_ADS_REFRESH_TOKEN=%q\n\n", refreshToken)
		fmt.Fprintln(out, "Run `ads doctor google` to verify. (developer token still required.)")
		return nil
	},
}

func init() {
	googleLoginCmd.Flags().StringVar(&loginCredentialsPath, "credentials", "", "path to a Desktop-app OAuth client JSON downloaded from Google Cloud Console")
	googleLoginCmd.Flags().IntVar(&loginPort, "port", 8085, "loopback port for the OAuth callback")
	googleLoginCmd.Flags().BoolVar(&loginNoBrowser, "no-browser", false, "print the auth URL instead of opening a browser")
	googleLoginCmd.Flags().BoolVar(&loginNoInput, "no-input", false, "never prompt; fail if credentials are missing (for scripts/CI)")
}
