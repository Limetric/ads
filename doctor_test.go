package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

// liveServer fakes the two endpoints doctor's live check probes: the metadata
// listAccessibleCustomers call (always 200 here) and the customer_client
// search. searchStatus/searchBody control what the search endpoint returns.
func liveServer(t *testing.T, searchStatus int, searchBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/customers:listAccessibleCustomers"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resourceNames":["customers/2033556075","customers/5987166041"]}`))
		case strings.HasSuffix(r.URL.Path, "/googleAds:search"):
			w.Header().Set("Content-Type", "application/json")
			if searchStatus != 0 && searchStatus != http.StatusOK {
				w.WriteHeader(searchStatus)
			}
			_, _ = w.Write([]byte(searchBody))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

const testTokenNotApproved = `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"The caller does not have permission","details":[{"@type":"type.googleapis.com/google.ads.googleads.v23.errors.GoogleAdsFailure","errors":[{"errorCode":{"authorizationError":"DEVELOPER_TOKEN_NOT_APPROVED"},"message":"The developer token is only approved for use with test accounts. To access non-test accounts, apply for Basic or Standard access."}]}]}}`

func TestRunGoogleDoctorLive_Healthy(t *testing.T) {
	srv := liveServer(t, http.StatusOK, `{"results":[{"customerClient":{"id":"5987166041"}}]}`)
	defer srv.Close()

	var out bytes.Buffer
	cfg := &GoogleConfig{BaseURL: srv.URL, LoginCustomerID: "5987166041"}
	res, err := runGoogleDoctorLive(context.Background(), &out, cfg)
	if err != nil || res != liveOK {
		t.Fatalf("healthy setup: got (%v, %v), want (liveOK, nil)", res, err)
	}
	got := out.String()
	if !strings.Contains(got, "203-355-6075") || !strings.Contains(got, "598-716-6041") {
		t.Errorf("accessible accounts not listed:\n%s", got)
	}
	if strings.Contains(got, "✗") || strings.Contains(got, "?") {
		t.Errorf("healthy setup reported a problem:\n%s", got)
	}
}

// A test-level developer token passes listAccessibleCustomers but fails a real
// query with a definitive 403 — the live check must report NOT READY (liveFailed).
func TestRunGoogleDoctorLive_TestTokenIsDefinitiveFailure(t *testing.T) {
	srv := liveServer(t, http.StatusForbidden, testTokenNotApproved)
	defer srv.Close()

	var out bytes.Buffer
	cfg := &GoogleConfig{BaseURL: srv.URL, LoginCustomerID: "5987166041"}
	res, err := runGoogleDoctorLive(context.Background(), &out, cfg)
	if err == nil || res != liveFailed {
		t.Fatalf("test-only token: got (%v, %v), want (liveFailed, err)", res, err)
	}
	got := out.String()
	if !strings.Contains(got, "203-355-6075") {
		t.Errorf("accessible accounts should still be listed:\n%s", got)
	}
	if !strings.Contains(got, "✗") || !strings.Contains(got, "DEVELOPER_TOKEN_NOT_APPROVED") || !strings.Contains(got, "apply for Basic or Standard access") {
		t.Errorf("live query error not surfaced as a definitive failure:\n%s", got)
	}
}

// A 5xx from the API is the server's problem, not the user's setup: inconclusive.
func TestRunGoogleDoctorLive_ServerErrorIsInconclusive(t *testing.T) {
	srv := liveServer(t, http.StatusServiceUnavailable, `{"error":{"code":503,"status":"UNAVAILABLE","message":"backend unavailable"}}`)
	defer srv.Close()

	var out bytes.Buffer
	cfg := &GoogleConfig{BaseURL: srv.URL, LoginCustomerID: "5987166041"}
	res, err := runGoogleDoctorLive(context.Background(), &out, cfg)
	if err == nil || res != liveInconclusive {
		t.Fatalf("5xx: got (%v, %v), want (liveInconclusive, err)", res, err)
	}
	if !strings.Contains(out.String(), "?") {
		t.Errorf("5xx should be marked inconclusive (?):\n%s", out.String())
	}
}

// An unreachable API (no HTTP response at all) is inconclusive, not NOT READY —
// the plane-mode case: credentials are fine, we just couldn't check them.
func TestRunGoogleDoctorLive_UnreachableIsInconclusive(t *testing.T) {
	srv := liveServer(t, http.StatusOK, `{}`)
	url := srv.URL
	srv.Close() // free the port so the client gets connection-refused

	var out bytes.Buffer
	cfg := &GoogleConfig{BaseURL: url, LoginCustomerID: "5987166041"}
	res, err := runGoogleDoctorLive(context.Background(), &out, cfg)
	if err == nil || res != liveInconclusive {
		t.Fatalf("unreachable API: got (%v, %v), want (liveInconclusive, err)", res, err)
	}
	if !strings.Contains(out.String(), "?") || strings.Contains(out.String(), "✗") {
		t.Errorf("unreachable should be inconclusive (?), not a definitive failure (✗):\n%s", out.String())
	}
}

func TestRunGoogleDoctorLive_SkipsQueryWithoutLoginCustomerID(t *testing.T) {
	srv := liveServer(t, http.StatusOK, `{"results":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	cfg := &GoogleConfig{BaseURL: srv.URL} // no login_customer_id
	res, err := runGoogleDoctorLive(context.Background(), &out, cfg)
	if err != nil || res != liveOK {
		t.Fatalf("no login_customer_id: got (%v, %v), want (liveOK, nil)", res, err)
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("expected the live query to be skipped without a login_customer_id:\n%s", out.String())
	}
}

// liveVerdictFor is the classifier the whole feature hinges on: 4xx = the
// caller's problem (definitive), everything else (5xx, transport, token) =
// inconclusive.
func TestLiveVerdictFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want liveResult
	}{
		{"nil", nil, liveOK},
		{"403 client error", &apiStatusError{status: 403, msg: "forbidden"}, liveFailed},
		{"400 client error", &apiStatusError{status: 400, msg: "bad request"}, liveFailed},
		{"503 server error", &apiStatusError{status: 503, msg: "unavailable"}, liveInconclusive},
		{"wrapped 403", toolError("list_accounts", &apiStatusError{status: 403, msg: "forbidden"}), liveFailed},
		{"transport error", context.DeadlineExceeded, liveInconclusive},
	}
	for _, c := range cases {
		if got := liveVerdictFor(c.err); got != c.want {
			t.Errorf("%s: liveVerdictFor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLiveVerdictFor_OAuthInvalidGrantIsDefinitive(t *testing.T) {
	// A 4xx from the token endpoint (revoked/mistyped refresh token) is a
	// broken setup, not an inconclusive network problem (issue #11).
	retrieveErr := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, Body: []byte(`{"error":"invalid_grant"}`)}
	wrapped := fmt.Errorf("obtain access token: %w", retrieveErr)
	if got := liveVerdictFor(wrapped); got != liveFailed {
		t.Fatalf("invalid_grant should be liveFailed, got %v", got)
	}
	// A 5xx from the token endpoint stays inconclusive.
	serverErr := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 503}}
	if got := liveVerdictFor(fmt.Errorf("obtain access token: %w", serverErr)); got != liveInconclusive {
		t.Fatalf("token-endpoint 5xx should be liveInconclusive, got %v", got)
	}
}
