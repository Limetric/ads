package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleApplyMutationPartialFailureReportsAppliedOperations(t *testing.T) {
	const campaign = "customers/1/campaigns/5"
	const criterion = "customers/1/campaignCriteria/5~9"
	for _, tc := range []struct {
		name        string
		response    string
		wantError   bool
		want        []string
		doNotWant   []string
		resultCount int
	}{
		{
			name:      "mixed batch reports only successful slots",
			response:  `{"partialFailureError":{"code":3,"message":"EU declaration missing"},"mutateOperationResponses":[{}, {"campaignResult":{"resourceName":"` + campaign + `"}}, null, {"campaignCriterionResult":{"resourceName":"` + criterion + `"}}]}`,
			wantError: true,
			want:      []string{"code 3", "EU declaration missing", "zero-based indices", "operation[1]: " + campaign, "operation[3]: " + criterion, "re-read", "before retrying"},
			doNotWant: []string{"operation[0]", "operation[2]"},
		},
		{
			name:      "no results does not imply success",
			response:  `{"partialFailureError":{"code":3,"message":"EU declaration missing"}}`,
			wantError: true,
			want:      []string{"EU declaration missing", "re-read", "changes may have applied"},
			doNotWant: []string{"confirmed applied", "operation["},
		},
		{
			name:      "empty or unknown results do not imply success",
			response:  `{"partialFailureError":{"code":3,"message":"EU declaration missing"},"mutateOperationResponses":[{},null,{"campaignResult":{}},{"campaignResult":{"resourceName":""}},{"unexpected":{"resourceName":"not-a-success"}}]}`,
			wantError: true,
			want:      []string{"re-read"},
			doNotWant: []string{"confirmed applied", "operation[", "not-a-success"},
		},
		{
			name:      "legacy results preserved",
			response:  `{"partialFailureError":{"code":3,"message":"failure"},"results":[{}, {"resourceName":"` + campaign + `"}]}`,
			wantError: true,
			want:      []string{"operation[1]: " + campaign, "re-read"},
			doNotWant: []string{"operation[0]"},
		},
		{
			name:        "successful batch still returns results",
			response:    `{"mutateOperationResponses":[{"campaignResult":{"resourceName":"` + campaign + `"}}]}`,
			resultCount: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/customers/1/googleAds:mutate" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()
			outcome, err := newTestClient(t, srv).applyMutation(t.Context(), &PendingMutation{
				CustomerID: "1",
				Operations: []any{map[string]any{"campaignOperation": map[string]any{"update": map[string]any{"resourceName": campaign}, "updateMask": "name"}}},
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %v", err, tc.wantError)
			}
			if !tc.wantError {
				if outcome == nil || len(outcome.Results) != tc.resultCount {
					t.Fatalf("outcome = %+v, want %d results", outcome, tc.resultCount)
				}
				return
			}
			if outcome != nil {
				t.Fatalf("partial failure returned success outcome: %+v", outcome)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
			for _, unwanted := range tc.doNotWant {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("error %q unexpectedly contains %q", err, unwanted)
				}
			}
		})
	}
}
