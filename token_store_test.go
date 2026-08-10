package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// staticPolicy / rotatingPolicy stand in for the two kinds of platform the
// store has to serve. Google is the only real platform today, so the rotating
// half — the whole reason the store exists — is exercised through a synthetic
// one, exactly as Microsoft and Pinterest will be wired up.
var (
	staticPolicy   = tokenPolicy{Platform: "static", Rotates: false}
	rotatingPolicy = tokenPolicy{Platform: "rotating", Rotates: true}
)

// captureWarnings redirects deprecation notices into a buffer and resets the
// once-per-process dedup, so each test sees only its own warnings.
func captureWarnings(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	warnedMu.Lock()
	prevWriter, prevSeen := warnWriter, warned
	warnWriter, warned = &b, map[string]bool{}
	warnedMu.Unlock()
	t.Cleanup(func() {
		warnedMu.Lock()
		warnWriter, warned = prevWriter, prevSeen
		warnedMu.Unlock()
	})
	return &b
}

// useTokenStore points the store at a fresh temp directory and returns it.
func useTokenStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tokens")
	t.Setenv(tokenStoreEnv, dir)
	return dir
}

// readOnlyDir creates a directory and strips write permission from it. Root
// ignores the mode bits, so tests that need a genuinely unwritable directory
// skip when running as root.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "tokens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // so TempDir cleanup works
	return dir
}

// --- store files ---

func TestTokenStore_RoundTrip(t *testing.T) {
	dir := useTokenStore(t)

	if got, err := readStoredToken("google"); err != nil || got != nil {
		t.Fatalf("empty store: got %+v, %v; want nil, nil", got, err)
	}

	want := &storedToken{RefreshToken: "rt-1", UpdatedAt: time.Now().UTC().Truncate(time.Second), Source: "ads login google"}
	if err := writeStoredToken("google", want); err != nil {
		t.Fatal(err)
	}
	got, err := readStoredToken("google")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != want.RefreshToken || got.Source != want.Source || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}

	// Credentials on disk: 0600 in a 0700 directory, and one file per platform.
	fi, err := os.Stat(filepath.Join(dir, "google.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file perm = %v, want 0600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("store dir perm = %v, want 0700", perm)
	}
}

func TestTokenStore_PlatformsAreIsolated(t *testing.T) {
	useTokenStore(t)
	if err := writeStoredToken("google", &storedToken{RefreshToken: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := writeStoredToken("bing", &storedToken{RefreshToken: "b"}); err != nil {
		t.Fatal(err)
	}
	g, err := readStoredToken("google")
	if err != nil {
		t.Fatal(err)
	}
	b, err := readStoredToken("bing")
	if err != nil {
		t.Fatal(err)
	}
	if g.RefreshToken != "g" || b.RefreshToken != "b" {
		t.Errorf("platforms share a slot: google=%q bing=%q", g.RefreshToken, b.RefreshToken)
	}
}

func TestTokenStore_DefaultsUnderStateDir(t *testing.T) {
	useTempState(t)
	t.Setenv(tokenStoreEnv, "")

	path, err := tokenStorePath("google")
	if err != nil {
		t.Fatal(err)
	}
	state, err := stateDirPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(state, "tokens", "google.json"); path != want {
		t.Errorf("default store path = %q, want %q", path, want)
	}
}

func TestTokenStore_CorruptFileNamesThePathAndTheFix(t *testing.T) {
	dir := useTokenStore(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "google.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readStoredToken("google")
	if err == nil {
		t.Fatal("expected an error for a corrupt store file")
	}
	for _, want := range []string{path, "ads login google"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
}

func TestTokenStore_RejectsUnsafePlatformNames(t *testing.T) {
	useTokenStore(t)
	for _, name := range []string{"", "../escape", "google/../..", "Google", "go_ogle", "goo gle"} {
		if _, err := tokenStorePath(name); err == nil {
			t.Errorf("platform name %q should be rejected", name)
		}
	}
}

// --- resolution: store first, deprecated seeds once ---

func TestResolveRefreshToken_MigratesEnvSeedThenIgnoresIt(t *testing.T) {
	useTokenStore(t)
	warnings := captureWarnings(t)

	env := tokenSeed{Value: "rt-from-env", Origin: "GOOGLE_ADS_REFRESH_TOKEN"}
	got, err := resolveRefreshToken(staticPolicy, "", []tokenSeed{env})
	if err != nil {
		t.Fatal(err)
	}
	if got != "rt-from-env" {
		t.Errorf("first use should accept the seed, got %q", got)
	}
	stored, err := readStoredToken(staticPolicy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RefreshToken != "rt-from-env" {
		t.Fatalf("seed not migrated into the store: %+v", stored)
	}
	if stored.Source != env.Origin {
		t.Errorf("stored source = %q, want the seed origin %q", stored.Source, env.Origin)
	}
	if stored.UpdatedAt.IsZero() {
		t.Error("stored token has no timestamp, so doctor cannot report its age")
	}
	if !strings.Contains(warnings.String(), "deprecated") || !strings.Contains(warnings.String(), env.Origin) {
		t.Errorf("migration should warn about the deprecated source: %q", warnings.String())
	}

	// Second invocation, with the environment now holding a *different* value:
	// the store is authoritative and the variable must not be re-read.
	stale := tokenSeed{Value: "rt-stale-in-env", Origin: env.Origin}
	got, err = resolveRefreshToken(staticPolicy, "", []tokenSeed{stale})
	if err != nil {
		t.Fatal(err)
	}
	if got != "rt-from-env" {
		t.Errorf("stored token should win over the env seed, got %q", got)
	}
	if !strings.Contains(warnings.String(), "holds a different token") {
		t.Errorf("a still-set deprecated source should be reported as unused: %q", warnings.String())
	}
}

func TestResolveRefreshToken_RetiresSeedsItCanRewrite(t *testing.T) {
	useTokenStore(t)
	captureWarnings(t)

	retired := false
	seeds := []tokenSeed{{
		Value:  "rt-from-toml",
		Origin: "refresh_token in /tmp/config.toml",
		Retire: func() error { retired = true; return nil },
	}}
	if _, err := resolveRefreshToken(staticPolicy, "", seeds); err != nil {
		t.Fatal(err)
	}
	if !retired {
		t.Error("a rewritable seed should be cleared once it is safely in the store")
	}
}

func TestResolveRefreshToken_RetireFailureIsNotFatal(t *testing.T) {
	useTokenStore(t)
	warnings := captureWarnings(t)

	seeds := []tokenSeed{{
		Value:  "rt",
		Origin: "refresh_token in /tmp/config.toml",
		Retire: func() error { return fmt.Errorf("read-only file") },
	}}
	got, err := resolveRefreshToken(staticPolicy, "", seeds)
	if err != nil {
		t.Fatalf("a failed cleanup must not fail the sign-in: %v", err)
	}
	if got != "rt" {
		t.Errorf("got %q, want the migrated token", got)
	}
	if !strings.Contains(warnings.String(), "could not remove") {
		t.Errorf("a failed cleanup should be reported: %q", warnings.String())
	}
}

func TestResolveRefreshToken_NeverDeletesASeedItDidNotSave(t *testing.T) {
	useTokenStore(t)
	warnings := captureWarnings(t)

	// The environment outranks the file, but outranking is not the same as
	// being valid: the file may hold the only working credential.
	retired := false
	seeds := []tokenSeed{
		{Value: "stale-from-env", Origin: "GOOGLE_ADS_REFRESH_TOKEN"},
		{Value: "good-from-file", Origin: "refresh_token in config.toml", Retire: func() error { retired = true; return nil }},
	}
	got, err := resolveRefreshToken(staticPolicy, "", seeds)
	if err != nil {
		t.Fatal(err)
	}
	if got != "stale-from-env" {
		t.Errorf("got %q, want the higher-precedence seed", got)
	}
	if retired {
		t.Error("a seed holding a different token was deleted — that credential may be the only working one")
	}
	if !strings.Contains(warnings.String(), "holds a different token") {
		t.Errorf("the unused second credential should be reported: %q", warnings.String())
	}
}

func TestResolveRefreshToken_RetriesRetirementOnLaterRuns(t *testing.T) {
	useTokenStore(t)
	captureWarnings(t)

	// First run: the token migrates but the config file cannot be rewritten.
	attempts, fail := 0, true
	newSeeds := func() []tokenSeed {
		return []tokenSeed{{
			Value:  "rt",
			Origin: "refresh_token in config.toml",
			Retire: func() error {
				attempts++
				if fail {
					return fmt.Errorf("read-only file")
				}
				return nil
			},
		}}
	}
	if _, err := resolveRefreshToken(staticPolicy, "", newSeeds()); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("retire attempts = %d, want 1", attempts)
	}

	// Later run, file now writable: the leftover copy must still get cleared,
	// rather than lingering because migration already happened.
	fail = false
	if _, err := resolveRefreshToken(staticPolicy, "", newSeeds()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Errorf("retire attempts = %d, want the retirement retried once the file became writable", attempts)
	}
}

func TestResolveRefreshToken_EnvSeedWinsOverFileSeed(t *testing.T) {
	useTokenStore(t)
	captureWarnings(t)

	got, err := resolveRefreshToken(staticPolicy, "", []tokenSeed{
		{Value: "from-env", Origin: "GOOGLE_ADS_REFRESH_TOKEN"},
		{Value: "from-file", Origin: "refresh_token in config.toml", Retire: func() error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Errorf("got %q, want the higher-precedence seed", got)
	}
	stored, err := readStoredToken(staticPolicy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "from-env" {
		t.Errorf("stored %q, want the higher-precedence seed", stored.RefreshToken)
	}
}

func TestResolveRefreshToken_NoStoreAndNoSeeds(t *testing.T) {
	useTokenStore(t)
	captureWarnings(t)

	got, err := resolveRefreshToken(staticPolicy, "", nil)
	if err != nil {
		t.Fatalf("an absent sign-in is the caller's to report, not an error here: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveRefreshToken_UnwritableStore(t *testing.T) {
	t.Run("static platform keeps working and names the path", func(t *testing.T) {
		dir := readOnlyDir(t)
		t.Setenv(tokenStoreEnv, dir)
		warnings := captureWarnings(t)

		got, err := resolveRefreshToken(staticPolicy, "", []tokenSeed{{Value: "rt", Origin: "GOOGLE_ADS_REFRESH_TOKEN"}})
		if err != nil {
			t.Fatalf("a static token survives not being saved; setups that work today must keep working: %v", err)
		}
		if got != "rt" {
			t.Errorf("got %q, want the seed", got)
		}
		if !strings.Contains(warnings.String(), dir) {
			t.Errorf("the warning must name the store path: %q", warnings.String())
		}
	})

	t.Run("rotating platform fails before burning the token", func(t *testing.T) {
		dir := readOnlyDir(t)
		t.Setenv(tokenStoreEnv, dir)
		captureWarnings(t)

		_, err := resolveRefreshToken(rotatingPolicy, "", []tokenSeed{{Value: "rt", Origin: "BING_ADS_REFRESH_TOKEN"}})
		if err == nil {
			t.Fatal("an unwritable store must fail a rotating platform up front")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("the error must name the store path, not read as an auth failure: %v", err)
		}
		if !strings.Contains(err.Error(), tokenStoreEnv) {
			t.Errorf("the error must name the override that fixes it: %v", err)
		}
	})
}

func TestResolveRefreshToken_SavedTokenOnAReadOnlyStore(t *testing.T) {
	// Set up a store holding a saved sign-in, then make it read-only.
	seal := func(t *testing.T, policy tokenPolicy) string {
		t.Helper()
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir := filepath.Join(t.TempDir(), "tokens")
		t.Setenv(tokenStoreEnv, dir)
		err := writeStoredToken(policy.Platform, &storedToken{RefreshToken: "rt-saved", UpdatedAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		captureWarnings(t)
		return dir
	}

	t.Run("static platform is undisturbed", func(t *testing.T) {
		seal(t, staticPolicy)
		// Its refresh token never changes, so reading it needs no write and a
		// read-only mount costs nothing.
		got, err := resolveRefreshToken(staticPolicy, "", nil)
		if err != nil {
			t.Fatalf("a static sign-in must survive a read-only store: %v", err)
		}
		if got != "rt-saved" {
			t.Errorf("got %q, want the saved token", got)
		}
	})

	t.Run("rotating platform refuses before the exchange", func(t *testing.T) {
		dir := seal(t, rotatingPolicy)
		// Using this token is what invalidates it. Handing it out and only
		// failing on the write-back afterwards would destroy the sign-in.
		_, err := resolveRefreshToken(rotatingPolicy, "", nil)
		if err == nil {
			t.Fatal("a rotating platform must not spend a token it cannot replace")
		}
		if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), tokenStoreEnv) {
			t.Errorf("error should name the path and the override: %v", err)
		}
	})
}

// sealedSymlinkStore points a platform's store file at a symlink whose target
// sits in an unwritable directory. The store directory itself stays writable,
// so only a check that follows the link can tell that a write will fail.
func sealedSymlinkStore(t *testing.T, platform, refreshToken string) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	base := t.TempDir()
	storeDir := filepath.Join(base, "tokens")
	sealed := filepath.Join(base, "sealed")
	for _, d := range []string{storeDir, sealed} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(sealed, platform+".json")
	body := fmt.Sprintf(`{"refresh_token":%q}`, refreshToken)
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(storeDir, platform+".json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Chmod(sealed, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o700) })
	t.Setenv(tokenStoreEnv, storeDir)
	return sealed
}

func TestResolveRefreshToken_SymlinkedStoreIntoAnUnwritableDirectory(t *testing.T) {
	t.Run("rotating platform refuses before the exchange", func(t *testing.T) {
		sealedSymlinkStore(t, rotatingPolicy.Platform, "rt-saved")
		captureWarnings(t)
		// The store directory is writable; only the link target is not. Missing
		// that would spend the refresh token and then fail to save its
		// replacement, which is exactly the case that cannot be recovered.
		if _, err := resolveRefreshToken(rotatingPolicy, "", nil); err == nil {
			t.Fatal("a store that cannot actually be written must fail up front")
		}
	})

	t.Run("static platform is undisturbed", func(t *testing.T) {
		sealedSymlinkStore(t, staticPolicy.Platform, "rt-saved")
		captureWarnings(t)
		got, err := resolveRefreshToken(staticPolicy, "", nil)
		if err != nil {
			t.Fatalf("a static sign-in never needs to be rewritten: %v", err)
		}
		if got != "rt-saved" {
			t.Errorf("got %q, want the saved token", got)
		}
	})
}

func TestResolveRefreshToken_NoStoreLocationAtAll(t *testing.T) {
	// No HOME and no override: there is nowhere to persist anything.
	unsetConfigDirEnv(t)
	t.Setenv(tokenStoreEnv, "")

	t.Run("static platform degrades with a warning", func(t *testing.T) {
		warnings := captureWarnings(t)
		got, err := resolveRefreshToken(staticPolicy, "", []tokenSeed{{Value: "rt", Origin: "GOOGLE_ADS_REFRESH_TOKEN"}})
		if err != nil {
			t.Fatalf("env-only setups without a config dir must keep working: %v", err)
		}
		if got != "rt" {
			t.Errorf("got %q, want the seed", got)
		}
		if !strings.Contains(warnings.String(), tokenStoreEnv) {
			t.Errorf("the warning should name the override: %q", warnings.String())
		}
	})

	t.Run("rotating platform fails", func(t *testing.T) {
		captureWarnings(t)
		if _, err := resolveRefreshToken(rotatingPolicy, "", []tokenSeed{{Value: "rt", Origin: "BING_ADS_REFRESH_TOKEN"}}); err == nil {
			t.Fatal("a rotating platform cannot run without persistence")
		}
	})
}

// --- rotation write-back ---

// rotatingTokenServer fakes a token endpoint that invalidates each refresh
// token as it is used, the way Microsoft and Pinterest do. It rejects any
// refresh token that is not the current one, so a client that fails to save a
// rotation is caught on its next call rather than silently passing.
type rotatingTokenServer struct {
	mu       sync.Mutex
	current  string
	issued   int
	rejected int
}

func newRotatingTokenServer(t *testing.T, initial string) (*rotatingTokenServer, *httptest.Server) {
	t.Helper()
	s := &rotatingTokenServer{current: initial}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		s.mu.Lock()
		defer s.mu.Unlock()
		if form.Get("refresh_token") != s.current {
			s.rejected++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"token already used"}`)
			return
		}
		s.issued++
		s.current = fmt.Sprintf("rt-%d", s.issued)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"at-%d","refresh_token":%q,"token_type":"Bearer","expires_in":3600}`, s.issued, s.current)
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func TestPersistingTokenSource_RotatingPlatformSurvivesSeparateInvocations(t *testing.T) {
	useTokenStore(t)
	captureWarnings(t)
	server, srv := newRotatingTokenServer(t, "rt-seed")

	// First "process": the token arrives from a deprecated env var.
	seeds := []tokenSeed{{Value: "rt-seed", Origin: "BING_ADS_REFRESH_TOKEN"}}

	for i := range 5 {
		// Each iteration is a fresh process: nothing is carried over in memory,
		// the refresh token comes back out of the store every time.
		refresh, err := resolveRefreshToken(rotatingPolicy, "", seeds)
		if err != nil {
			t.Fatalf("run %d: resolve: %v", i, err)
		}
		ts := newTokenSource(t.Context(), oauthClient{
			tokenPolicy:  rotatingPolicy,
			Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
			ClientID:     "id",
			ClientSecret: "secret",
			RefreshToken: refresh,
		})
		tok, err := ts.Token()
		if err != nil {
			t.Fatalf("run %d: token: %v", i, err)
		}
		if tok.AccessToken == "" {
			t.Fatalf("run %d: no access token", i)
		}
		seeds = nil // only the first run had an env var to migrate
	}

	if server.rejected != 0 {
		t.Errorf("%d refresh attempts used an already-invalidated token — rotations were not saved", server.rejected)
	}
	if server.issued != 5 {
		t.Fatalf("server issued %d tokens, want one per run — the test did not exercise rotation", server.issued)
	}
	stored, err := readStoredToken(rotatingPolicy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RefreshToken != server.current {
		t.Errorf("store holds %+v, want the latest rotation %q", stored, server.current)
	}
	if stored.Source != "token refresh" {
		t.Errorf("stored source = %q, want it to record the refresh", stored.Source)
	}
}

func TestPersistingTokenSource_StaticTokenNeverWrites(t *testing.T) {
	dir := useTokenStore(t)
	// A Google-shaped token endpoint: the response carries no refresh_token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	ts := newTokenSource(t.Context(), oauthClient{
		tokenPolicy:  googleTokenPolicy,
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
		ClientID:     "id",
		ClientSecret: "secret",
		RefreshToken: "rt-static",
	})
	if _, err := ts.Token(); err != nil {
		t.Fatal(err)
	}
	// Nothing changed, so nothing is written — which is what lets a signed-in
	// Google setup run against a read-only store.
	if _, err := os.Stat(filepath.Join(dir, "google.json")); !os.IsNotExist(err) {
		t.Errorf("an unchanged refresh token should not touch the store (stat err = %v)", err)
	}
}

func TestPersistingTokenSource_UnsavableRotationFails(t *testing.T) {
	dir := readOnlyDir(t)
	t.Setenv(tokenStoreEnv, dir)
	_, srv := newRotatingTokenServer(t, "rt-seed")

	ts := newTokenSource(t.Context(), oauthClient{
		tokenPolicy:  rotatingPolicy,
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
		ClientID:     "id",
		ClientSecret: "secret",
		RefreshToken: "rt-seed",
	})
	_, err := ts.Token()
	if err == nil {
		t.Fatal("a rotation that cannot be saved must fail loudly, not be dropped")
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), tokenStoreEnv) {
		t.Errorf("error should name the path and the override: %v", err)
	}
}

func TestPersistingTokenSource_InvalidGrantPointsAtLogin(t *testing.T) {
	useTokenStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
	}))
	defer srv.Close()

	ts := newTokenSource(t.Context(), oauthClient{
		tokenPolicy:  googleTokenPolicy,
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
		ClientID:     "id",
		ClientSecret: "secret",
		RefreshToken: "revoked",
	})
	_, err := ts.Token()
	if err == nil {
		t.Fatal("expected the revoked grant to fail")
	}
	if !strings.Contains(err.Error(), "ads login google") {
		t.Errorf("invalid_grant must tell the user how to fix it: %v", err)
	}
	// Wrapping must not hide the cause from doctor's classification, or a
	// revoked token gets reported as a transient network problem (issue #11).
	if got := liveVerdictFor(err); got != liveFailed {
		t.Errorf("liveVerdictFor = %v, want liveFailed", got)
	}
}

func TestTokenPolicy_AuthErrorPassesOtherFailuresThrough(t *testing.T) {
	cause := &oauth2.RetrieveError{ErrorCode: "temporarily_unavailable"}
	if got := googleTokenPolicy.authError(cause); got != error(cause) {
		t.Errorf("non-invalid_grant errors should pass through unchanged, got %v", got)
	}
}

// --- diagnostics ---

func TestDescribeTokenStore(t *testing.T) {
	t.Run("reports a writable store and the token age", func(t *testing.T) {
		dir := useTokenStore(t)
		err := writeStoredToken("google", &storedToken{
			RefreshToken: "rt",
			UpdatedAt:    time.Now().UTC().Add(-50 * time.Hour),
			Source:       "ads login google",
		})
		if err != nil {
			t.Fatal(err)
		}
		st := describeTokenStore("google")
		if st.location() != filepath.Join(dir, "google.json") {
			t.Errorf("location = %q, want the store file", st.location())
		}
		if desc := st.describe(googleTokenPolicy); !strings.Contains(desc, "2 days ago") || !strings.Contains(desc, "ads login google") {
			t.Errorf("describe = %q, want the age and the source", desc)
		}
	})

	t.Run("reports an unwritable store", func(t *testing.T) {
		dir := readOnlyDir(t)
		t.Setenv(tokenStoreEnv, dir)
		st := describeTokenStore("google")
		if st.WriteErr == nil {
			t.Fatal("expected the read-only store to be reported as unwritable")
		}
		if loc := st.location(); !strings.Contains(loc, "NOT WRITABLE") || !strings.Contains(loc, dir) {
			t.Errorf("location = %q, want it to name the path and the problem", loc)
		}
	})

	t.Run("reports an empty store with the fix", func(t *testing.T) {
		useTokenStore(t)
		st := describeTokenStore("google")
		if desc := st.describe(googleTokenPolicy); !strings.Contains(desc, "ads login google") {
			t.Errorf("describe = %q, want it to name the sign-in command", desc)
		}
	})
}

func TestHumanizeAge(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{90 * time.Minute, "1 hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{25 * time.Hour, "1 day ago"},
		{72 * time.Hour, "3 days ago"},
		{-time.Hour, "in the future (check the system clock)"},
	} {
		if got := humanizeAge(tc.d); got != tc.want {
			t.Errorf("humanizeAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestWarnOnce_DeduplicatesWithinAProcess(t *testing.T) {
	warnings := captureWarnings(t)
	for range 3 {
		warnOnce("token store at %s is unwritable", "/tmp/x")
	}
	warnOnce("a different notice")
	if n := strings.Count(warnings.String(), "unwritable"); n != 1 {
		t.Errorf("repeated notice printed %d times, want 1:\n%s", n, warnings.String())
	}
	if !strings.Contains(warnings.String(), "a different notice") {
		t.Errorf("a distinct notice should still print:\n%s", warnings.String())
	}
}

// --- Google wiring ---

func TestGoogleConfig_MigratesConfigFileTokenAndStripsTheKey(t *testing.T) {
	useTokenStore(t)
	clearAdsEnv(t)
	warnings := captureWarnings(t)

	path := filepath.Join(t.TempDir(), "config.toml")
	body := "developer_token = \"devtok\"\nclient_id = \"cid\"\nclient_secret = \"csec\"\nrefresh_token = \"rt-from-file\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadGoogleConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshToken != "rt-from-file" {
		t.Errorf("effective refresh token = %q, want the migrated value", cfg.RefreshToken)
	}
	stored, err := readStoredToken("google")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RefreshToken != "rt-from-file" {
		t.Fatalf("config-file token not migrated: %+v", stored)
	}
	if !strings.Contains(warnings.String(), path) {
		t.Errorf("the notice should name the file the token came from: %q", warnings.String())
	}

	// The key is gone from the file, and every other key survived.
	reloaded, err := loadGoogleConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.tomlRefreshToken != "" {
		t.Errorf("refresh_token should have been retired from %s", path)
	}
	if reloaded.DeveloperToken != "devtok" || reloaded.ClientID != "cid" || reloaded.ClientSecret != "csec" {
		t.Errorf("retiring the key disturbed other settings: %+v", reloaded)
	}
}

func TestGoogleConfig_EnvSeedIsMigratedAndThenIgnored(t *testing.T) {
	useTokenStore(t)
	clearAdsEnv(t)
	captureWarnings(t)
	t.Setenv("GOOGLE_ADS_REFRESH_TOKEN", "rt-env")

	first, err := loadGoogleConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}
	if first.RefreshToken != "rt-env" {
		t.Fatalf("first run should accept the env seed, got %q", first.RefreshToken)
	}

	// The user rotates their token in Google's console and re-runs `ads login`,
	// but leaves the stale variable in their MCP host config.
	if err := writeStoredToken("google", &storedToken{RefreshToken: "rt-relogin", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	second, err := loadGoogleConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken != "rt-relogin" {
		t.Errorf("the saved sign-in must win over the stale env var, got %q", second.RefreshToken)
	}
}

func TestGoogleConfig_ResolveIsSkippedInTestMode(t *testing.T) {
	dir := useTokenStore(t)
	clearAdsEnv(t)
	captureWarnings(t)

	cfg := &GoogleConfig{RefreshToken: "rt", BaseURL: "http://127.0.0.1:1"}
	if err := cfg.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		t.Errorf("a loopback base URL must not touch the token store: %v", entries)
	}
}

func TestGoogleConfig_ResolveRunsOnce(t *testing.T) {
	useTokenStore(t)
	clearAdsEnv(t)
	captureWarnings(t)
	t.Setenv("GOOGLE_ADS_REFRESH_TOKEN", "rt-env")

	cfg, err := loadGoogleConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}
	// Overwrite the store behind the config's back: a second resolve must be a
	// no-op, not a re-read that swaps the token mid-command.
	if err := writeStoredToken("google", &storedToken{RefreshToken: "changed", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}
	if cfg.RefreshToken != "rt-env" {
		t.Errorf("second resolve should be a no-op, got %q", cfg.RefreshToken)
	}
}

func TestUseTempState_IsolatesAnExportedStoreOverride(t *testing.T) {
	// A developer with GOADS_TOKEN_STORE exported must not have the suite write
	// fixture tokens over their real sign-in.
	real := filepath.Join(t.TempDir(), "real-tokens")
	t.Setenv(tokenStoreEnv, real)

	t.Run("sandboxed", func(t *testing.T) {
		useTempState(t)
		if err := writeStoredToken("google", &storedToken{RefreshToken: "fixture"}); err != nil {
			t.Fatal(err)
		}
		path, err := tokenStorePath("google")
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(path, real) {
			t.Errorf("store path %q is still the exported one", path)
		}
	})

	if _, err := os.Stat(filepath.Join(real, "google.json")); !os.IsNotExist(err) {
		t.Errorf("the exported store was written to (stat err = %v)", err)
	}
}

func TestSaveGoogleCredentials_KeepsTheOldSignInWhenTheConfigCannotBeWritten(t *testing.T) {
	useTempState(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := writeStoredToken("google", &storedToken{RefreshToken: "rt-working", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := saveGoogleCredentials(filepath.Join(dir, "config.toml"), clientCreds{clientID: "new", clientSecret: "new"}, "rt-new")
	if err == nil {
		t.Fatal("expected the unwritable config to fail the save")
	}
	// A sign-in that failed must leave the working one alone, rather than
	// pairing a new refresh token with the old client credentials.
	stored, err := readStoredToken("google")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RefreshToken != "rt-working" {
		t.Errorf("a failed save replaced the working sign-in: %+v", stored)
	}
}

func TestSaveGoogleCredentials_LeavesTheConfigAloneWhenTheStoreIsUnwritable(t *testing.T) {
	dir := readOnlyDir(t)
	t.Setenv(tokenStoreEnv, dir)

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	original := "client_id = \"old-client\"\nclient_secret = \"old-secret\"\nrefresh_token = \"rt-old\"\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := saveGoogleCredentials(cfgPath, clientCreds{clientID: "new-client", clientSecret: "new-secret"}, "rt-new")
	if err == nil {
		t.Fatal("expected the unwritable store to fail the save")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the store path: %v", err)
	}
	// A refresh token is bound to the client that minted it, so a half-written
	// sign-in must not replace the client while the token stays behind.
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("config was modified by a failed save:\n%s", got)
	}
}

func TestResolveRefreshToken_WarnsWhenTheSavedSignInBelongsToAnotherClient(t *testing.T) {
	useTokenStore(t)
	err := writeStoredToken(staticPolicy.Platform, &storedToken{
		RefreshToken: "rt", UpdatedAt: time.Now().UTC(), ClientID: "client-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("mismatched client is named", func(t *testing.T) {
		warnings := captureWarnings(t)
		if _, err := resolveRefreshToken(staticPolicy, "client-b", nil); err != nil {
			t.Fatal(err)
		}
		// A refresh token only works with the client that minted it, so say so
		// here rather than letting it surface as a bare invalid_grant.
		for _, want := range []string{"client-a", "client-b", "ads login static"} {
			if !strings.Contains(warnings.String(), want) {
				t.Errorf("warning should name %q: %q", want, warnings.String())
			}
		}
	})

	t.Run("matching client is silent", func(t *testing.T) {
		warnings := captureWarnings(t)
		if _, err := resolveRefreshToken(staticPolicy, "client-a", nil); err != nil {
			t.Fatal(err)
		}
		if warnings.String() != "" {
			t.Errorf("a matching client should not warn: %q", warnings.String())
		}
	})

	t.Run("unknown binding is silent", func(t *testing.T) {
		// Seeded tokens carry no binding; guessing one would invent a fact.
		warnings := captureWarnings(t)
		if err := writeStoredToken(staticPolicy.Platform, &storedToken{RefreshToken: "rt", UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveRefreshToken(staticPolicy, "client-b", nil); err != nil {
			t.Fatal(err)
		}
		if warnings.String() != "" {
			t.Errorf("an unknown binding should not warn: %q", warnings.String())
		}
	})
}

func TestPersistingTokenSource_RotationKeepsTheClientBinding(t *testing.T) {
	useTokenStore(t)
	captureWarnings(t)
	_, srv := newRotatingTokenServer(t, "rt-seed")

	ts := newTokenSource(t.Context(), oauthClient{
		tokenPolicy:  rotatingPolicy,
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
		ClientID:     "client-a",
		ClientSecret: "secret",
		RefreshToken: "rt-seed",
	})
	if _, err := ts.Token(); err != nil {
		t.Fatal(err)
	}
	stored, err := readStoredToken(rotatingPolicy.Platform)
	if err != nil {
		t.Fatal(err)
	}
	// Losing the binding on the first rotation would disable the mismatch
	// warning for exactly the platforms that rotate.
	if stored == nil || stored.ClientID != "client-a" {
		t.Errorf("rotation dropped the client binding: %+v", stored)
	}
}

func TestRetiringATokenKeepsCommentsAndFormatting(t *testing.T) {
	useTokenStore(t)
	clearAdsEnv(t)
	captureWarnings(t)

	path := filepath.Join(t.TempDir(), "config.toml")
	original := `# ads configuration — hand-written
developer_token = "devtok"   # from the API centre

# OAuth client (Desktop app)
client_id     = "cid"
client_secret = "csec"
refresh_token = "rt-from-file"

[limits]
refresh_token = "not the top-level one"
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadGoogleConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Migration is automatic — it happens on the first ordinary command after an
	// upgrade — so it must not quietly rewrite a file the user hand-authored.
	want := strings.Replace(original, "refresh_token = \"rt-from-file\"\n", "", 1)
	if string(got) != want {
		t.Errorf("retiring the key rewrote the file:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestDeleteConfigKey_LeavesFilesItCannotEditSafely(t *testing.T) {
	// A multi-line value the line scan would mangle: the re-parse check must
	// catch it and leave the file untouched rather than commit a broken config.
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "client_id = \"cid\"\nrefresh_token = \"\"\"\nspanning\nlines\n\"\"\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deleteConfigKey(path, "refresh_token"); err == nil {
		t.Error("expected an unsafe edit to be refused")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("a refused edit modified the file:\n%s", got)
	}
}

func TestRetiringATokenKeepsASymlinkedConfigIntact(t *testing.T) {
	useTokenStore(t)
	clearAdsEnv(t)
	captureWarnings(t)

	// A config.toml symlinked out of a dotfile repo, the way chezmoi or stow
	// lay one out.
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-config.toml")
	link := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(target, []byte("client_id = \"cid\"\nrefresh_token = \"rt-from-file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cfg, err := loadGoogleConfig(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.resolveRefreshToken(); err != nil {
		t.Fatal(err)
	}

	// Migration rewrites the config without being asked, so it must not quietly
	// detach the link and leave the real file stale.
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("migration replaced the symlink with a plain file")
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "refresh_token") {
		t.Errorf("the retired key survived in the symlink target:\n%s", body)
	}
	if !strings.Contains(string(body), "cid") {
		t.Errorf("the symlink target lost its other settings:\n%s", body)
	}
}

func TestSaveGoogleCredentials_KeepsTheOldPairWhenTheTokenCannotBeWritten(t *testing.T) {
	// google.json is a symlink into an unwritable directory, so the store looks
	// writable until the write actually lands there. Whether that is caught by
	// the up-front probe or by the rollback afterwards, the observable rule is
	// the same: the old client and the old token stay together.
	sealedSymlinkStore(t, "google", "rt-old")
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	original := "client_id = \"old-client\"\nclient_secret = \"old-secret\"\n"
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := saveGoogleCredentials(cfgPath, clientCreds{clientID: "new-client", clientSecret: "new-secret"}, "rt-new")
	if err == nil {
		t.Fatal("expected the unwritable store target to fail the save")
	}
	// The old client must still be paired with the old token, rather than a new
	// client left stranded next to a refresh token it cannot use.
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("the client config was not rolled back:\n%s", got)
	}
	stored, err := readStoredToken("google")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.RefreshToken != "rt-old" {
		t.Errorf("the old sign-in was disturbed: %+v", stored)
	}
}

func TestWriteFileAtomic_FollowsADanglingSymlink(t *testing.T) {
	// A deployment or dotfile tool stages the link before the file exists.
	base := t.TempDir()
	target := filepath.Join(base, "real", "google.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "google.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := writeFileAtomic(link, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the write replaced the symlink with a plain file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the link target was never written: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("target holds %q, want the written payload", got)
	}
}

func TestSnapshotFile_UndoesAWriteMadeThroughADanglingSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real", "config.toml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	restore, err := snapshotFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("client_id = \"new\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}

	// The undo must remove what the write created, not the link it went through.
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("the written file survived the rollback (stat err = %v)", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("the symlink was destroyed: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a plain file")
	}
}

func TestDescribeTokenStore_FollowsTheSymlinkWhenProbing(t *testing.T) {
	sealedSymlinkStore(t, "google", "rt-saved")

	st := describeTokenStore("google")
	// The store directory is writable; the link target is not. Reporting the
	// former would promise a sign-in that cannot actually be saved.
	if st.WriteErr == nil {
		t.Fatal("a store symlinked into an unwritable directory must report as unwritable")
	}
	if loc := st.location(); !strings.Contains(loc, "NOT WRITABLE") {
		t.Errorf("location = %q, want it to report the problem", loc)
	}
}

func TestDescribeTokenStore_DoesNotCreateTheStore(t *testing.T) {
	// A mistyped GOADS_TOKEN_STORE must not be conjured into existence by the
	// command run to find out what is wrong.
	base := t.TempDir()
	dir := filepath.Join(base, "typo", "tokens")
	t.Setenv(tokenStoreEnv, dir)

	st := describeTokenStore("google")
	if st.WriteErr != nil {
		t.Errorf("a creatable store should report as writable: %v", st.WriteErr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("describing the store created it (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Errorf("describing the store created its parent (stat err = %v)", err)
	}
}

func TestGoogleShowConfig_UnreadableStoreIsNotMaskedByASeed(t *testing.T) {
	dir := useTokenStore(t)
	clearAdsEnv(t)
	t.Setenv("GOOGLE_ADS_REFRESH_TOKEN", "seed-value-1234")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "google.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := googleShowConfig(&out); err != nil {
		t.Fatal(err)
	}
	// Real commands fail on the corrupt store, so `config show` must not report
	// the deprecated seed as the sign-in in effect.
	if strings.Contains(out.String(), "set (…1234)") {
		t.Errorf("a corrupt store was masked by the deprecated seed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "unreadable") {
		t.Errorf("config show should report the store as unreadable:\n%s", out.String())
	}
}

func TestNewClient_ResolvesTheSavedSignIn(t *testing.T) {
	useTokenStore(t)
	clearAdsEnv(t)
	captureWarnings(t)
	if err := writeStoredToken("google", &storedToken{RefreshToken: "rt-saved", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	cfg := &GoogleConfig{DeveloperToken: "d", ClientID: "c", ClientSecret: "s", BaseURL: defaultBaseURL}
	if _, err := NewClient(context.Background(), cfg); err != nil {
		t.Fatalf("a saved sign-in should satisfy credential validation: %v", err)
	}
	if cfg.RefreshToken != "rt-saved" {
		t.Errorf("client built with %q, want the saved token", cfg.RefreshToken)
	}
}

func TestNewClient_WithoutASignInNamesTheLoginCommand(t *testing.T) {
	useTokenStore(t)
	clearAdsEnv(t)
	captureWarnings(t)

	cfg := &GoogleConfig{DeveloperToken: "d", ClientID: "c", ClientSecret: "s", BaseURL: defaultBaseURL}
	_, err := NewClient(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected missing credentials without a saved sign-in")
	}
	if !strings.Contains(err.Error(), "ads login google") {
		t.Errorf("error should name the sign-in command: %v", err)
	}
}
