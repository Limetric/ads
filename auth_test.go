package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestNewTokenSource(t *testing.T) {
	t.Run("test mode uses a static token", func(t *testing.T) {
		cfg := &GoogleConfig{BaseURL: "http://127.0.0.1:1"} // non-prod base URL → test mode
		tok, err := newTokenSource(t.Context(), cfg.oauth()).Token()
		if err != nil {
			t.Fatal(err)
		}
		if tok.AccessToken != "test-access-token" {
			t.Errorf("access token = %q, want test-access-token", tok.AccessToken)
		}
	})

	t.Run("real credentials build a refreshing source", func(t *testing.T) {
		cfg := &GoogleConfig{ClientID: "id", ClientSecret: "sec", RefreshToken: "rt"}
		if ts := newTokenSource(t.Context(), cfg.oauth()); ts == nil {
			t.Fatal("expected a token source for real credentials")
		}
	})
}

// tokenEndpoint fakes an OAuth token endpoint, recording the form it was sent.
func tokenEndpoint(t *testing.T, status int, body string) (*httptest.Server, *url.Values) {
	t.Helper()
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, body)
	}))
	return srv, &form
}

func TestScopedRefreshSource_SendsScopeAndNoCode(t *testing.T) {
	srv, form := tokenEndpoint(t, http.StatusOK, `{"access_token":"at","refresh_token":"rt2","token_type":"Bearer","expires_in":3600}`)
	defer srv.Close()

	src := &scopedRefreshSource{
		ctx:          t.Context(),
		conf:         &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}, Scopes: bingScopes},
		refreshToken: "rt1",
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt2" {
		t.Fatalf("token = %+v", tok)
	}
	// Entra documents scope as required on the refresh grant, and x/oauth2's
	// own refresher never sends it — which is the whole reason this type exists.
	if got := form.Get("scope"); !strings.Contains(got, "msads.manage") {
		t.Errorf("scope = %q", got)
	}
	if form.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q", form.Get("grant_type"))
	}
	// A refresh grant carrying an empty `code` is not a request worth
	// explaining to a provider.
	if _, ok := (*form)["code"]; ok {
		t.Errorf("a refresh must not send a code parameter: %v", *form)
	}
	if _, ok := (*form)["client_secret"]; ok {
		t.Error("a public client must not send a secret")
	}
}

func TestScopedRefreshSource_KeepsTheOldTokenWhenNoneIsReturned(t *testing.T) {
	srv, _ := tokenEndpoint(t, http.StatusOK, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
	defer srv.Close()

	src := &scopedRefreshSource{
		ctx:          t.Context(),
		conf:         &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}, Scopes: bingScopes},
		refreshToken: "rt1",
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatal(err)
	}
	// A provider that does not rotate leaves the field empty; dropping the
	// token we already have would break the next call.
	if tok.RefreshToken != "rt1" {
		t.Errorf("refresh token = %q, want the existing one carried forward", tok.RefreshToken)
	}
}

func TestScopedRefreshSource_RejectionIsClassifiable(t *testing.T) {
	srv, _ := tokenEndpoint(t, http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"The user could not be authenticated or the grant is expired."}`)
	defer srv.Close()

	src := &scopedRefreshSource{
		ctx:          t.Context(),
		conf:         &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}, Scopes: bingScopes},
		refreshToken: "rt1",
	}
	_, err := src.Token()
	if err == nil {
		t.Fatal("expected the rejection to surface")
	}
	// Everything downstream classifies on this type: doctor reads its status,
	// and tokenPolicy.authError turns invalid_grant into "sign in again".
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) {
		t.Fatalf("error = %T, want *oauth2.RetrieveError", err)
	}
	if retrieve.ErrorCode != "invalid_grant" {
		t.Errorf("ErrorCode = %q", retrieve.ErrorCode)
	}
	if liveVerdictFor(bingTokenPolicy.authError(err)) != liveFailed {
		t.Error("an invalid_grant is a broken setup, not a transient one")
	}
	if !strings.Contains(bingTokenPolicy.authError(err).Error(), "ads login bing") {
		t.Errorf("the error should name the fix: %v", bingTokenPolicy.authError(err))
	}
}

func TestNewTokenSource_PersistsARotatedRefreshToken(t *testing.T) {
	useTempState(t)
	srv, _ := tokenEndpoint(t, http.StatusOK, `{"access_token":"at","refresh_token":"rt2","token_type":"Bearer","expires_in":3600}`)
	defer srv.Close()

	prev := bingOAuthEndpointOverride
	bingOAuthEndpointOverride = &oauth2.Endpoint{TokenURL: srv.URL, AuthURL: srv.URL}
	t.Cleanup(func() { bingOAuthEndpointOverride = prev })

	cfg := &BingConfig{ClientID: "cid"}
	if _, err := newTokenSource(t.Context(), cfg.oauth("rt1")).Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}
	// This is what makes a rotating platform survive across separate process
	// invocations: the replacement has to reach the store, or the next run
	// presents a token Microsoft already invalidated.
	stored, err := readStoredToken(bingTokenPolicy.Platform)
	if err != nil || stored == nil {
		t.Fatalf("readStoredToken = (%v, %v)", stored, err)
	}
	if stored.RefreshToken != "rt2" {
		t.Errorf("stored refresh token = %q, want the rotated one", stored.RefreshToken)
	}
	if stored.ClientID != "cid" {
		t.Errorf("the client binding must survive a rotation, got %q", stored.ClientID)
	}
}
