package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearBingEnv blanks every BING_ADS_* var the loader reads so a developer's
// real environment can't leak into a config test.
func clearBingEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BING_ADS_DEVELOPER_TOKEN", "BING_ADS_CLIENT_ID", "BING_ADS_CLIENT_SECRET",
		"BING_ADS_CUSTOMER_ID", "BING_ADS_ACCOUNT_ID", "BING_ADS_ENVIRONMENT",
		"BING_ADS_API_BASE_URL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadBingConfig_TableAndEnvOverlay(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// Google's keys sit at the top level; Bing's live in their own table, and
	// the two must not read each other's values.
	if err := os.WriteFile(path, []byte(`
developer_token = "google-token"
client_id = "google-client"

[bing]
developer_token = "bing-token"
client_id = "bing-client"
customer_id = "555-111"
default_account_id = "999222"
environment = "sandbox"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BING_ADS_ACCOUNT_ID", "123456") // env wins over the file

	cfg, err := loadBingConfig(path)
	if err != nil {
		t.Fatalf("loadBingConfig: %v", err)
	}
	if cfg.ClientID != "bing-client" {
		t.Errorf("read Google's top-level keys instead of the [bing] table: %+v", cfg)
	}
	// The fixture selects the sandbox, where the developer token is a constant
	// rather than whatever the file says — the file's own value is untouched
	// and comes back when the environment does.
	if cfg.DeveloperToken != bingSandboxDeveloperToken {
		t.Errorf("developer token = %q, want the sandbox constant", cfg.DeveloperToken)
	}
	if cfg.CustomerID != "555111" {
		t.Errorf("CustomerID = %q, want separators stripped", cfg.CustomerID)
	}
	if cfg.DefaultAccountID != "123456" {
		t.Errorf("DefaultAccountID = %q, want the environment to win", cfg.DefaultAccountID)
	}
	if cfg.Environment != bingEnvSandbox {
		t.Errorf("Environment = %q", cfg.Environment)
	}
}

func TestBingConfig_SandboxDeveloperTokenIsFilledIn(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	t.Setenv("BING_ADS_ENVIRONMENT", "Sandbox") // case-insensitive
	cfg, err := loadBingConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != bingEnvSandbox {
		t.Fatalf("Environment = %q", cfg.Environment)
	}
	if cfg.DeveloperToken != bingSandboxDeveloperToken {
		t.Errorf("DeveloperToken = %q, want the universal sandbox token", cfg.DeveloperToken)
	}
	// The sandbox token is public, so it must not make a fresh install look
	// like a configured Bing setup.
	if cfg.configured() {
		t.Error("the auto-filled sandbox token should not count as configuration")
	}
}

func TestBingConfig_Validate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          BingConfig
		refreshToken string
		wantErr      string
	}{
		{
			name:         "complete",
			cfg:          BingConfig{DeveloperToken: "dev", ClientID: "cid", Environment: bingEnvProduction},
			refreshToken: "rt",
		},
		{
			name:    "missing everything names the login command",
			cfg:     BingConfig{Environment: bingEnvProduction},
			wantErr: "ads login bing",
		},
		{
			name:         "missing developer token",
			cfg:          BingConfig{ClientID: "cid", Environment: bingEnvProduction},
			refreshToken: "rt",
			wantErr:      "BING_ADS_DEVELOPER_TOKEN",
		},
		{
			name:         "unknown environment",
			cfg:          BingConfig{DeveloperToken: "dev", ClientID: "cid", Environment: "staging"},
			refreshToken: "rt",
			wantErr:      "unknown bing environment",
		},
		{
			// A loopback base URL is a test server: credentials are not checked,
			// which is what lets the suite run offline.
			name: "loopback base URL relaxes every check",
			cfg:  BingConfig{BaseURL: "http://127.0.0.1:8080", Environment: bingEnvProduction},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate(tc.refreshToken)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("validate: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected an error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestBingConfig_IsTest(t *testing.T) {
	tests := []struct {
		baseURL string
		want    bool
	}{
		{"", false},
		{"http://127.0.0.1:1234", true},
		{"http://localhost:1234", true},
		// A remote override is a real target — a regional endpoint or a proxy —
		// and must keep the user's real credentials (issue #5).
		{"https://campaign.api.sandbox.bingads.microsoft.com", false},
	}
	for _, tc := range tests {
		cfg := BingConfig{BaseURL: tc.baseURL}
		if got := cfg.isTest(); got != tc.want {
			t.Errorf("isTest(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestBingConfig_TokenPolicyRotates(t *testing.T) {
	// Entra replaces the refresh token on every refresh. If this flag is ever
	// flipped, an unwritable store stops being an error and a signed-in user
	// silently loses their credential on the first refresh.
	if !bingTokenPolicy.Rotates {
		t.Error("Bing's refresh token rotates; the policy must say so")
	}
	if bingTokenPolicy.Platform != bingPlatformName {
		t.Errorf("policy platform = %q", bingTokenPolicy.Platform)
	}
}

func TestBingConfig_OAuthCarriesScopes(t *testing.T) {
	cfg := &BingConfig{ClientID: "cid"}
	oc := cfg.oauth("rt")
	if len(oc.Scopes) == 0 {
		t.Fatal("Entra wants the scopes on every refresh; oauthClient must carry them")
	}
	joined := strings.Join(oc.Scopes, " ")
	for _, want := range []string{"msads.manage", "offline_access"} {
		if !strings.Contains(joined, want) {
			t.Errorf("scopes %q missing %q", joined, want)
		}
	}
}

func TestBingServiceURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  *BingConfig
		svc  bingService
		op   string
		want string
	}{
		{
			name: "production",
			cfg:  &BingConfig{Environment: bingEnvProduction},
			svc:  bingCampaignService,
			op:   "Campaigns/QueryByAccountId",
			want: "https://campaign.api.bingads.microsoft.com/CampaignManagement/v13/Campaigns/QueryByAccountId",
		},
		{
			name: "sandbox has its own host",
			cfg:  &BingConfig{Environment: bingEnvSandbox},
			svc:  bingCustomerService,
			op:   "AccountsInfo/Query",
			want: "https://clientcenter.api.sandbox.bingads.microsoft.com/CustomerManagement/v13/AccountsInfo/Query",
		},
		{
			name: "reporting",
			cfg:  &BingConfig{Environment: bingEnvProduction},
			svc:  bingReportService,
			op:   "GenerateReport/Submit",
			want: "https://reporting.api.bingads.microsoft.com/Reporting/v13/GenerateReport/Submit",
		},
		{
			// The override replaces the host but keeps the service path, so one
			// httptest server can stand in for all three and still route.
			name: "base URL override keeps the service path",
			cfg:  &BingConfig{BaseURL: "http://127.0.0.1:9999"},
			svc:  bingCampaignService,
			op:   "Campaigns",
			want: "http://127.0.0.1:9999/CampaignManagement/v13/Campaigns",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.svc.url(tc.cfg, tc.op); got != tc.want {
				t.Errorf("url = %q, want %q", got, tc.want)
			}
		})
	}
}
