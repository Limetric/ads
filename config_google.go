package main

import (
	"fmt"
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
	// ClientID / ClientSecret / RefreshToken are the installed-app OAuth2
	// credentials used to mint access tokens (see auth.go).
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
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
}

// loadGoogleConfig reads Google's configuration from the given file (optional)
// and overlays environment variables on top. An empty path means "use the
// default path if it exists, otherwise env only".
func loadGoogleConfig(path string) (*GoogleConfig, error) {
	cfg := &GoogleConfig{}
	if err := decodeConfigFile(path, cfg); err != nil {
		return nil, err
	}
	cfg.finalize()
	return cfg, nil
}

// finalize overlays environment variables on top of any file values and applies
// defaults/normalization. It is the shared tail of config loading so callers
// (e.g. loadLoginConfig) can build an env-only config without a file.
func (cfg *GoogleConfig) finalize() {
	overlayEnv(map[string]*string{
		"GOOGLE_ADS_DEVELOPER_TOKEN":   &cfg.DeveloperToken,
		"GOOGLE_ADS_CLIENT_ID":         &cfg.ClientID,
		"GOOGLE_ADS_CLIENT_SECRET":     &cfg.ClientSecret,
		"GOOGLE_ADS_REFRESH_TOKEN":     &cfg.RefreshToken,
		"GOOGLE_ADS_LOGIN_CUSTOMER_ID": &cfg.LoginCustomerID,
		"GOOGLE_ADS_CUSTOMER_ID":       &cfg.DefaultCustomerID,
		"GOOGLE_ADS_API_BASE_URL":      &cfg.BaseURL,
	})
	cfg.LoginCustomerID = normalizeCustomerID(cfg.LoginCustomerID)
	cfg.DefaultCustomerID = normalizeCustomerID(cfg.DefaultCustomerID)
	cfg.BaseURL = normalizeBaseURL(cfg.BaseURL, defaultBaseURL)
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
		missing = append(missing, "GOOGLE_ADS_REFRESH_TOKEN")
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
