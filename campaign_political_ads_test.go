package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateCampaignPoliticalDeclaration(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"does-not-contain", "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING"},
		{"contains", "CONTAINS_EU_POLITICAL_ADVERTISING"},
		{"CONTAINS_EU_POLITICAL_ADVERTISING", "CONTAINS_EU_POLITICAL_ADVERTISING"},
		{"DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING", "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			useTempState(t)
			var calls int
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasSuffix(r.URL.Path, "googleAds:mutate") {
					t.Errorf("standalone declaration must not read campaign: %s", r.URL.Path)
				}
				_ = decodeJSONBody(r, &body)
				_, _ = w.Write([]byte(`{"mutateOperationResponses":[{"campaignResult":{"resourceName":"customers/1/campaigns/5"}}]}`))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			prev, err := runUpdateCampaign(t.Context(), c, UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", EUPoliticalAds: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || prev.Token == "" || !strings.Contains(prev.Preview, tc.want) {
				t.Fatalf("preview: %+v calls=%d", prev, calls)
			}
			if _, err := runUpdateCampaign(t.Context(), c, UpdateCampaignArgs{Confirm: prev.Token}); err != nil {
				t.Fatal(err)
			}
			update, mask := campaignUpdateOp(t, body)
			if mask != "containsEuPoliticalAdvertising" || update["containsEuPoliticalAdvertising"] != tc.want || len(update) != 2 {
				t.Fatalf("update=%v mask=%s", update, mask)
			}
		})
	}
}

func TestUpdateCampaignPoliticalPreflight(t *testing.T) {
	for _, tc := range []struct{ name, response, want string }{
		{"absent", `{"results":[{"campaign":{"id":"5"}}]}`, "per-campaign"},
		{"unspecified", `{"results":[{"campaign":{"containsEuPoliticalAdvertising":"UNSPECIFIED"}}]}`, "per-campaign"},
		{"unknown", `{"results":[{"campaign":{"containsEuPoliticalAdvertising":"UNKNOWN"}}]}`, "per-campaign"},
		{"missing campaign", `{"results":[]}`, "not found"},
		{"bad field", `{"results":[{"campaign":{"containsEuPoliticalAdvertising":123}}]}`, "decode"},
		{"nonpolitical", `{"results":[{"campaign":{"containsEuPoliticalAdvertising":"DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING"}}]}`, ""},
		{"political", `{"results":[{"campaign":{"containsEuPoliticalAdvertising":"CONTAINS_EU_POLITICAL_ADVERTISING"}}]}`, ""},
		{"read failure", `{"error":{"code":403,"message":"access denied"}}`, "access denied"},
	} {
		for _, kind := range []string{"target", "exclude", "options", "combined declaration"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				useTempState(t)
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if !strings.HasSuffix(r.URL.Path, "googleAds:search") {
						t.Errorf("preview made mutation: %s", r.URL.Path)
					}
					var body struct {
						Query string `json:"query"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if strings.Contains(body.Query, "FROM geo_target_constant") {
						if tc.want != "" || calls != 2 {
							t.Errorf("name lookup before successful preflight")
						}
						_, _ = w.Write([]byte(`{"results":[]}`))
						return
					}
					if !strings.Contains(body.Query, "campaign.contains_eu_political_advertising") || !strings.Contains(body.Query, "campaign.id = 5") {
						t.Errorf("query = %s", body.Query)
					}
					if tc.name == "read failure" {
						w.WriteHeader(http.StatusForbidden)
					}
					_, _ = w.Write([]byte(tc.response))
				}))
				defer srv.Close()
				args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5"}
				switch kind {
				case "target":
					args.GeoTargetIDs = []string{"2840"}
				case "exclude":
					args.ExcludeGeoTargetIDs = []string{"2840"}
				case "options":
					args.PositiveGeoTargetType = "PRESENCE"
				case "combined declaration":
					args.EUPoliticalAds = "does-not-contain"
					args.GeoTargetIDs = []string{"2840"}
				}
				prev, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), args)
				wantCalls := 1
				if tc.want == "" && kind != "options" {
					wantCalls = 2
				}
				if calls != wantCalls {
					t.Errorf("reads=%d, want %d", calls, wantCalls)
				}
				if tc.want == "" {
					if err != nil || prev.Token == "" {
						t.Fatalf("preview=%+v err=%v", prev, err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) || prev.Token != "" {
					t.Fatalf("preview=%+v err=%v", prev, err)
				}
				if tc.want == "per-campaign" && (!strings.Contains(err.Error(), "--eu-political-ads") || !strings.Contains(err.Error(), "ads confirm")) {
					t.Fatalf("missing repair: %v", err)
				}
			})
		}
	}
}

func TestUpdateCampaignRejectsInvalidPoliticalDeclaration(t *testing.T) {
	for _, value := range []string{"UNSPECIFIED", "UNKNOWN", "false", "typo"} {
		t.Run(value, func(t *testing.T) {
			useTempState(t)
			_, err := runUpdateCampaign(t.Context(), nil, UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", EUPoliticalAds: value})
			if err == nil || !strings.Contains(err.Error(), "eu_political_ads") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
