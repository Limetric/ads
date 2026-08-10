//go:build integration

// Live smoke tests against the real Google Ads API. These are excluded from the
// default offline suite and only run with the `integration` build tag:
//
//	GOOGLE_ADS_DEVELOPER_TOKEN=… GOOGLE_ADS_CLIENT_ID=… GOOGLE_ADS_CLIENT_SECRET=… \
//	GOOGLE_ADS_REFRESH_TOKEN=… GOOGLE_ADS_LOGIN_CUSTOMER_ID=… \
//	go test -tags integration -count=1 -v ./...
//
// They are read-only: no test here stages or applies a mutation.
package main

import (
	"context"
	"os"
	"testing"
)

// liveClient builds a real API client from the environment, skipping the test
// when credentials are not configured.
func liveClient(t *testing.T) *Client {
	t.Helper()
	cfg, err := loadGoogleConfig("")
	if err != nil {
		t.Fatalf("loadGoogleConfig: %v", err)
	}
	if cfg.isTest() {
		t.Skip("GOOGLE_ADS_API_BASE_URL points away from production; skipping live test")
	}
	// The refresh token comes from the token store, so resolve before deciding
	// whether credentials are configured — otherwise a store-only setup (the
	// recommended one) reports "not configured" and silently skips every live
	// test it was supposed to run.
	if err := cfg.resolveRefreshToken(); err != nil {
		t.Skipf("token store unavailable: %v", err)
	}
	if err := cfg.validate(); err != nil {
		t.Skipf("live credentials not configured: %v", err)
	}
	c, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// TestLiveListAccounts exercises the accounts read against the real API.
func TestLiveListAccounts(t *testing.T) {
	c := liveClient(t)
	res, err := runAccounts(context.Background(), c, AccountsArgs{})
	if err != nil {
		t.Fatalf("runAccounts: %v", err)
	}
	t.Logf("reachable customer accounts: %d", len(res.CustomerIDs))
}

// TestLiveSearch runs a trivial GAQL read. Set GOADS_TEST_CUSTOMER_ID to a
// customer you can access; falls back to the login customer ID.
func TestLiveSearch(t *testing.T) {
	c := liveClient(t)
	customerID := os.Getenv("GOADS_TEST_CUSTOMER_ID")
	if customerID == "" {
		customerID = c.cfg.LoginCustomerID
	}
	if customerID == "" {
		t.Skip("set GOADS_TEST_CUSTOMER_ID or GOOGLE_ADS_LOGIN_CUSTOMER_ID to run the live search")
	}
	res, err := runSearch(context.Background(), c, SearchArgs{
		CustomerID: customerID,
		Query:      "SELECT customer.id, customer.descriptive_name FROM customer LIMIT 1",
	})
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if res.RowCount == 0 {
		t.Error("expected at least one customer row")
	}
}
