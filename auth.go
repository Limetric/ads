package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// staticTokenSource always returns the same token. Offline mode uses it so the
// suite never needs real OAuth credentials.
type staticTokenSource struct{ tok *oauth2.Token }

func (s staticTokenSource) Token() (*oauth2.Token, error) { return s.tok, nil }

// oauthClient describes one platform's installed-app OAuth2 flow. Every ad
// network we target uses the same shape — client_id + client_secret + a
// refresh_token exchanged for short-lived access tokens — and differs only in
// the token endpoint and whether the refresh token rotates, so a platform
// supplies one of these (see (*GoogleConfig).oauth) instead of auth.go knowing
// the platforms.
type oauthClient struct {
	// tokenPolicy names the platform and says whether its refresh token
	// rotates, which is what the write-back path needs (see token_store.go).
	tokenPolicy
	// Endpoint is the platform's OAuth2 authorization/token endpoint.
	Endpoint oauth2.Endpoint
	// ClientID / ClientSecret identify the OAuth application.
	ClientID, ClientSecret string
	// RefreshToken is the grant access tokens are minted from, as already
	// resolved from the token store.
	RefreshToken string
	// Scopes, when set, are re-sent with every refresh. Google's endpoint
	// carries the granted scopes on the refresh token itself and needs none;
	// Microsoft Entra documents `scope` as required on the refresh grant, and
	// x/oauth2's built-in refresher never sends it. A platform that needs it
	// says so here.
	Scopes []string
	// Offline skips token minting entirely, for a platform pointed at a local
	// test server rather than its real API.
	Offline bool
}

// newTokenSource builds an oauth2.TokenSource for a platform's credentials.
// oauth2.TokenSource caches the access token and refreshes it automatically
// when it expires; the wrapper saves the refresh token back to the store when a
// rotating platform replaces it.
func newTokenSource(ctx context.Context, oc oauthClient) oauth2.TokenSource {
	if oc.Offline {
		return staticTokenSource{tok: &oauth2.Token{AccessToken: "test-access-token"}}
	}
	conf := &oauth2.Config{
		ClientID:     oc.ClientID,
		ClientSecret: oc.ClientSecret,
		Endpoint:     oc.Endpoint,
		Scopes:       oc.Scopes,
	}
	// A token carrying only a refresh token; the source mints access tokens.
	var src oauth2.TokenSource = conf.TokenSource(ctx, &oauth2.Token{RefreshToken: oc.RefreshToken})
	if len(oc.Scopes) > 0 {
		// ReuseTokenSource caches the access token in memory and only calls
		// through when it expires, exactly as conf.TokenSource would.
		src = oauth2.ReuseTokenSource(nil, &scopedRefreshSource{
			ctx:          ctx,
			conf:         conf,
			refreshToken: oc.RefreshToken,
		})
	}
	return &persistingTokenSource{
		policy:   oc.tokenPolicy,
		clientID: oc.ClientID,
		src:      src,
		// Already in the store, so an unchanged refresh token writes nothing.
		current: oc.RefreshToken,
	}
}

// scopedRefreshSource redeems a refresh token with the scopes attached, which
// x/oauth2's own refresher does not do.
//
// It is a plain refresh_token grant otherwise, so its failures still surface as
// *oauth2.RetrieveError — the type doctor classifies and tokenPolicy.authError
// turns into "run `ads login <platform>`".
//
// Each call carries the refresh token this source was built with rather than
// the last one the provider handed back: the rotated value is persisted by
// persistingTokenSource, which wraps this, and a process that outlives one
// rotation re-reads it from the store on its next run.
type scopedRefreshSource struct {
	ctx          context.Context
	conf         *oauth2.Config
	refreshToken string
}

func (s *scopedRefreshSource) Token() (*oauth2.Token, error) {
	if s.refreshToken == "" {
		return nil, errors.New("no refresh token to redeem")
	}
	// Written out rather than routed through Config.Exchange: that helper always
	// sends a `code` parameter, and a refresh grant carrying an empty code is
	// not a request worth explaining to a provider.
	form := url.Values{
		"client_id":     {s.conf.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {s.refreshToken},
		"scope":         {strings.Join(s.conf.Scopes, " ")},
	}
	if s.conf.ClientSecret != "" {
		form.Set("client_secret", s.conf.ClientSecret)
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.conf.Endpoint.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh access token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	var payload struct {
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &payload)
	if resp.StatusCode >= 300 || payload.AccessToken == "" {
		// *oauth2.RetrieveError is the type the rest of ads classifies on:
		// doctor reads its status, and tokenPolicy.authError turns
		// invalid_grant into "sign in again".
		return nil, &oauth2.RetrieveError{
			Response:         resp,
			Body:             body,
			ErrorCode:        payload.Error,
			ErrorDescription: payload.ErrorDescription,
		}
	}
	tok := &oauth2.Token{
		AccessToken:  payload.AccessToken,
		TokenType:    payload.TokenType,
		RefreshToken: payload.RefreshToken,
	}
	if payload.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	// A provider that rotates its refresh token sends the replacement here; one
	// that doesn't leaves the field empty, and the caller keeps using the token
	// it already has.
	if tok.RefreshToken == "" {
		tok.RefreshToken = s.refreshToken
	}
	return tok, nil
}
