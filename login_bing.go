package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// `ads login bing` runs Microsoft Entra's authorization-code flow with PKCE on
// a loopback redirect.
//
// PKCE, not a client secret: the application ads signs in with is a *public*
// client, because a CLI cannot keep a secret on someone else's machine. Entra
// rejects a public client that sends one ("Public clients can't send a client
// secret"), and PKCE is what protects the code in its place. A user who has
// registered a web application instead can still set BING_ADS_CLIENT_SECRET,
// and it is sent alongside — Entra accepts PKCE from confidential clients too.

// bingLoginRedirectPort is the default loopback port for the callback. It
// differs from Google's so both flows can be registered, and used, side by side.
const bingLoginRedirectPort = 8086

var (
	bingLoginPort        int
	bingLoginNoBrowser   bool
	bingLoginClientID    string
	bingLoginEnvironment string
)

// bingLoginRedirectURL builds the redirect URI. Entra requires the literal
// value to be registered on the application, so it is spelled the same way
// every time: http://localhost:<port>.
//
// localhost, not 127.0.0.1: Entra's portal registers loopback redirects for
// desktop apps under the localhost name, and the value has to match exactly.
// The listener binds both loopback families for the same reason (see listen).
func bingLoginRedirectURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// bingLoginConfig builds the OAuth config for the sign-in flow.
func bingLoginConfig(cfg *BingConfig, port int) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     bingOAuthEndpoint(),
		RedirectURL:  bingLoginRedirectURL(port),
		Scopes:       bingScopes,
	}
}

// runBingLoopbackOAuth opens the consent screen and captures the authorization
// code, with PKCE. It reuses the same loopback server the Google flow uses, so
// there is one implementation of "wait for a browser redirect" to get right.
func runBingLoopbackOAuth(ctx context.Context, conf *oauth2.Config, openFn func(string) error, ln net.Listener) (code, verifier string, err error) {
	verifier = oauth2.GenerateVerifier()
	// The challenge goes out with the authorization request and the verifier
	// with the exchange; an attacker who intercepts the redirected code cannot
	// redeem it without the verifier, which never leaves this process.
	code, err = runLoopbackOAuthWith(ctx, conf, openFn, ln, oauth2.S256ChallengeOption(verifier))
	if err != nil {
		return "", "", err
	}
	return code, verifier, nil
}

// exchangeBingTokens redeems the authorization code for a refresh token.
func exchangeBingTokens(ctx context.Context, conf *oauth2.Config, code, verifier string) (*oauth2.Token, error) {
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("no refresh_token in response — the `offline_access` scope was not granted. Check that the application registration allows it, and that consent was not restricted")
	}
	return tok, nil
}

// saveBingCredentials persists what a sign-in produced: the client ID into the
// TOML config's [bing] table, and the refresh token into the token store.
//
// The order matters more here than it does for Google. Microsoft rotates
// refresh tokens, so the one this sign-in produced is the only one that works —
// the store is checked for writability *before* the browser flow starts (see
// the command), and again here, because a token that cannot be saved is a
// sign-in that has to be repeated.
func saveBingCredentials(path string, cfg *BingConfig, refreshToken string) error {
	if err := requireWritableStore(bingTokenPolicy.Platform); err != nil {
		return bingTokenPolicy.rotationStoreError(err)
	}
	restoreConfig, err := snapshotFile(path)
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	values := map[string]string{"client_id": cfg.ClientID}
	// The sandbox token is written down because the substitution has to outlive
	// this process: the next command reloads the file, finds a non-empty
	// developer token sitting next to environment = "sandbox", and respects it —
	// so an unpersisted switch leaves the production token in place and every
	// later call fails as error 105. Only this one value is ever written: it is
	// public, whereas a production token arriving from the environment is a
	// secret that has no business being copied into the config file.
	if cfg.Environment == bingEnvSandbox && cfg.DeveloperToken == bingSandboxDeveloperToken {
		values["developer_token"] = bingSandboxDeveloperToken
	}
	if cfg.ClientSecret != "" {
		values["client_secret"] = cfg.ClientSecret
	}
	if cfg.CustomerID != "" {
		values["customer_id"] = cfg.CustomerID
	}
	// Written even when it is the default: mergeBingConfigValues only ever sets
	// keys, so skipping production would leave an earlier `environment =
	// "sandbox"` in place and quietly point the new sign-in at the environment
	// it was just moved away from.
	values["environment"] = cfg.Environment
	if err := mergeBingConfigValues(path, values); err != nil {
		return err
	}
	if err := writeStoredToken(bingTokenPolicy.Platform, &storedToken{
		RefreshToken: refreshToken,
		UpdatedAt:    time.Now().UTC(),
		Source:       bingTokenPolicy.loginCommand(),
		ClientID:     cfg.ClientID,
	}); err != nil {
		saved := fmt.Errorf("signed in, but the refresh token could not be saved: %w — make that path writable, or set %s to a writable directory, then sign in again", err, tokenStoreEnv)
		if rerr := restoreConfig(); rerr != nil {
			return fmt.Errorf("%w (and %q could not be rolled back: %v — its client_id no longer matches the saved sign-in)", saved, path, rerr)
		}
		return saved
	}
	return nil
}

// mergeBingConfigValues merges values into the `[bing]` table of the TOML file
// at path, preserving every other key in the file. 0600 file, 0700 dir.
//
// It is the nested-table counterpart of mergeConfigValues: Bing's settings live
// under their own table, so a flat merge would write them where nothing reads
// them.
func mergeBingConfigValues(path string, values map[string]string) error {
	doc := map[string]any{}
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &doc); err != nil {
			return fmt.Errorf("read existing config %q: %w", path, err)
		}
	}
	table, _ := doc[bingPlatformName].(map[string]any)
	if table == nil {
		table = map[string]any{}
	}
	for k, v := range values {
		if v != "" {
			table[k] = v
		}
	}
	doc[bingPlatformName] = table
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := writeFileAtomic(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

var bingLoginCmd = &cobra.Command{
	Use:   "bing",
	Short: "Sign in to Microsoft Advertising via OAuth2 and save a refresh token",
	Long: "Runs Microsoft Entra's authorization-code flow with PKCE: it opens your browser,\n" +
		"captures the authorization code on localhost, exchanges it for a refresh token,\n" +
		"and saves it in the ads token store.\n\n" +
		"You need a Microsoft Entra application registration (a public/native client) whose\n" +
		"redirect URI includes http://localhost:8086, and — for production — a Microsoft\n" +
		"Advertising developer token. The sandbox uses the public developer token BBD37VB98\n" +
		"automatically; run `ads login bing --environment sandbox` to target it.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		cfg, err := loadBingLoginConfig(configPath)
		if err != nil {
			return err
		}
		if bingLoginClientID != "" {
			cfg.ClientID = bingLoginClientID
		}
		// The flag wins over the file and the environment: it is the most
		// explicit statement of which environment this sign-in is for, and the
		// two have entirely separate credentials.
		if bingLoginEnvironment != "" {
			cfg.switchEnvironment(bingLoginEnvironment)
			if !cfg.knownEnvironment() {
				return fmt.Errorf("unknown environment %q — use %q or %q", bingLoginEnvironment, bingEnvProduction, bingEnvSandbox)
			}
		}
		if cfg.ClientID == "" {
			return errors.New("no application (client) ID — pass --client-id, or set BING_ADS_CLIENT_ID. Register a public client application in the Microsoft Entra admin center (Azure portal → App registrations), supporting accounts in any organizational directory and personal Microsoft accounts, with the redirect URI " + bingLoginRedirectURL(bingLoginPort))
		}
		// Checked before the browser flow, not after: Microsoft invalidates the
		// old refresh token the moment a new one is issued, so a sign-in that
		// cannot be saved has spent a credential for nothing.
		if err := requireWritableStore(bingTokenPolicy.Platform); err != nil {
			return bingTokenPolicy.rotationStoreError(err)
		}

		fmt.Fprintln(out, "=== Microsoft Advertising sign-in ===")
		conf := bingLoginConfig(cfg, bingLoginPort)
		addr := fmt.Sprintf("127.0.0.1:%d", bingLoginPort)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w — is the port busy? pass --port (and register the matching redirect URI)", addr, err)
		}
		openFn := func(u string) error {
			// A missing browser opener (headless Linux without xdg-open) must
			// not abort the login — fall back to printing the URL.
			if err := openBrowser(u); err != nil {
				fmt.Fprintf(out, "Could not open a browser (%v).\nOpen this URL manually:\n  %s\n", err, u)
			}
			return nil
		}
		if bingLoginNoBrowser {
			openFn = func(u string) error {
				fmt.Fprintf(out, "Open this URL in your browser:\n  %s\n", u)
				return nil
			}
		} else {
			fmt.Fprintln(out, "Opening browser for Microsoft sign-in…")
		}
		fmt.Fprintf(out, "Waiting for callback on %s …\n", bingLoginRedirectURL(bingLoginPort))

		code, verifier, err := runBingLoopbackOAuth(cmd.Context(), conf, openFn, ln)
		if err != nil {
			return err
		}
		tok, err := exchangeBingTokens(cmd.Context(), conf, code, verifier)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "✓ Authorized. Exchanged code for a refresh token.")

		target, err := configWriteTarget(configPath)
		if err != nil {
			return err
		}
		if err := saveBingCredentials(target, cfg, tok.RefreshToken); err != nil {
			return err
		}
		fmt.Fprintf(out, "✓ Wrote credentials to %s\n", target)
		printBingHandoff(out, cfg)
		fmt.Fprintf(out, "Run `ads doctor %s` to verify.\n", bingPlatformName)
		return nil
	},
}

// loadBingLoginConfig loads Bing configuration for `login`. Like Google's, it
// tolerates an explicit --config path that does not exist yet: that file is the
// one login will create.
func loadBingLoginConfig(path string) (*BingConfig, error) {
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				cfg := &BingConfig{}
				cfg.finalize()
				return cfg, nil
			}
			return nil, fmt.Errorf("stat config %q: %w", path, err)
		}
	}
	return loadBingConfig(path)
}

// printBingHandoff tells the user how to carry this sign-in to CI or an MCP
// host.
//
// It cannot offer an environment variable for the refresh token, and that is
// not an omission: Microsoft issues a new refresh token on every refresh and
// invalidates the old one, so a token pasted into a variable stops working
// after the first run. The store is the only place it can live.
func printBingHandoff(out io.Writer, cfg *BingConfig) {
	fmt.Fprintln(out, "\nFor CI / MCP hosts, set:")
	fmt.Fprintln(out, "  export BING_ADS_DEVELOPER_TOKEN=\"…\"")
	fmt.Fprintf(out, "  export BING_ADS_CLIENT_ID=%q\n", cfg.ClientID)
	if cfg.CustomerID != "" {
		fmt.Fprintf(out, "  export BING_ADS_CUSTOMER_ID=%q\n", cfg.CustomerID)
	}
	if path, err := tokenStorePath(bingTokenPolicy.Platform); err == nil {
		fmt.Fprintln(out, "\nThe refresh token lives in the token store:")
		fmt.Fprintf(out, "  %s\n", path)
		fmt.Fprintln(out, "Microsoft replaces it on every refresh, so it cannot be passed in an")
		fmt.Fprintln(out, "environment variable — mount the store instead, writable, and point ads at it:")
		fmt.Fprintf(out, "  export %s=\"/path/to/tokens\"\n", tokenStoreEnv)
	}
	fmt.Fprintln(out)
}

func init() {
	bingLoginCmd.Flags().IntVar(&bingLoginPort, "port", bingLoginRedirectPort, "loopback port for the OAuth callback (must match a registered redirect URI)")
	bingLoginCmd.Flags().BoolVar(&bingLoginNoBrowser, "no-browser", false, "print the auth URL instead of opening a browser")
	bingLoginCmd.Flags().StringVar(&bingLoginClientID, "client-id", "", "Microsoft Entra application (client) ID")
	bingLoginCmd.Flags().StringVar(&bingLoginEnvironment, "environment", "", "which environment to sign in to: production (default) or sandbox")
}
