package main

import (
	"context"
	"fmt"
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

// fakePrompter returns scripted answers per method, in order.
type fakePrompter struct {
	lines      []string
	secrets    []string
	confirms   []bool
	li, si, ci int
}

func (f *fakePrompter) line(string) (string, error) {
	v := f.lines[f.li]
	f.li++
	return v, nil
}
func (f *fakePrompter) secret(string) (string, error) {
	v := f.secrets[f.si]
	f.si++
	return v, nil
}
func (f *fakePrompter) confirm(string, bool) (bool, error) {
	v := f.confirms[f.ci]
	f.ci++
	return v, nil
}

func TestWizardGatherClient_FromPath(t *testing.T) {
	dir := t.TempDir()
	jsonPath := dir + "/c.json"
	if err := writeFileHelper(jsonPath, `{"installed":{"client_id":"cid","client_secret":"csec"}}`); err != nil {
		t.Fatal(err)
	}
	// offerToOpen: confirm "Open this now?" → no, then a "Press Enter" line.
	// Then wizardGatherClient prompts for the path. So lines = [press-enter, path].
	p := &fakePrompter{confirms: []bool{false}, lines: []string{"", jsonPath}}
	creds, err := wizardGatherClient(p, io.Discard, &GoogleConfig{}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if creds.clientID != "cid" || creds.clientSecret != "csec" {
		t.Fatalf("got %+v", creds)
	}
	if creds.kind != "installed" {
		t.Errorf("kind = %q, want \"installed\"", creds.kind)
	}
}

func TestWizardGatherClient_RepromptsOnBadPath(t *testing.T) {
	dir := t.TempDir()
	good := dir + "/c.json"
	if err := writeFileHelper(good, `{"installed":{"client_id":"cid","client_secret":"csec"}}`); err != nil {
		t.Fatal(err)
	}
	// offerToOpen: open? no, then press-enter line. Then path prompts:
	// first missing → reprompt, second good. lines = [press-enter, missing, good].
	p := &fakePrompter{confirms: []bool{false}, lines: []string{"", dir + "/missing.json", good}}
	var out strings.Builder
	creds, err := wizardGatherClient(p, &out, &GoogleConfig{}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if creds.clientID != "cid" {
		t.Fatalf("got %+v", creds)
	}
	if !strings.Contains(out.String(), "try again") {
		t.Errorf("expected a retry message, got: %s", out.String())
	}
}

func TestWizardGatherClient_ReuseExisting(t *testing.T) {
	cfg := &GoogleConfig{ClientID: "existing", ClientSecret: "esec"}
	// confirm "Keep it?" → yes. No line reads.
	p := &fakePrompter{confirms: []bool{true}}
	creds, err := wizardGatherClient(p, io.Discard, cfg, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if creds.clientID != "existing" || creds.kind != "config" {
		t.Fatalf("got %+v", creds)
	}
}

func TestWizardGatherDeveloperToken_FreshAndEmptyReprompt(t *testing.T) {
	// open? no; first secret empty → reprompt; second secret valid.
	p := &fakePrompter{confirms: []bool{false}, lines: []string{""}, secrets: []string{"", "devtok"}}
	var out strings.Builder
	tok, err := wizardGatherDeveloperToken(p, &out, &GoogleConfig{}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if tok != "devtok" {
		t.Fatalf("got %q", tok)
	}
	if !strings.Contains(out.String(), "can't be empty") {
		t.Errorf("expected empty-token message, got %s", out.String())
	}
}

func TestWizardGatherDeveloperToken_Reuse(t *testing.T) {
	p := &fakePrompter{confirms: []bool{true}} // Keep it? → yes
	tok, err := wizardGatherDeveloperToken(p, io.Discard, &GoogleConfig{DeveloperToken: "old"}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if tok != "old" {
		t.Fatalf("got %q", tok)
	}
}

func TestWizardGatherLoginCustomerID(t *testing.T) {
	// Provided value, dashes stripped.
	p := &fakePrompter{lines: []string{"123-456-7890"}}
	id, err := wizardGatherLoginCustomerID(p, io.Discard, &GoogleConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if id != "1234567890" {
		t.Fatalf("got %q", id)
	}
	// Empty input keeps the existing default.
	p2 := &fakePrompter{lines: []string{""}}
	id2, err := wizardGatherLoginCustomerID(p2, io.Discard, &GoogleConfig{LoginCustomerID: "999"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != "999" {
		t.Fatalf("got %q", id2)
	}
}

func TestTTYPrompter_LineAndConfirm(t *testing.T) {
	// Two reads: a line, then a confirm answered with empty (→ default).
	in := strings.NewReader("  hello \n\n")
	var out strings.Builder
	p := newTTYPrompter(in, &out, -1) // fd<0 → no masking

	got, err := p.line("name: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("line = %q, want %q", got, "hello")
	}
	yes, err := p.confirm("ok?", true)
	if err != nil {
		t.Fatal(err)
	}
	if !yes {
		t.Error("empty answer should take the default (true)")
	}
	if !strings.Contains(out.String(), "name: ") || !strings.Contains(out.String(), "[Y/n]") {
		t.Errorf("prompts not written: %q", out.String())
	}
}

func TestTTYPrompter_ConfirmNo(t *testing.T) {
	p := newTTYPrompter(strings.NewReader("n\n"), io.Discard, -1)
	yes, err := p.confirm("ok?", true)
	if err != nil {
		t.Fatal(err)
	}
	if yes {
		t.Error("'n' should be false")
	}
}

func TestTTYPrompter_SecretFallback(t *testing.T) {
	// fd<0 → not a terminal → plain line read (no masking).
	p := newTTYPrompter(strings.NewReader("s3cret\n"), io.Discard, -1)
	got, err := p.secret("token: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Errorf("secret = %q", got)
	}
}

func TestSecretHint(t *testing.T) {
	if got := secretHint("abcdefghij"); got != "…efghij" {
		t.Errorf("got %q", got)
	}
	if got := secretHint("abc"); got != "…" {
		t.Errorf("short hint = %q", got)
	}
}

func TestDashCustomerID(t *testing.T) {
	if got := dashCustomerID("1234567890"); got != "123-456-7890" {
		t.Errorf("got %q", got)
	}
	if got := dashCustomerID("123"); got != "123" {
		t.Errorf("non-10-digit unchanged, got %q", got)
	}
}

func TestExpandHome(t *testing.T) {
	if got := expandHome(`  "/tmp/x.json"  `); got != "/tmp/x.json" {
		t.Errorf("quote/space strip: got %q", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandHome("~/Downloads/c.json"); got != filepath.Join(home, "Downloads/c.json") {
		t.Errorf("~/ expansion: got %q", got)
	}
}

// freePort returns a currently-free localhost port for the loopback OAuth server.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestRunLoginWizard_HappyPath(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)

	// One httptest server doubles as the OAuth token endpoint and the Ads API.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
		case r.URL.Path == "/customers:listAccessibleCustomers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"resourceNames":["customers/1234567890"]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	// Point OAuth token exchange and the Ads API at the test server.
	oldEndpoint := googleOAuthEndpoint
	googleOAuthEndpoint = oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token"}
	t.Cleanup(func() { googleOAuthEndpoint = oldEndpoint })
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL)

	cfg, err := loadLoginConfig("")
	if err != nil {
		t.Fatal(err)
	}

	// OAuth client JSON the wizard will read.
	jsonPath := t.TempDir() + "/c.json"
	if err := writeFileHelper(jsonPath, `{"installed":{"client_id":"cid","client_secret":"csec"}}`); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	// openFn fires the loopback callback with the state extracted from the auth URL.
	openFn := func(authURL string) error {
		u, perr := url.Parse(authURL)
		if perr != nil {
			return perr
		}
		st := u.Query().Get("state")
		go http.Get(fmt.Sprintf("http://127.0.0.1:%d/?code=testcode&state=%s", port, st))
		return nil
	}

	// Scripted answers, in call order:
	//   confirms: step1 open? n, step2 open? n, step3 open? y (fires callback), step4 open? n
	//   lines:    step1 enter, step2 enter, JSON path, step4 enter, login id (skip)
	//   secrets:  developer token
	p := &fakePrompter{
		confirms: []bool{false, false, true, false},
		lines:    []string{"", "", jsonPath, "", ""},
		secrets:  []string{"devtok"},
	}

	var out strings.Builder
	if err := runLoginWizard(context.Background(), &out, p, cfg, openFn, port); err != nil {
		t.Fatalf("wizard failed: %v\n%s", err, out.String())
	}

	if !strings.Contains(out.String(), "Connected") || !strings.Contains(out.String(), "123-456-7890") {
		t.Errorf("missing verify line:\n%s", out.String())
	}

	target, err := configWriteTarget("")
	if err != nil {
		t.Fatal(err)
	}
	var written GoogleConfig
	if _, err := toml.DecodeFile(target, &written); err != nil {
		t.Fatal(err)
	}
	if written.ClientID != "cid" || written.DeveloperToken != "devtok" {
		t.Errorf("config not fully written: %+v", written)
	}
	if written.RefreshToken != "" {
		t.Errorf("refresh_token must not be written to the config file: %+v", written)
	}
	stored, err := readStoredToken("google")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RefreshToken != "rt" {
		t.Errorf("refresh token not saved to the store: %+v", stored)
	}
}

func TestIsInteractiveLogin_NoInputFlag(t *testing.T) {
	// --no-input forces non-interactive regardless of TTY.
	loginNoInput = true
	t.Cleanup(func() { loginNoInput = false })
	if isInteractiveLogin() {
		t.Error("--no-input must force non-interactive")
	}
}

func TestIsInteractiveLogin_CredentialsFlag(t *testing.T) {
	loginCredentialsPath = "/some/file.json"
	t.Cleanup(func() { loginCredentialsPath = "" })
	if isInteractiveLogin() {
		t.Error("--credentials must force non-interactive")
	}
}

func TestConfirmBrowserOpen_ShowsURLAndOpensOnYes(t *testing.T) {
	var opened string
	p := &fakePrompter{confirms: []bool{true}} // Open this now? → yes
	var out strings.Builder
	wrapped := confirmBrowserOpen(p, &out, 8080, func(u string) error { opened = u; return nil })
	if err := wrapped("https://accounts.google.com/auth?x=1"); err != nil {
		t.Fatal(err)
	}
	if opened != "https://accounts.google.com/auth?x=1" {
		t.Errorf("openFn not called with URL, got %q", opened)
	}
	if !strings.Contains(out.String(), "https://accounts.google.com/auth?x=1") {
		t.Errorf("consent URL must be shown before opening, got: %q", out.String())
	}
	if p.ci != 1 {
		t.Errorf("expected exactly one confirm consumed, ci=%d", p.ci)
	}
}

func TestConfirmBrowserOpen_DeclineDoesNotOpenButShowsURL(t *testing.T) {
	opened := false
	p := &fakePrompter{confirms: []bool{false}} // Open this now? → no
	var out strings.Builder
	wrapped := confirmBrowserOpen(p, &out, 8080, func(string) error { opened = true; return nil })
	if err := wrapped("https://example/auth"); err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Error("declining must not open a browser")
	}
	if !strings.Contains(out.String(), "https://example/auth") {
		t.Errorf("URL must still be shown so the user can open it manually, got: %q", out.String())
	}
}

func TestConfirmBrowserOpen_NoBrowserSkipsConfirm(t *testing.T) {
	loginNoBrowser = true
	t.Cleanup(func() { loginNoBrowser = false })
	opened := false
	p := &fakePrompter{}
	var out strings.Builder
	wrapped := confirmBrowserOpen(p, &out, 8080, func(string) error { opened = true; return nil })
	if err := wrapped("https://example/auth"); err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Error("--no-browser must not open a browser")
	}
	if p.ci != 0 {
		t.Errorf("--no-browser must not consume a confirm, ci=%d", p.ci)
	}
	if !strings.Contains(out.String(), "https://example/auth") {
		t.Errorf("URL must be shown so the user can open it manually, got: %q", out.String())
	}
}

func TestOfferToOpen_NoBrowserSkipsConfirm(t *testing.T) {
	loginNoBrowser = true
	t.Cleanup(func() { loginNoBrowser = false })
	opened := false
	p := &fakePrompter{lines: []string{""}} // only the "Press Enter" line
	err := offerToOpen(p, io.Discard, "do the thing", "https://example", func(string) error {
		opened = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Error("--no-browser must not open a browser")
	}
	if p.ci != 0 {
		t.Errorf("--no-browser must not consume a confirm, ci=%d", p.ci)
	}
}

func TestWizardGatherRefreshToken_AuthorizedUserShortcut(t *testing.T) {
	creds := clientCreds{kind: "authorized_user", clientID: "id", clientSecret: "sec", refreshToken: "rt-existing"}
	p := &fakePrompter{} // any prompt would panic — the shortcut must not prompt
	rt, err := wizardGatherRefreshToken(t.Context(), p, io.Discard, creds, &GoogleConfig{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rt != "rt-existing" {
		t.Errorf("rt = %q, want rt-existing", rt)
	}
}

func TestWizardGatherRefreshToken_ReuseExistingSignIn(t *testing.T) {
	// Both an empty answer (the default) and anything starting with "r" reuse.
	for _, answer := range []string{"", "reuse", "R"} {
		p := &fakePrompter{lines: []string{answer}}
		var out strings.Builder
		rt, err := wizardGatherRefreshToken(t.Context(), p, &out, clientCreds{kind: "config"}, &GoogleConfig{RefreshToken: "rt-cfg"}, nil, 0)
		if err != nil {
			t.Fatalf("answer %q: %v", answer, err)
		}
		if rt != "rt-cfg" {
			t.Errorf("answer %q: rt = %q, want rt-cfg", answer, rt)
		}
		if !strings.Contains(out.String(), "reusing existing sign-in") {
			t.Errorf("answer %q: expected reuse notice, got %q", answer, out.String())
		}
	}
}

func TestWizardGatherRefreshToken_DoesNotOfferAnUnusableSignIn(t *testing.T) {
	useTokenStore(t)
	// The saved sign-in belongs to a different OAuth client, so reusing it
	// would stage a token that cannot authenticate — and would overwrite the
	// binding that says so.
	err := writeStoredToken("google", &storedToken{RefreshToken: "rt-client-a", ClientID: "client-a"})
	if err != nil {
		t.Fatal(err)
	}

	// A "reuse" answer is queued but must go unused; the confirm belongs to the
	// browser prompt of the fresh sign-in this should fall through to.
	p := &fakePrompter{lines: []string{"reuse"}, confirms: []bool{false}}
	creds := clientCreds{kind: "config", clientID: "client-b", clientSecret: "secret"}
	cfg := &GoogleConfig{RefreshToken: "rt-client-a"}
	// The flow should fall through to a real sign-in; cancel so the loopback
	// server returns instead of waiting out the callback timeout.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := wizardGatherRefreshToken(ctx, p, io.Discard, creds, cfg, func(string) error { return nil }, 0)
	if err == nil {
		t.Fatalf("expected the cancelled sign-in flow to fail, got %q", got)
	}
	if p.li != 0 {
		t.Error("the wizard offered to reuse a sign-in belonging to another OAuth client")
	}
	if got == "rt-client-a" {
		t.Error("the wizard returned a sign-in that cannot authenticate with the configured client")
	}
}

func TestWizardGatherRefreshToken_PortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	p := &fakePrompter{lines: []string{"new"}} // decline reuse → fresh sign-in
	creds := clientCreds{kind: "config", clientID: "id", clientSecret: "sec"}
	_, err = wizardGatherRefreshToken(t.Context(), p, io.Discard, creds, &GoogleConfig{RefreshToken: "rt-cfg"}, nil, port)
	if err == nil {
		t.Fatal("expected an error when the loopback port is busy")
	}
	if !strings.Contains(err.Error(), "--port") {
		t.Errorf("error should point at --port as the fix, got: %v", err)
	}
}

// wizardTestEnv points OAuth and the Ads API at a fake server that hands out
// refresh token "rt" and one accessible customer, and returns an openFn that
// completes the loopback flow on the given port.
func wizardTestEnv(t *testing.T, port int) func(string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":3600}`)
		case r.URL.Path == "/customers:listAccessibleCustomers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"resourceNames":["customers/1234567890"]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldEndpoint := googleOAuthEndpoint
	googleOAuthEndpoint = oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token"}
	t.Cleanup(func() { googleOAuthEndpoint = oldEndpoint })
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL)
	return func(authURL string) error {
		u, perr := url.Parse(authURL)
		if perr != nil {
			return perr
		}
		st := u.Query().Get("state")
		go http.Get(fmt.Sprintf("http://127.0.0.1:%d/?code=testcode&state=%s", port, st))
		return nil
	}
}

// A config that already has an OAuth client and developer token — the state
// after a token store went missing — must not replay first-time setup: one
// confirm, the browser sign-in, done. Nothing else is asked or changed.
func TestRunLoginWizard_SignInOnlyWhenSetupComplete(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	port := freePort(t)
	openFn := wizardTestEnv(t, port)

	target, err := configWriteTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileHelper(target, "client_id = \"cid\"\nclient_secret = \"csec\"\ndeveloper_token = \"devtok\"\nlogin_customer_id = \"1112223333\"\n"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadLoginConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if !wizardSetupComplete(cfg) {
		t.Fatalf("test config should count as complete: %+v", cfg)
	}

	// confirms: keep setup? y, open browser? y (fires callback). No lines, no secrets.
	p := &fakePrompter{confirms: []bool{true, true}}
	var out strings.Builder
	if err := runLoginWizard(context.Background(), &out, p, cfg, openFn, port); err != nil {
		t.Fatalf("wizard failed: %v\n%s", err, out.String())
	}
	got := out.String()
	if strings.Contains(got, "Step 1/5") || strings.Contains(got, "Welcome to ads") {
		t.Errorf("sign-in-only path replayed the full wizard:\n%s", got)
	}
	if !strings.Contains(got, "Found an existing Google Ads setup") || !strings.Contains(got, "111-222-3333") {
		t.Errorf("missing setup summary:\n%s", got)
	}
	if !strings.Contains(got, "Connected") {
		t.Errorf("missing verify line:\n%s", got)
	}
	if p.li != 0 || p.si != 0 {
		t.Errorf("sign-in-only path must not prompt for values: lines=%d secrets=%d", p.li, p.si)
	}

	var written GoogleConfig
	if _, err := toml.DecodeFile(target, &written); err != nil {
		t.Fatal(err)
	}
	if written.ClientID != "cid" || written.DeveloperToken != "devtok" || written.LoginCustomerID != "1112223333" {
		t.Errorf("existing config values changed: %+v", written)
	}
	if written.RefreshToken != "" {
		t.Errorf("refresh_token must not be written to the config file: %+v", written)
	}
	stored, err := readStoredToken("google")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RefreshToken != "rt" || stored.ClientID != "cid" {
		t.Errorf("refresh token not saved to the store: %+v", stored)
	}
}

// Declining the shortcut drops into the full wizard, where every prompt
// defaults to the existing value.
func TestRunLoginWizard_DeclineSignInOnlyRunsFullWizard(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	port := freePort(t)
	openFn := wizardTestEnv(t, port)

	target, err := configWriteTarget("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileHelper(target, "client_id = \"cid\"\nclient_secret = \"csec\"\ndeveloper_token = \"devtok\"\n"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadLoginConfig("")
	if err != nil {
		t.Fatal(err)
	}

	// confirms: keep setup? n, step1 open? n, keep client? y, open browser? y,
	//           keep dev token? y
	// lines:    step1 enter, login id (skip)
	p := &fakePrompter{
		confirms: []bool{false, false, true, true, true},
		lines:    []string{"", ""},
	}
	var out strings.Builder
	if err := runLoginWizard(context.Background(), &out, p, cfg, openFn, port); err != nil {
		t.Fatalf("wizard failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Step 1/5") || !strings.Contains(out.String(), "Connected") {
		t.Errorf("expected full wizard run:\n%s", out.String())
	}
	if p.ci != 5 || p.li != 2 {
		t.Errorf("unexpected prompt counts: confirms=%d lines=%d", p.ci, p.li)
	}
}

func TestWizardSetupComplete(t *testing.T) {
	cases := []struct {
		name string
		cfg  GoogleConfig
		want bool
	}{
		{"empty", GoogleConfig{}, false},
		{"client only", GoogleConfig{ClientID: "a", ClientSecret: "b"}, false},
		{"dev token only", GoogleConfig{DeveloperToken: "d"}, false},
		{"complete", GoogleConfig{ClientID: "a", ClientSecret: "b", DeveloperToken: "d"}, true},
	}
	for _, c := range cases {
		if got := wizardSetupComplete(&c.cfg); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
