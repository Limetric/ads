package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"golang.org/x/oauth2"
)

func TestCLI_LoginAuthorizedUser(t *testing.T) {
	useTempState(t) // HOME/XDG → temp, so the default config path is sandboxed
	clearAdsEnv(t)

	credPath := t.TempDir() + "/creds.json"
	if err := writeFileHelper(credPath, `{"type":"authorized_user","client_id":"cid","client_secret":"csec","refresh_token":"rtok"}`); err != nil {
		t.Fatal(err)
	}

	// Pre-create the default config with an existing field to verify the merge.
	target, err := configWriteTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileHelper(target, "developer_token = \"pre\"\n"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "login", "google", "--credentials", credPath)
	if err != nil {
		t.Fatalf("login failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Wrote credentials") || !strings.Contains(out, "GOOGLE_ADS_REFRESH_TOKEN") {
		t.Errorf("unexpected output:\n%s", out)
	}

	var cfg GoogleConfig
	if _, err := toml.DecodeFile(target, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DeveloperToken != "pre" {
		t.Errorf("existing developer_token not preserved: %+v", cfg)
	}
	if cfg.ClientID != "cid" || cfg.RefreshToken != "rtok" {
		t.Errorf("oauth fields not written: %+v", cfg)
	}
}

func TestCLI_LoginAuthorizedUser_FreshConfigPath(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	t.Cleanup(func() { configPath = "" }) // --config leaks into the global; reset

	dir := t.TempDir()
	credPath := dir + "/creds.json"
	if err := writeFileHelper(credPath, `{"type":"authorized_user","client_id":"cid","client_secret":"csec","refresh_token":"rtok"}`); err != nil {
		t.Fatal(err)
	}
	cfgPath := dir + "/fresh/config.toml" // neither the file nor its dir exists yet

	out, err := runCLI(t, "login", "google", "--credentials", credPath, "--config", cfgPath)
	if err != nil {
		t.Fatalf("login failed: %v\n%s", err, out)
	}
	var cfg GoogleConfig
	if _, err := toml.DecodeFile(cfgPath, &cfg); err != nil {
		t.Fatalf("config not created at explicit --config path: %v", err)
	}
	if cfg.ClientID != "cid" || cfg.RefreshToken != "rtok" {
		t.Errorf("creds not written: %+v", cfg)
	}
}

func TestParseCredentialsJSON(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		wantKind    string
		wantID      string
		wantSecret  string
		wantRefresh string
		wantErr     bool
	}{
		{
			name:     "installed desktop app",
			data:     `{"installed":{"client_id":"cid","client_secret":"csec"}}`,
			wantKind: "installed", wantID: "cid", wantSecret: "csec",
		},
		{
			name:     "web application",
			data:     `{"web":{"client_id":"wid","client_secret":"wsec"}}`,
			wantKind: "web", wantID: "wid", wantSecret: "wsec",
		},
		{
			name:     "authorized_user",
			data:     `{"type":"authorized_user","client_id":"aid","client_secret":"asec","refresh_token":"rtok"}`,
			wantKind: "authorized_user", wantID: "aid", wantSecret: "asec", wantRefresh: "rtok",
		},
		{
			name:    "unknown shape",
			data:    `{"something":{}}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			data:    `not json`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCredentialsJSON([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.kind != tt.wantKind || got.clientID != tt.wantID || got.clientSecret != tt.wantSecret || got.refreshToken != tt.wantRefresh {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestResolveLoginCreds_PrefersFile(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/creds.json"
	if err := writeFileHelper(p, `{"installed":{"client_id":"fileid","client_secret":"filesec"}}`); err != nil {
		t.Fatal(err)
	}
	cfg := &GoogleConfig{ClientID: "envid", ClientSecret: "envsec"}
	got, err := resolveLoginCreds(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	if got.clientID != "fileid" {
		t.Fatalf("expected file creds to win, got %+v", got)
	}
}

func TestResolveLoginCreds_FallsBackToConfig(t *testing.T) {
	cfg := &GoogleConfig{ClientID: "envid", ClientSecret: "envsec"}
	got, err := resolveLoginCreds(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.clientID != "envid" || got.kind != "config" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveLoginCreds_NoneFound(t *testing.T) {
	_, err := resolveLoginCreds(&GoogleConfig{}, "")
	if err == nil || !strings.Contains(err.Error(), "--credentials") {
		t.Fatalf("expected actionable error, got %v", err)
	}
}

// writeFileHelper is a tiny test helper, defined once in login_test.go.
func writeFileHelper(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestWriteOAuthToConfig_PreservesExisting(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.toml"
	if err := writeFileHelper(path, "developer_token = \"devtok\"\nlogin_customer_id = \"123\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeOAuthToConfig(path, clientCreds{clientID: "cid", clientSecret: "csec"}, "rtok"); err != nil {
		t.Fatal(err)
	}

	var cfg GoogleConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DeveloperToken != "devtok" || cfg.LoginCustomerID != "123" {
		t.Errorf("existing fields not preserved: %+v", cfg)
	}
	if cfg.ClientID != "cid" || cfg.ClientSecret != "csec" || cfg.RefreshToken != "rtok" {
		t.Errorf("oauth fields not written: %+v", cfg)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %v, want 0600", perm)
	}
}

func TestWriteOAuthToConfig_FreshFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sub/config.toml" // dir does not exist yet
	if err := writeOAuthToConfig(path, clientCreds{clientID: "cid", clientSecret: "csec"}, "rtok"); err != nil {
		t.Fatal(err)
	}
	var cfg GoogleConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "cid" || cfg.RefreshToken != "rtok" {
		t.Errorf("got %+v", cfg)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("fresh file perm = %v, want 0600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir perm = %v, want 0700", perm)
	}
}

// fireCallback returns an openFn that simulates the browser redirect by GETting
// the loopback callback with the given query, reusing the state from authURL.
func fireCallback(ln net.Listener, query func(state string) string) func(string) error {
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		state := u.Query().Get("state")
		go http.Get("http://" + ln.Addr().String() + "/?" + query(state)) //nolint:errcheck
		return nil
	}
}

func newLoopbackConf(t *testing.T) (*oauth2.Config, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &oauth2.Config{RedirectURL: "http://" + ln.Addr().String()}, ln
}

func TestRunLoopbackOAuth_CapturesCode(t *testing.T) {
	conf, ln := newLoopbackConf(t)
	openFn := fireCallback(ln, func(state string) string {
		return "code=testcode&state=" + state
	})
	code, err := runLoopbackOAuth(context.Background(), conf, openFn, ln)
	if err != nil {
		t.Fatal(err)
	}
	if code != "testcode" {
		t.Fatalf("got code %q", code)
	}
}

func TestRunLoopbackOAuth_StateMismatch(t *testing.T) {
	conf, ln := newLoopbackConf(t)
	openFn := fireCallback(ln, func(string) string { return "code=x&state=wrong" })
	_, err := runLoopbackOAuth(context.Background(), conf, openFn, ln)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("expected state mismatch error, got %v", err)
	}
}

func TestRunLoopbackOAuth_AuthError(t *testing.T) {
	conf, ln := newLoopbackConf(t)
	openFn := fireCallback(ln, func(string) string {
		return "error=access_denied&error_description=denied"
	})
	_, err := runLoopbackOAuth(context.Background(), conf, openFn, ln)
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestExchangeRefreshToken_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
	}))
	defer ts.Close()

	conf := &oauth2.Config{ClientID: "id", ClientSecret: "sec", Endpoint: oauth2.Endpoint{TokenURL: ts.URL}}
	rt, err := exchangeRefreshToken(context.Background(), conf, "code")
	if err != nil {
		t.Fatal(err)
	}
	if rt != "rt" {
		t.Fatalf("got refresh token %q", rt)
	}
}

func TestMergeConfigValues_WritesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.toml"
	if err := writeFileHelper(path, "base_url = \"https://example/v23\"\n"); err != nil {
		t.Fatal(err)
	}
	err := mergeConfigValues(path, map[string]string{
		"developer_token":   "devtok",
		"login_customer_id": "1234567890",
		"client_id":         "", // empty → must NOT be written
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg GoogleConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://example/v23" {
		t.Errorf("preserved key lost: %+v", cfg)
	}
	if cfg.DeveloperToken != "devtok" || cfg.LoginCustomerID != "1234567890" {
		t.Errorf("values not written: %+v", cfg)
	}
	if cfg.ClientID != "" {
		t.Errorf("empty value should not be written, got %q", cfg.ClientID)
	}
}

func TestExchangeRefreshToken_NoRefreshToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
	}))
	defer ts.Close()

	conf := &oauth2.Config{ClientID: "id", ClientSecret: "sec", Endpoint: oauth2.Endpoint{TokenURL: ts.URL}}
	_, err := exchangeRefreshToken(context.Background(), conf, "code")
	if err == nil || !strings.Contains(err.Error(), "Desktop app") {
		t.Fatalf("expected actionable no-refresh-token error, got %v", err)
	}
}

func TestRunLoopbackOAuth_IgnoresStrayRequests(t *testing.T) {
	conf, ln := newLoopbackConf(t)
	openFn := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		state := u.Query().Get("state")
		go func() {
			base := "http://" + ln.Addr().String()
			// Stray hits arrive before the real callback: a bare request (browser
			// preconnect / user opening the URL) and a favicon fetch. These used
			// to be treated as failed callbacks and aborted the login (issue #11).
			if resp, err := http.Get(base + "/"); err == nil {
				if resp.StatusCode != http.StatusNoContent {
					t.Errorf("bare request status = %d, want 204", resp.StatusCode)
				}
				resp.Body.Close()
			}
			if resp, err := http.Get(base + "/favicon.ico"); err == nil {
				resp.Body.Close()
			}
			if resp, err := http.Get(base + "/?code=realcode&state=" + state); err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	code, err := runLoopbackOAuth(context.Background(), conf, openFn, ln)
	if err != nil {
		t.Fatalf("stray requests must not abort the login: %v", err)
	}
	if code != "realcode" {
		t.Fatalf("got code %q, want realcode", code)
	}
}

func TestLoopbackRedirectURL_UsesLiteralLoopbackIP(t *testing.T) {
	// Google's loopback guidance: use http://127.0.0.1:<port>, not localhost,
	// which can resolve to ::1 while the listener binds IPv4 (issue #11).
	if got := loopbackRedirectURL(8085); got != "http://127.0.0.1:8085" {
		t.Fatalf("loopbackRedirectURL = %q", got)
	}
}
