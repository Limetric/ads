package main

import (
	"context"

	"golang.org/x/oauth2"
)

// staticTokenSource always returns the same token. Offline mode uses it so the
// suite never needs real OAuth credentials.
type staticTokenSource struct{ tok *oauth2.Token }

func (s staticTokenSource) Token() (*oauth2.Token, error) { return s.tok, nil }

// oauthClient describes one platform's installed-app OAuth2 flow. Every ad
// network we target uses the same shape — client_id + client_secret + a
// long-lived refresh_token exchanged for short-lived access tokens — and
// differs only in the token endpoint, so a platform supplies one of these
// (see (*GoogleConfig).oauth) instead of auth.go knowing the platforms.
type oauthClient struct {
	// Endpoint is the platform's OAuth2 authorization/token endpoint.
	Endpoint oauth2.Endpoint
	// ClientID / ClientSecret identify the OAuth application.
	ClientID, ClientSecret string
	// RefreshToken is the long-lived grant access tokens are minted from.
	RefreshToken string
	// Offline skips token minting entirely, for a platform pointed at a local
	// test server rather than its real API.
	Offline bool
}

// newTokenSource builds an oauth2.TokenSource for a platform's credentials.
// oauth2.TokenSource caches the access token and refreshes it automatically
// when it expires.
func newTokenSource(ctx context.Context, oc oauthClient) oauth2.TokenSource {
	if oc.Offline {
		return staticTokenSource{tok: &oauth2.Token{AccessToken: "test-access-token"}}
	}
	conf := &oauth2.Config{
		ClientID:     oc.ClientID,
		ClientSecret: oc.ClientSecret,
		Endpoint:     oc.Endpoint,
	}
	// A token carrying only a refresh token; the source mints access tokens.
	return conf.TokenSource(ctx, &oauth2.Token{RefreshToken: oc.RefreshToken})
}
