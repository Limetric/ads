package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestBingClient builds a client pointed at a test server. The loopback base
// URL is what puts the config in test mode: no OAuth, no real credentials.
func newTestBingClient(t *testing.T, srv *httptest.Server) *BingClient {
	t.Helper()
	return newTestBingClientWith(t, srv, &BingConfig{
		BaseURL:          srv.URL,
		CustomerID:       "777",
		DefaultAccountID: "123456",
	})
}

func newTestBingClientWith(t *testing.T, srv *httptest.Server, cfg *BingConfig) *BingClient {
	t.Helper()
	cfg.BaseURL = srv.URL
	if !cfg.isTest() {
		t.Fatalf("test server URL %q is not loopback; the client would try to authenticate", srv.URL)
	}
	c, err := NewBingClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewBingClient: %v", err)
	}
	c.http = srv.Client()
	return c
}

// bingJSONServer answers each request from a map of path suffix → JSON body.
func bingJSONServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		t.Errorf("unexpected request path %q", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestBingClient_SendsRequiredHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"Campaigns":[]}`))
	}))
	defer srv.Close()

	c := newTestBingClient(t, srv)
	if _, err := c.ListCampaigns(t.Context(), "123456"); err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	// All four are required by every campaign management operation.
	for header, want := range map[string]string{
		"Customerid":        "777",
		"Customeraccountid": "123456",
	} {
		if got.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, got.Get(header), want)
		}
	}
	if !strings.HasPrefix(got.Get("Authorization"), "Bearer ") {
		t.Errorf("Authorization = %q, want a Bearer token", got.Get("Authorization"))
	}
	if got.Get("Developertoken") == "" {
		t.Error("DeveloperToken header is required on every call")
	}
}

func TestBingClient_ResolveAccountID(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{"/": `{}`})
	defer srv.Close()
	c := newTestBingClient(t, srv)

	if got, err := c.resolveAccountID(""); err != nil || got != "123456" {
		t.Errorf("empty → (%q, %v), want the configured default", got, err)
	}
	if got, err := c.resolveAccountID(" 987-654 "); err != nil || got != "987654" {
		t.Errorf("explicit → (%q, %v), want it normalized", got, err)
	}
	// A non-blank value that normalizes to nothing must fail rather than
	// silently redirect the call to the default account.
	if _, err := c.resolveAccountID("---"); err == nil {
		t.Error("a garbage account ID must be rejected, not replaced by the default")
	}

	bare := newTestBingClientWith(t, srv, &BingConfig{})
	if _, err := bare.resolveAccountID(""); err == nil || !strings.Contains(err.Error(), "set-account") {
		t.Errorf("with no default the error should name the fix: %v", err)
	}
}

func TestBingClient_EntityReads(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Campaigns/QueryByAccountId"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Without an explicit CampaignType the service returns Search
			// campaigns only, silently hiding Shopping and Performance Max.
			if ct, _ := body["CampaignType"].(string); !strings.Contains(ct, "Shopping") {
				t.Errorf("CampaignType = %q, want every campaign type", ct)
			}
			_, _ = w.Write([]byte(`{"Campaigns":[{"Id":"1","Name":"Brand","Status":"Active","DailyBudget":25.5,"BudgetType":"DailyBudgetStandard","BudgetId":null}]}`))
		case strings.HasSuffix(r.URL.Path, "/AdGroups/QueryByCampaignId"):
			_, _ = w.Write([]byte(`{"AdGroups":[{"Id":"11","Name":"Core","Status":"Active","CpcBid":{"Amount":1.25},"StartDate":{"Day":3,"Month":2,"Year":2026}}]}`))
		case strings.HasSuffix(r.URL.Path, "/Keywords/QueryByAdGroupId"):
			_, _ = w.Write([]byte(`{"Keywords":[{"Id":"21","Text":"shoes","MatchType":"Exact","Status":"Active","Bid":{"Amount":0.75}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestBingClient(t, srv)

	campaigns, err := c.ListCampaigns(t.Context(), "123456")
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(campaigns) != 1 || campaigns[0].DailyBudget == nil || *campaigns[0].DailyBudget != 25.5 {
		t.Fatalf("campaigns = %+v", campaigns)
	}

	adGroups, err := c.ListAdGroups(t.Context(), "123456", "1")
	if err != nil {
		t.Fatalf("ListAdGroups: %v", err)
	}
	if len(adGroups) != 1 || adGroups[0].StartDate.String() != "2026-02-03" {
		t.Fatalf("ad groups = %+v (start date %q)", adGroups, adGroups[0].StartDate.String())
	}

	keywords, err := c.ListKeywords(t.Context(), "123456", "11")
	if err != nil {
		t.Fatalf("ListKeywords: %v", err)
	}
	if len(keywords) != 1 || keywords[0].Bid.value() == nil || *keywords[0].Bid.value() != 0.75 {
		t.Fatalf("keywords = %+v", keywords)
	}

	for _, want := range []string{
		"/CampaignManagement/v13/Campaigns/QueryByAccountId",
		"/CampaignManagement/v13/AdGroups/QueryByCampaignId",
		"/CampaignManagement/v13/Keywords/QueryByAdGroupId",
	} {
		if !containsString(paths, want) {
			t.Errorf("never called %s (paths: %v)", want, paths)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestBingClient_GetCampaignFiltersByID(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{
		"/Campaigns/QueryByAccountId": `{"Campaigns":[{"Id":"1","Name":"A"},{"Id":"2","Name":"B"}]}`,
	})
	defer srv.Close()
	c := newTestBingClient(t, srv)

	campaign, err := c.GetCampaign(t.Context(), "123456", "2")
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	if campaign == nil || campaign.Name != "B" {
		t.Fatalf("GetCampaign = %+v", campaign)
	}
	missing, err := c.GetCampaign(t.Context(), "123456", "99")
	if err != nil {
		t.Fatalf("GetCampaign(missing): %v", err)
	}
	if missing != nil {
		t.Errorf("a campaign that isn't there should come back nil, got %+v", missing)
	}
}

func TestBingError_ParsesEveryFaultShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "operation errors",
			body: `{"OperationErrors":[{"Code":105,"ErrorCode":"InvalidCredentials","Message":"Authentication failed."}]}`,
			want: "InvalidCredentials (105)",
		},
		{
			name: "errors",
			body: `{"Errors":[{"Code":117,"ErrorCode":"CallRateExceeded","Message":"Too many calls."}]}`,
			want: "CallRateExceeded (117)",
		},
		{
			name: "batch errors",
			body: `{"BatchErrors":[{"Code":1042,"ErrorCode":"CampaignServiceInvalidBudget","Message":"Bad budget","Index":0}]}`,
			want: "CampaignServiceInvalidBudget (1042)",
		},
		{
			name: "unparseable body falls back to the raw text",
			body: `<html>gateway error</html>`,
			want: "gateway error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := bingError(http.StatusBadRequest, bingCampaignService, "Campaigns", []byte(tc.body))
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestBingError_ThrottleClassification(t *testing.T) {
	tests := []struct {
		code          int
		wantThrottled bool
		wantAdvice    string
	}{
		{bingErrCallRateExceeded, true, "60 seconds"},
		{bingErrConcurrentRequestLimit, true, "report requests"},
		{bingErrBulkNoMoreCallsForNow, true, "15 minutes"},
		{1042, false, ""},
	}
	for _, tc := range tests {
		err := &bingAPIError{status: 400, items: []bingErrorItem{{Code: tc.code}}}
		if err.throttled() != tc.wantThrottled {
			t.Errorf("code %d: throttled = %v, want %v", tc.code, err.throttled(), tc.wantThrottled)
		}
		// A throttle is a 4xx, but it says nothing about whether the setup
		// works — doctor must not report it as a broken configuration.
		if tc.wantThrottled && err.isClientError() {
			t.Errorf("code %d: a throttle must not be classified as a definitive client error", tc.code)
		}
		if !tc.wantThrottled && !err.isClientError() {
			t.Errorf("code %d: a 4xx that isn't a throttle is a client error", tc.code)
		}
		if tc.wantAdvice != "" && !strings.Contains(err.Error(), tc.wantAdvice) {
			t.Errorf("code %d: error %q should say how long to wait (%q)", tc.code, err.Error(), tc.wantAdvice)
		}
	}
}

func TestBingClient_RetriesThrottleThenSucceeds(t *testing.T) {
	bingThrottleBaseDelay = time.Millisecond
	t.Cleanup(func() { bingThrottleBaseDelay = 2 * time.Second })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"Errors":[{"Code":117,"ErrorCode":"CallRateExceeded","Message":"slow down"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"Campaigns":[]}`))
	}))
	defer srv.Close()

	c := newTestBingClient(t, srv)
	if _, err := c.ListCampaigns(t.Context(), "123456"); err != nil {
		t.Fatalf("a throttled call should be retried: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want the throttled attempt to be retried once", calls.Load())
	}
}

func TestBingClient_GivesUpOnPersistentThrottleWithAdvice(t *testing.T) {
	bingThrottleBaseDelay = time.Millisecond
	t.Cleanup(func() { bingThrottleBaseDelay = 2 * time.Second })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"Errors":[{"Code":117,"ErrorCode":"CallRateExceeded","Message":"slow down"}]}`))
	}))
	defer srv.Close()

	c := newTestBingClient(t, srv)
	_, err := c.ListCampaigns(t.Context(), "123456")
	if err == nil {
		t.Fatal("a persistent throttle must surface")
	}
	if !strings.Contains(err.Error(), "60 seconds") {
		t.Errorf("the surviving error must say how long to wait: %v", err)
	}
	if got := calls.Load(); got != int32(bingThrottleRetryMaxAttempts) {
		t.Errorf("calls = %d, want %d", got, bingThrottleRetryMaxAttempts)
	}
}

func TestBingClient_DoesNotRetryWriteServerErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"OperationErrors":[{"Code":0,"ErrorCode":"InternalError"}]}`))
	}))
	defer srv.Close()

	c := newTestBingClient(t, srv)
	// A 5xx on a write may mean the change was applied; repeating it could
	// apply it twice.
	if _, err := c.UpdateCampaigns(t.Context(), "123456", []any{map[string]any{"Id": "1"}}); err == nil {
		t.Fatal("expected the server error to surface")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, a write must not retry a 5xx", calls.Load())
	}
}

func TestBingPartialErrors(t *testing.T) {
	if err := bingPartialErrors(nil, 3); err != nil {
		t.Errorf("no partial errors means success: %v", err)
	}
	index := 1
	err := bingPartialErrors([]bingErrorItem{{
		Code: 1042, ErrorCode: "InvalidBudget", Message: "too low", Index: &index,
	}}, 3)
	if err == nil {
		t.Fatal("a partial failure must not be reported as success")
	}
	// The message has to say which items applied and which did not — "ok" for a
	// half-applied batch is exactly the failure mode this guards.
	for _, want := range []string{"2 of 3", "item 1", "InvalidBudget"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

func TestBingClient_UpdateCampaignsSendsPutWithAccount(t *testing.T) {
	var method, path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"PartialErrors":[]}`))
	}))
	defer srv.Close()

	c := newTestBingClient(t, srv)
	if _, err := c.UpdateCampaigns(t.Context(), "123456", []any{map[string]any{"Id": "1", "DailyBudget": 30.0}}); err != nil {
		t.Fatalf("UpdateCampaigns: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT", method)
	}
	if path != "/CampaignManagement/v13/Campaigns" {
		t.Errorf("path = %q", path)
	}
	if body["AccountId"] != "123456" {
		t.Errorf("AccountId = %v, the account has to travel in the body as well as the header", body["AccountId"])
	}
}
