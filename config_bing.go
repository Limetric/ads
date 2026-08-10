package main

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
)

// BingConfig holds everything needed to talk to the Microsoft Advertising
// (Bing Ads) API v13. It is Bing's slice of the shared configuration: the core
// loader (config.go) hands it the TOML file, and it overlays its own BING_ADS_*
// environment variables on top.
//
// Unlike Google's — whose keys sit at the top level for historical reasons —
// Bing's live under a `[bing]` table, which is where every platform added from
// here on should put its settings.
type BingConfig struct {
	// DeveloperToken is the Bing Ads API developer token. In the sandbox it is
	// the universal public token, filled in automatically (see finalize).
	DeveloperToken string `toml:"developer_token"`
	// ClientID is the Microsoft Entra application (client) ID. ClientSecret is
	// set only for a registered *web* app; a native/public app has none, and
	// sending one is an error Entra names explicitly ("Public clients can't
	// send a client secret"), so it stays empty for the `ads login bing` flow.
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	// CustomerID is the manager account (customer) the user operates from — the
	// `CustomerId` header. It is Bing's analogue of Google's login customer ID.
	CustomerID string `toml:"customer_id"`
	// DefaultAccountID is the ad account used when a command or tool call does
	// not name one — the `CustomerAccountId` header. Set it with
	// `ads config bing set-account` or BING_ADS_ACCOUNT_ID.
	DefaultAccountID string `toml:"default_account_id"`
	// Environment selects the API and sign-in endpoints: "production" (default)
	// or "sandbox". The two have entirely separate credentials, and a token
	// minted for one is rejected by the other with error 105.
	Environment string `toml:"environment"`
	// BaseURL overrides the API host for every service, keeping each service's
	// path (`/CampaignManagement/v13/…`) so one test server can route them all.
	// Tests point it at an httptest server; a proxy would go here too.
	BaseURL string `toml:"base_url"`
}

// bingConfigFile is the shape of the shared TOML file as far as Bing cares:
// its own table, and nothing else.
type bingConfigFile struct {
	Bing BingConfig `toml:"bing"`
}

// bingSandboxDeveloperToken is the universal sandbox developer token, published
// by Microsoft for everyone. Filling it in automatically is the difference
// between "sandbox works out of the box" and a setup error that reads like a
// missing credential.
const bingSandboxDeveloperToken = "BBD37VB98"

const (
	bingEnvProduction = "production"
	bingEnvSandbox    = "sandbox"
)

// bingScopes are the delegated permissions `ads login bing` asks for.
// msads.manage is the Microsoft Advertising API itself; offline_access is what
// makes Entra return a refresh token at all.
var bingScopes = []string{"https://ads.microsoft.com/msads.manage", "offline_access"}

// bingTokenPolicy is Bing's slice of the shared token store. Microsoft Entra
// hands back a *new* refresh token on every refresh and expects the old one to
// be discarded, so the store must be writable — a run that cannot save the
// replacement is a run that spends the only credential it has.
var bingTokenPolicy = tokenPolicy{Platform: bingPlatformName, Rotates: true}

// loadBingConfig reads Bing's configuration from the given file (optional) and
// overlays environment variables on top. An empty path means "use the default
// path if it exists, otherwise env only".
func loadBingConfig(path string) (*BingConfig, error) {
	var file bingConfigFile
	if _, err := decodeConfigFile(path, &file); err != nil {
		return nil, err
	}
	cfg := file.Bing
	cfg.finalize()
	return &cfg, nil
}

// finalize overlays environment variables on top of any file values and applies
// defaults and normalization. It is the shared tail of config loading, so a
// caller can build an env-only config without a file.
func (c *BingConfig) finalize() {
	overlayEnv(map[string]*string{
		"BING_ADS_DEVELOPER_TOKEN": &c.DeveloperToken,
		"BING_ADS_CLIENT_ID":       &c.ClientID,
		"BING_ADS_CLIENT_SECRET":   &c.ClientSecret,
		"BING_ADS_CUSTOMER_ID":     &c.CustomerID,
		"BING_ADS_ACCOUNT_ID":      &c.DefaultAccountID,
		"BING_ADS_ENVIRONMENT":     &c.Environment,
		"BING_ADS_API_BASE_URL":    &c.BaseURL,
	})
	c.DeveloperToken = strings.TrimSpace(c.DeveloperToken)
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.ClientSecret = strings.TrimSpace(c.ClientSecret)
	c.CustomerID = normalizeBingID(c.CustomerID)
	c.DefaultAccountID = normalizeBingID(c.DefaultAccountID)
	c.Environment = normalizeBingEnvironment(c.Environment)
	c.BaseURL = normalizeBaseURL(c.BaseURL, "")
	if c.DeveloperToken == "" && c.Environment == bingEnvSandbox {
		c.DeveloperToken = bingSandboxDeveloperToken
	}
}

// normalizeBingEnvironment maps what a user might write to the two environments
// that exist. An unrecognized value is left alone so validate can name it.
func normalizeBingEnvironment(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", bingEnvProduction, "prod":
		return bingEnvProduction
	case bingEnvSandbox:
		return bingEnvSandbox
	default:
		return strings.ToLower(strings.TrimSpace(env))
	}
}

// normalizeBingID strips whitespace and separators from an account or customer
// ID. Microsoft's UI shows them as bare numbers, but they get pasted out of
// URLs and spreadsheets with stray formatting.
func normalizeBingID(id string) string {
	return strings.ReplaceAll(strings.TrimSpace(id), "-", "")
}

// validBingID reports whether id (already normalized) is a plausible Bing
// account or customer ID: a non-empty run of digits. Unlike Google's, the IDs
// have no fixed width.
func validBingID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseInt64ID converts an already-validated Bing ID to the numeric form some
// request bodies want. Most of the API accepts an ID as a JSON string, but the
// report scope takes real numbers.
func parseInt64ID(field, id string) (int64, error) {
	normalized := normalizeBingID(id)
	if !validBingID(normalized) {
		return 0, fmt.Errorf("invalid %s %q — expected digits", field, id)
	}
	n, err := strconv.ParseInt(normalized, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", field, id, err)
	}
	return n, nil
}

// isTest reports whether we're pointed at a local/offline base URL, in which
// case auth and credential checks are relaxed.
func (c *BingConfig) isTest() bool {
	return c.BaseURL != "" && isLoopbackURL(c.BaseURL)
}

// configured reports whether anything Bing-specific has been set up. It decides
// whether a plain `ads doctor` includes Bing at all, so it asks about intent
// (is there a client ID, a developer token, a sign-in?) rather than about
// completeness.
func (c *BingConfig) configured() bool {
	if c.ClientID != "" || c.ClientSecret != "" || c.CustomerID != "" || c.DefaultAccountID != "" {
		return true
	}
	// The sandbox developer token is filled in for everyone, so it says nothing
	// about intent; an explicitly set one does.
	if c.DeveloperToken != "" && c.DeveloperToken != bingSandboxDeveloperToken {
		return true
	}
	if tok, err := readStoredToken(bingTokenPolicy.Platform); err == nil && tok != nil {
		return true
	}
	return false
}

// resolveRefreshToken points the config at the saved sign-in.
//
// Bing has no deprecated seeds to migrate: it arrived after the token store, so
// `ads login bing` is the only thing that ever writes one. A rotating refresh
// token could not have been supplied by an environment variable anyway — the
// first refresh would replace it, and a process cannot write back to its own
// environment (see token_store.go).
func (c *BingConfig) resolveRefreshToken() (string, error) {
	if c.isTest() {
		return "", nil
	}
	return resolveRefreshToken(bingTokenPolicy, c.ClientID, nil)
}

// validate reports whether the config is usable for real API calls. Like
// Google's, it is lenient when BaseURL points at a local test server.
func (c *BingConfig) validate(refreshToken string) error {
	if c.isTest() {
		return nil
	}
	if c.Environment != bingEnvProduction && c.Environment != bingEnvSandbox {
		return fmt.Errorf("unknown bing environment %q — use %q or %q (BING_ADS_ENVIRONMENT)", c.Environment, bingEnvProduction, bingEnvSandbox)
	}
	var missing []string
	if c.DeveloperToken == "" {
		missing = append(missing, "BING_ADS_DEVELOPER_TOKEN")
	}
	if c.ClientID == "" {
		missing = append(missing, "BING_ADS_CLIENT_ID (the Microsoft Entra application ID)")
	}
	if refreshToken == "" {
		missing = append(missing, "a saved sign-in (run `"+bingTokenPolicy.loginCommand()+"`)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing credentials: %s — set them in the environment or a --config TOML file (see `ads doctor bing`)", strings.Join(missing, ", "))
	}
	return nil
}

// oauth describes Bing's authorization-code flow for the shared token source in
// auth.go. The scopes are carried through because Entra wants them on every
// refresh, not just at sign-in.
func (c *BingConfig) oauth(refreshToken string) oauthClient {
	return oauthClient{
		tokenPolicy:  bingTokenPolicy,
		Endpoint:     bingOAuthEndpoint(),
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RefreshToken: refreshToken,
		Scopes:       bingScopes,
		Offline:      c.isTest(),
	}
}

// bingOAuthEndpointOverride lets tests point the sign-in and refresh flows at a
// fake token server. It is the only way any of Microsoft's endpoints are named
// outside this function.
var bingOAuthEndpointOverride *oauth2.Endpoint

// bingOAuthEndpoint is the Microsoft identity platform endpoint. The `common`
// tenant is what supports both personal Microsoft accounts and work/school
// accounts, which is what Microsoft Advertising users have.
//
// Sandbox and production share it: the sandbox difference is the *application
// registration* and the API hosts, not the authority.
func bingOAuthEndpoint() oauth2.Endpoint {
	if bingOAuthEndpointOverride != nil {
		return *bingOAuthEndpointOverride
	}
	return oauth2.Endpoint{
		AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
	}
}
