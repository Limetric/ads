package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunAccounts(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotQuery = body.Query
		w.Header().Set("Content-Type", "application/json")
		// Third row is malformed (no id) and must be skipped.
		_, _ = w.Write([]byte(`{"results":[{"customerClient":{"id":"111"}},{"customerClient":{"id":"222"}},{"customerClient":{}}]}`))
	}))
	defer srv.Close()

	res, err := runAccounts(t.Context(), newTestClient(t, srv), AccountsArgs{})
	if err != nil {
		t.Fatalf("runAccounts: %v", err)
	}
	if len(res.CustomerIDs) != 2 || res.CustomerIDs[0] != "111" || res.CustomerIDs[1] != "222" {
		t.Errorf("CustomerIDs = %v, want [111 222] (malformed row skipped)", res.CustomerIDs)
	}
	// Accounts are listed by querying the login customer as the search root.
	if !strings.HasSuffix(gotPath, "/customers/1234567890/googleAds:search") {
		t.Errorf("path = %q, should query the login customer as root", gotPath)
	}
	if !strings.Contains(gotQuery, "FROM customer_client") || !strings.Contains(gotQuery, "customer_client.status = 'ENABLED'") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestRunAccounts_APIError(t *testing.T) {
	srv := errServer(t)
	defer srv.Close()
	if _, err := runAccounts(t.Context(), newTestClient(t, srv), AccountsArgs{}); err == nil {
		t.Fatal("expected an API error to surface")
	}
}

func TestRunAccounts_FallsBackWithoutLoginCustomerID(t *testing.T) {
	// Without a login customer ID the old query built the malformed path
	// customers//googleAds:search and 404ed (issue #14); it now falls back to
	// listAccessibleCustomers.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "customers:listAccessibleCustomers") {
			_, _ = w.Write([]byte(`{"resourceNames":["customers/111","customers/222"]}`))
			return
		}
		t.Errorf("unexpected path %q", r.URL.Path)
	}))
	defer srv.Close()
	cfg := &GoogleConfig{BaseURL: srv.URL} // no LoginCustomerID
	c, err := NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := runAccounts(t.Context(), c, AccountsArgs{})
	if err != nil {
		t.Fatalf("runAccounts: %v", err)
	}
	if len(res.CustomerIDs) != 2 || res.CustomerIDs[0] != "111" {
		t.Fatalf("CustomerIDs = %v", res.CustomerIDs)
	}
	if res.Message == "" {
		t.Error("fallback should explain itself in Message")
	}
}

func TestRunAccounts_IncludesNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"customerClient":{"id":"333","descriptiveName":"Acme"}}]}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	res, err := runAccounts(t.Context(), c, AccountsArgs{})
	if err != nil {
		t.Fatal(err)
	}
	// The query always selected descriptive_name and then discarded it.
	if res.Names["333"] != "Acme" {
		t.Errorf("Names = %v, want 333->Acme", res.Names)
	}
}
