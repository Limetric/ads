package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
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

func TestRunBingLoopbackOAuth_SendsPKCEChallenge(t *testing.T) {
	conf, ln := newLoopbackConf(t)
	var challenge, method string
	openFn := func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		challenge = u.Query().Get("code_challenge")
		method = u.Query().Get("code_challenge_method")
		state := u.Query().Get("state")
		go http.Get("http://" + ln.Addr().String() + "/?code=testcode&state=" + state) //nolint:errcheck
		return nil
	}

	code, verifier, err := runBingLoopbackOAuth(context.Background(), conf, openFn, ln)
	if err != nil {
		t.Fatalf("runBingLoopbackOAuth: %v", err)
	}
	if code != "testcode" {
		t.Errorf("code = %q", code)
	}
	if verifier == "" {
		t.Fatal("no verifier was generated — without one the exchange cannot prove it started the flow")
	}
	if method != "S256" {
		t.Errorf("code_challenge_method = %q, want S256 (plain defeats the point)", method)
	}
	// The challenge is the hash of the verifier; anything else and Entra will
	// reject the exchange.
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Errorf("code_challenge = %q, want S256(verifier) = %q", challenge, want)
	}
}

func TestExchangeBingTokens(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	conf := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}, Scopes: bingScopes}
	tok, err := exchangeBingTokens(context.Background(), conf, "code", "verifier-123")
	if err != nil {
		t.Fatalf("exchangeBingTokens: %v", err)
	}
	if tok.RefreshToken != "rt" {
		t.Errorf("refresh token = %q", tok.RefreshToken)
	}
	if form.Get("code_verifier") != "verifier-123" {
		t.Errorf("code_verifier = %q, want the verifier from the authorization request", form.Get("code_verifier"))
	}
	// A public client must not send a secret; Entra rejects the request if it
	// does ("Public clients can't send a client secret").
	if form.Get("client_secret") != "" {
		t.Errorf("client_secret = %q, want none for a public client", form.Get("client_secret"))
	}
}

func TestExchangeBingTokens_NoRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	conf := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}}
	_, err := exchangeBingTokens(context.Background(), conf, "code", "v")
	if err == nil || !strings.Contains(err.Error(), "offline_access") {
		t.Fatalf("the error should name the missing scope: %v", err)
	}
}

func TestMergeBingConfigValues_WritesTheTableAndPreservesTheRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// A file that already holds Google's top-level keys and a Bing table.
	if err := os.WriteFile(path, []byte("developer_token = \"google-token\"\n\n[bing]\nclient_id = \"old\"\ncustomer_id = \"777\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeBingConfigValues(path, map[string]string{
		"client_id":          "new",
		"default_account_id": "123456",
		"environment":        "", // empty values must not overwrite
	}); err != nil {
		t.Fatalf("mergeBingConfigValues: %v", err)
	}

	var doc map[string]any
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["developer_token"] != "google-token" {
		t.Errorf("Google's keys must survive a Bing write: %v", doc["developer_token"])
	}
	table, _ := doc[bingPlatformName].(map[string]any)
	if table["client_id"] != "new" || table["default_account_id"] != "123456" {
		t.Errorf("bing table = %v", table)
	}
	if table["customer_id"] != "777" {
		t.Errorf("untouched keys must be preserved: %v", table)
	}
	if _, ok := table["environment"]; ok {
		t.Error("an empty value should be skipped, not written")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("config permissions = %v (err %v), want 0600 — it holds credentials", info.Mode().Perm(), err)
	}
}

func TestSaveBingCredentials_SplitsConfigAndStore(t *testing.T) {
	useTempState(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := &BingConfig{ClientID: "cid", CustomerID: "777", Environment: bingEnvSandbox}

	if err := saveBingCredentials(path, cfg, "rt-1"); err != nil {
		t.Fatalf("saveBingCredentials: %v", err)
	}

	var doc map[string]any
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		t.Fatal(err)
	}
	table, _ := doc[bingPlatformName].(map[string]any)
	if table["client_id"] != "cid" || table["environment"] != bingEnvSandbox {
		t.Errorf("bing table = %v", table)
	}
	// The refresh token belongs to the store, never the config file — it is
	// replaced on every refresh and ads has to be able to rewrite it.
	for _, key := range []string{"refresh_token", "refreshtoken"} {
		if _, ok := table[key]; ok {
			t.Errorf("the refresh token must not be written to the config file (%s)", key)
		}
	}
	stored, err := readStoredToken(bingTokenPolicy.Platform)
	if err != nil || stored == nil {
		t.Fatalf("readStoredToken = (%v, %v)", stored, err)
	}
	if stored.RefreshToken != "rt-1" {
		t.Errorf("stored refresh token = %q", stored.RefreshToken)
	}
	// The client binding is what lets a mismatch be named instead of surfacing
	// as a bare invalid_grant later.
	if stored.ClientID != "cid" {
		t.Errorf("stored client ID = %q", stored.ClientID)
	}
}

func TestSaveBingCredentials_UnwritableStoreRollsBackTheConfig(t *testing.T) {
	useTempState(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[bing]\nclient_id = \"previous\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A store that cannot be written at all.
	t.Setenv(tokenStoreEnv, filepath.Join(dir, "missing-parent", "tokens"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make the directory read-only on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := saveBingCredentials(path, &BingConfig{ClientID: "new"}, "rt-1")
	if err == nil {
		t.Fatal("a sign-in that cannot be saved must fail loudly — Microsoft has already replaced the old token")
	}
	if !strings.Contains(err.Error(), tokenStoreEnv) {
		t.Errorf("the error should name the fix: %v", err)
	}
	// The client half must not be left pointing at a token that was never saved.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), "previous") {
		t.Errorf("config was not rolled back:\n%s", data)
	}
}

func TestBingLoginRedirectURL(t *testing.T) {
	// The value has to be registered on the Entra application verbatim, so it
	// is worth pinning: a change here is a change users must mirror.
	if got := bingLoginRedirectURL(8086); got != "http://localhost:8086" {
		t.Errorf("redirect URL = %q", got)
	}
}
