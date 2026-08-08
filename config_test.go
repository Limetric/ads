package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeCustomerID(t *testing.T) {
	cases := map[string]string{
		"123-456-7890":   "1234567890",
		" 123-456-7890 ": "1234567890",
		"1234567890":     "1234567890",
		"":               "",
	}
	for in, want := range cases {
		if got := normalizeCustomerID(in); got != want {
			t.Errorf("normalizeCustomerID(%q) = %q, want %q", in, got, want)
		}
	}
}

// clearAdsEnv blanks every GOOGLE_ADS_* var the loader reads so a developer's
// real environment can't leak into a config test (t.Setenv restores on cleanup).
func clearAdsEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GOOGLE_ADS_DEVELOPER_TOKEN", "GOOGLE_ADS_CLIENT_ID", "GOOGLE_ADS_CLIENT_SECRET",
		"GOOGLE_ADS_REFRESH_TOKEN", "GOOGLE_ADS_LOGIN_CUSTOMER_ID", "GOOGLE_ADS_CUSTOMER_ID",
		"GOOGLE_ADS_API_BASE_URL",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadConfig_EnvOnly(t *testing.T) {
	useTempState(t) // HOME/XDG → temp, so no real config.toml is found
	clearAdsEnv(t)
	t.Setenv("GOOGLE_ADS_DEVELOPER_TOKEN", "devtok")
	t.Setenv("GOOGLE_ADS_CLIENT_ID", "cid")
	t.Setenv("GOOGLE_ADS_LOGIN_CUSTOMER_ID", "123-456-7890")

	cfg, err := loadGoogleConfig("")
	if err != nil {
		t.Fatalf("loadGoogleConfig: %v", err)
	}
	if cfg.DeveloperToken != "devtok" || cfg.ClientID != "cid" {
		t.Errorf("env not applied: %+v", cfg)
	}
	if cfg.LoginCustomerID != "1234567890" {
		t.Errorf("login id not normalized: %q", cfg.LoginCustomerID)
	}
	if cfg.BaseURL != defaultBaseURL {
		t.Errorf("BaseURL = %q, want default %q", cfg.BaseURL, defaultBaseURL)
	}
}

func TestLoadConfig_TOMLOverlaidByEnv(t *testing.T) {
	clearAdsEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "developer_token = \"from-file\"\nclient_id = \"file-cid\"\nlogin_customer_id = \"999-999-9999\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_ADS_DEVELOPER_TOKEN", "from-env") // overrides the file

	cfg, err := loadGoogleConfig(path)
	if err != nil {
		t.Fatalf("loadGoogleConfig: %v", err)
	}
	if cfg.DeveloperToken != "from-env" {
		t.Errorf("env should override file: got %q", cfg.DeveloperToken)
	}
	if cfg.ClientID != "file-cid" {
		t.Errorf("file-only value should remain: got %q", cfg.ClientID)
	}
	if cfg.LoginCustomerID != "9999999999" {
		t.Errorf("login id should be normalized: got %q", cfg.LoginCustomerID)
	}
}

func TestLoadConfig_DefaultCustomerID(t *testing.T) {
	clearAdsEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("default_customer_id = \"111-111-1111\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadGoogleConfig(path)
	if err != nil {
		t.Fatalf("loadGoogleConfig: %v", err)
	}
	if cfg.DefaultCustomerID != "1111111111" {
		t.Errorf("file default should be normalized: got %q", cfg.DefaultCustomerID)
	}

	// The env var overrides the file and is normalized too.
	t.Setenv("GOOGLE_ADS_CUSTOMER_ID", "222-222-2222")
	cfg, err = loadGoogleConfig(path)
	if err != nil {
		t.Fatalf("loadGoogleConfig: %v", err)
	}
	if cfg.DefaultCustomerID != "2222222222" {
		t.Errorf("env should override file: got %q", cfg.DefaultCustomerID)
	}
}

func TestResolveCustomerID(t *testing.T) {
	withDefault := &Client{cfg: &GoogleConfig{DefaultCustomerID: "1111111111"}}
	noDefault := &Client{cfg: &GoogleConfig{}}

	tests := []struct {
		name     string
		client   *Client
		explicit string
		want     string
		wantErr  bool
	}{
		{name: "explicit wins over default", client: withDefault, explicit: "222-222-2222", want: "2222222222"},
		{name: "default fills in", client: withDefault, want: "1111111111"},
		{name: "blank explicit value counts as not given", client: withDefault, explicit: "   ", want: "1111111111"},
		// A non-blank value that normalizes to nothing must error, never
		// silently redirect the call to the default account.
		{name: "garbage explicit value errors despite default", client: withDefault, explicit: "---", wantErr: true},
		{name: "no default and no explicit errors", client: noDefault, wantErr: true},
		{name: "nil client with explicit id", client: nil, explicit: "333-333-3333", want: "3333333333"},
		{name: "nil client without explicit id errors", client: nil, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.client.resolveCustomerID(tt.explicit)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveCustomerID(%q) should error, got %q", tt.explicit, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCustomerID(%q): %v", tt.explicit, err)
			}
			if got != tt.want {
				t.Errorf("resolveCustomerID(%q) = %q, want %q", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestLoadConfig_MissingFileErrors(t *testing.T) {
	clearAdsEnv(t)
	if _, err := loadGoogleConfig("/no/such/config-file.toml"); err == nil {
		t.Fatal("expected error for an explicit but unreadable config path")
	}
}

func TestConfig_Validate(t *testing.T) {
	missing := (&GoogleConfig{BaseURL: defaultBaseURL}).validate()
	if missing == nil {
		t.Fatal("expected missing-credentials error against production base URL")
	}
	for _, want := range []string{
		"GOOGLE_ADS_DEVELOPER_TOKEN", "GOOGLE_ADS_CLIENT_ID",
		"GOOGLE_ADS_CLIENT_SECRET", "GOOGLE_ADS_REFRESH_TOKEN",
	} {
		if !strings.Contains(missing.Error(), want) {
			t.Errorf("error should name %s: %v", want, missing)
		}
	}
	// A non-production base URL relaxes the credential requirement (test mode).
	if err := (&GoogleConfig{BaseURL: "http://localhost:1"}).validate(); err != nil {
		t.Errorf("test base URL should skip validation: %v", err)
	}
	// Complete production credentials validate.
	full := &GoogleConfig{BaseURL: defaultBaseURL, DeveloperToken: "d", ClientID: "c", ClientSecret: "s", RefreshToken: "r"}
	if err := full.validate(); err != nil {
		t.Errorf("complete credentials should validate: %v", err)
	}
}

func TestConfig_IsTest(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:1":                    true, // plain HTTP → test
		"http://127.0.0.1:39217":                true, // httptest server → test
		"http://[::1]:8080":                     true, // IPv6 loopback → test
		defaultBaseURL:                          false,
		"":                                      false,
		"https://googleads.googleapis.com/v24":  false, // future version: REAL credentials (issue #5)
		"https://googleads.googleapis.com/v23/": false, // trailing slash: REAL credentials (issue #5)
		"https://ads-proxy.example.com/v23":     false, // custom HTTPS endpoint: REAL credentials
		"http://internal-proxy:8080/v23":        false, // remote plain-HTTP: REAL credentials, not test mode
	}
	for baseURL, want := range cases {
		if got := (&GoogleConfig{BaseURL: baseURL}).isTest(); got != want {
			t.Errorf("isTest(%q) = %t, want %t", baseURL, got, want)
		}
	}
}

func TestFinalize_NormalizesBaseURL(t *testing.T) {
	clearAdsEnv(t)
	cfg := &GoogleConfig{BaseURL: "https://googleads.googleapis.com/v23/"}
	cfg.finalize()
	if cfg.BaseURL != "https://googleads.googleapis.com/v23" {
		t.Errorf("trailing slash should be trimmed, got %q", cfg.BaseURL)
	}
}

func TestResolveConfigPath(t *testing.T) {
	if p, _ := resolveConfigPath("/explicit/path.toml"); p != "/explicit/path.toml" {
		t.Errorf("explicit path should pass through, got %q", p)
	}
	useTempState(t) // HOME → temp dir with no config.toml
	if p, _ := resolveConfigPath(""); p != "" {
		t.Errorf("a missing default config should resolve to empty, got %q", p)
	}
}
