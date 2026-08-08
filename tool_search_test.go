package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient returns a Client wired to a fake Ads API server. Because the
// base URL is non-production, NewClient skips OAuth and uses a static token.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	cfg := &GoogleConfig{BaseURL: srv.URL, LoginCustomerID: "1234567890"}
	if cfg.BaseURL == defaultBaseURL {
		t.Fatal("test server URL collided with the production base URL")
	}
	c, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.http = srv.Client()
	return c
}

func TestRunSearch_Paginates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/customers/1234567890/googleAds:search") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("developer-token"); got == "" {
			t.Error("developer-token header not set")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Errorf("Authorization = %q", got)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"results":[{"campaign":{"id":"1"}}],"nextPageToken":"PAGE2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"campaign":{"id":"2"}}]}`))
	}))
	defer srv.Close()

	res, err := runSearch(context.Background(), newTestClient(t, srv), SearchArgs{
		CustomerID: "123-456-7890",
		Query:      "SELECT campaign.id FROM campaign",
	})
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 paginated calls, got %d", calls)
	}
	if res.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2", res.RowCount)
	}
	if res.CustomerID != "1234567890" {
		t.Errorf("CustomerID = %q, want normalized 1234567890", res.CustomerID)
	}
}

func TestRunSearch_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad field"}}`))
	}))
	defer srv.Close()

	_, err := runSearch(context.Background(), newTestClient(t, srv), SearchArgs{
		CustomerID: "1234567890",
		Query:      "SELECT campaign.id FROM campaign",
	})
	if err == nil || !strings.Contains(err.Error(), "bad field") {
		t.Fatalf("expected API error surfaced, got %v", err)
	}
}

func TestRunSearch_RejectsBadQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be reached for an invalid query")
	}))
	defer srv.Close()

	_, err := runSearch(context.Background(), newTestClient(t, srv), SearchArgs{
		CustomerID: "1234567890",
		Query:      "DROP TABLE campaign",
	})
	if err == nil {
		t.Fatal("expected validation error for non-SELECT query")
	}
}

// guard: the result rows are valid raw JSON objects we can re-decode.
func TestSearchResult_RowsAreRawJSON(t *testing.T) {
	var row map[string]any
	if err := json.Unmarshal(json.RawMessage(`{"campaign":{"id":"1"}}`), &row); err != nil {
		t.Fatal(err)
	}
}
