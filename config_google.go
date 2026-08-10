package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/oauth2/google"
)

// GoogleConfig holds everything needed to talk to the Google Ads API. It is the
// Google platform's slice of the shared configuration: the core loader
// (config.go) hands it the TOML file, and it overlays its own GOOGLE_ADS_* env
// vars on top.
//
// The TOML keys are unprefixed for historical reasons — this file was the whole
// config before the platform split. A second platform gets its own struct with
// its own table; reworking the storage layout is tracked separately.
type GoogleConfig struct {
	// DeveloperToken is the Google Ads API developer token.
	DeveloperToken string `toml:"developer_token"`
	// ClientID / ClientSecret are the installed-app OAuth2 credentials used to
	// mint access tokens (see auth.go).
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	// RefreshToken is the grant access tokens are minted from. The TOML key is
	// deprecated: it is read once as a seed for the token store and then
	// removed from the file (see token_store.go). After resolveRefreshToken
	// this field holds the effective token, whatever it was sourced from.
	RefreshToken string `toml:"refresh_token"`
	// LoginCustomerID is the manager (MCC) account used for the
	// `login-customer-id` header. Optional; dashes are stripped.
	LoginCustomerID string `toml:"login_customer_id"`
	// DefaultCustomerID is the customer ID used when a command/tool call does
	// not pass one explicitly. Optional; dashes are stripped. Set it with
	// `ads config google set-customer` or GOOGLE_ADS_CUSTOMER_ID.
	DefaultCustomerID string `toml:"default_customer_id"`
	// BaseURL overrides the API base (default defaultBaseURL). This is the
	// per-platform base-URL override: tests point it at an httptest server, and
	// a regional endpoint would go here too.
	BaseURL string `toml:"base_url"`

	// --- unexported bookkeeping for the refresh-token deprecation path ---

	// sourcePath is the config file the TOML values came from, empty when no
	// file was found. resolveRefreshToken needs it to retire the deprecated
	// refresh_token key from the exact file it was read out of.
	sourcePath string
	// tomlRefreshToken / envRefreshToken are the deprecated seeds as found,
	// before either won the overlay. Both are kept because both have to be
	// reported and (where possible) retired.
	tomlRefreshToken string
	envRefreshToken  string
	// refreshTokenResolved guards resolveRefreshToken so the store is consulted
	// — and its deprecation notices printed — at most once per config.
	refreshTokenResolved bool
}

// googleRefreshTokenEnv is the deprecated environment seed for Google's refresh
// token. It is accepted as a one-time seed into the token store through the 0.x
// line; see token_store.go for why it cannot be the credential's home.
const googleRefreshTokenEnv = "GOOGLE_ADS_REFRESH_TOKEN"

// googleTokenPolicy is Google's slice of the shared token store. Google's
// refresh tokens are long-lived and static — a refresh returns a new access
// token and the same refresh token — so an unwritable store costs nothing and
// must not break a setup that works today.
var googleTokenPolicy = tokenPolicy{Platform: "google", Rotates: false}

// loadGoogleConfig reads Google's configuration from the given file (optional)
// and overlays environment variables on top. An empty path means "use the
// default path if it exists, otherwise env only".
func loadGoogleConfig(path string) (*GoogleConfig, error) {
	cfg := &GoogleConfig{}
	resolved, err := decodeConfigFile(path, cfg)
	if err != nil {
		return nil, err
	}
	cfg.sourcePath = resolved
	cfg.finalize()
	return cfg, nil
}

// finalize overlays environment variables on top of any file values and applies
// defaults/normalization. It is the shared tail of config loading so callers
// (e.g. loadLoginConfig) can build an env-only config without a file.
func (cfg *GoogleConfig) finalize() {
	// Captured before the overlay: which of the two deprecated sources won is
	// not enough — a stale copy in the loser has to be reported too.
	cfg.tomlRefreshToken = strings.TrimSpace(cfg.RefreshToken)
	overlayEnv(map[string]*string{
		"GOOGLE_ADS_DEVELOPER_TOKEN":   &cfg.DeveloperToken,
		"GOOGLE_ADS_CLIENT_ID":         &cfg.ClientID,
		"GOOGLE_ADS_CLIENT_SECRET":     &cfg.ClientSecret,
		googleRefreshTokenEnv:          &cfg.RefreshToken,
		"GOOGLE_ADS_LOGIN_CUSTOMER_ID": &cfg.LoginCustomerID,
		"GOOGLE_ADS_CUSTOMER_ID":       &cfg.DefaultCustomerID,
		"GOOGLE_ADS_API_BASE_URL":      &cfg.BaseURL,
	})
	cfg.envRefreshToken = strings.TrimSpace(os.Getenv(googleRefreshTokenEnv))
	cfg.LoginCustomerID = normalizeCustomerID(cfg.LoginCustomerID)
	cfg.DefaultCustomerID = normalizeCustomerID(cfg.DefaultCustomerID)
	cfg.BaseURL = normalizeBaseURL(cfg.BaseURL, defaultBaseURL)
}

// refreshTokenSeeds lists the deprecated sources a refresh token may still
// arrive from, in the same precedence order as the rest of the configuration
// (environment over file). Only the config file can be rewritten, so only it
// carries a Retire hook.
func (c *GoogleConfig) refreshTokenSeeds() []tokenSeed {
	return []tokenSeed{
		{Value: c.envRefreshToken, Origin: googleRefreshTokenEnv},
		{
			Value:  c.tomlRefreshToken,
			Origin: fmt.Sprintf("refresh_token in %s", c.sourcePath),
			Retire: func() error { return deleteConfigKey(c.sourcePath, "refresh_token") },
		},
	}
}

// savedSignInUsableWith reports whether the saved Google sign-in can be reused
// with clientID. A refresh token only works with the OAuth client that minted
// it, so offering to reuse one from a different client would stage a sign-in
// that cannot authenticate — and would overwrite the accurate binding on the
// way. An unrecorded binding (a token seeded from a deprecated source) is not
// evidence of a mismatch, so it does not block reuse.
func googleSavedSignInUsableWith(clientID string) bool {
	stored, err := readStoredToken(googleTokenPolicy.Platform)
	if err != nil || stored == nil || stored.ClientID == "" || clientID == "" {
		return true
	}
	return stored.ClientID == clientID
}

// resolveRefreshToken points RefreshToken at the token store, migrating a
// deprecated environment or config-file value into it on first use.
//
// It writes to disk — the store, and the config file it strips the retired key
// from — so it runs only where ads is about to authenticate. Read-only surfaces
// (`ads config show`) read the store directly instead. In test mode there is no
// real sign-in to resolve, so it does nothing.
func (c *GoogleConfig) resolveRefreshToken() error {
	if c.refreshTokenResolved || c.isTest() {
		return nil
	}
	token, err := resolveRefreshToken(googleTokenPolicy, c.ClientID, c.refreshTokenSeeds())
	if err != nil {
		return err
	}
	c.RefreshToken = token
	c.refreshTokenResolved = true
	return nil
}

// validate reports whether the config is usable for real API calls. It is
// intentionally lenient when BaseURL points away from production (tests).
func (c *GoogleConfig) validate() error {
	if c.isTest() {
		return nil
	}
	var missing []string
	if c.DeveloperToken == "" {
		missing = append(missing, "GOOGLE_ADS_DEVELOPER_TOKEN")
	}
	if c.ClientID == "" {
		missing = append(missing, "GOOGLE_ADS_CLIENT_ID")
	}
	if c.ClientSecret == "" {
		missing = append(missing, "GOOGLE_ADS_CLIENT_SECRET")
	}
	if c.RefreshToken == "" {
		// Not an env var any more: the refresh token comes from the token
		// store, and `ads login google` is the only thing that should put it
		// there. GOOGLE_ADS_REFRESH_TOKEN still seeds it, but naming a
		// deprecated variable as the fix would be pointing at the exit.
		missing = append(missing, "a saved sign-in (run `"+googleTokenPolicy.loginCommand()+"`)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing credentials: %s — set them in the environment or a --config TOML file (see `ads doctor google`)", strings.Join(missing, ", "))
	}
	return nil
}

// isTest reports whether we're pointed at a local/offline base URL, in which
// case auth and credential checks are relaxed.
func (c *GoogleConfig) isTest() bool {
	if c.BaseURL == "" || c.BaseURL == defaultBaseURL {
		return false
	}
	return isLoopbackURL(c.BaseURL)
}

// oauth describes Google's installed-app OAuth2 flow for the shared token
// source in auth.go. This is the whole of what auth.go needs to know about
// Google — a second platform supplies its own endpoint the same way.
func (c *GoogleConfig) oauth() oauthClient {
	return oauthClient{
		tokenPolicy:  googleTokenPolicy,
		Endpoint:     googleOAuthEndpoint,
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RefreshToken: c.RefreshToken,
		Offline:      c.isTest(),
	}
}

// googleOAuthEndpoint is the production OAuth endpoint. It is a package var so
// tests can point the login and token flows at a fake token server. This is the
// only place Google's endpoint is named — auth.go takes it from oauthClient.
var googleOAuthEndpoint = google.Endpoint

// normalizeCustomerID strips dashes and whitespace ("123-456-7890" -> "1234567890").
func normalizeCustomerID(id string) string {
	return strings.ReplaceAll(strings.TrimSpace(id), "-", "")
}
