package main

import "testing"

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
