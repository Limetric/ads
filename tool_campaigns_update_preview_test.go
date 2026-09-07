package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Multiple changes can share one operation. The preview must describe every
// requested change, including additions that leave existing targets in place.
func TestUpdateCampaign_PreviewDisclosesMixedChanges(t *testing.T) {
	for _, headline := range []string{"standard", "clear", "portfolio"} {
		t.Run(headline, func(t *testing.T) {
			useTempState(t)
			reads := map[string]int{}
			var mutateBody map[string]any
			mutations := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "googleAds:mutate") {
					mutations++
					_ = decodeJSONBody(r, &mutateBody)
					_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
					return
				}
				var body struct {
					Query string `json:"query"`
				}
				_ = decodeJSONBody(r, &body)
				switch {
				case strings.Contains(body.Query, "FROM geo_target_constant"):
					reads["geo"]++
					_, _ = w.Write([]byte(`{"results":[{"geoTargetConstant":{"id":"20835","name":"Metro Manila","canonicalName":"Metro Manila,Philippines"}},{"geoTargetConstant":{"id":"20822","name":"Bulacan","canonicalName":"Bulacan,Philippines"}},{"geoTargetConstant":{"id":"2840","name":"United States","canonicalName":"United States"}}]}`))
				case strings.Contains(body.Query, "FROM language_constant"):
					reads["language"]++
					_, _ = w.Write([]byte(`{"results":[{"languageConstant":{"id":"1000","name":"English"}},{"languageConstant":{"id":"1004","name":"Chinese"}}]}`))
				case strings.Contains(body.Query, "FROM accessible_bidding_strategy"):
					_, _ = w.Write([]byte(`{"results":[{"accessibleBiddingStrategy":{"id":"9","name":"Pooled tCPA","type":"TARGET_CPA","ownerCustomerId":"1"}}]}`))
				case strings.Contains(body.Query, "campaign.campaign_budget"):
					_, _ = w.Write([]byte(`{"results":[{"campaign":{"campaignBudget":"customers/1/campaignBudgets/777"},"campaignBudget":{"amountMicros":"20000000"}}]}`))
				default:
					_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"MAXIMIZE_CONVERSIONS"}}]}`))
				}
			}))
			defer srv.Close()
			args := UpdateCampaignArgs{
				CustomerID: "1", CampaignID: "5", DailyBudget: 25,
				GeoTargetIDs: []string{"20835", "20822"}, ExcludeGeoTargetIDs: []string{"2840"},
				LanguageIDs: []string{"1000", "1004"}, PositiveGeoTargetType: "PRESENCE", NegativeGeoTargetType: "PRESENCE_OR_INTEREST",
			}
			want := []string{"campaign 5", "daily budget 20 → 25 currency units", "customers/1/campaignBudgets/777", "add location target", "Metro Manila,Philippines", "20835", "Bulacan,Philippines", "20822", "add location exclusion", "United States", "2840", "add language", "English", "1000", "Chinese", "1004", "set positive location option to PRESENCE", "set negative location option to PRESENCE_OR_INTEREST", "existing targets retained"}
			switch headline {
			case "standard":
				args.BiddingStrategy, args.TargetCPA = "TARGET_CPA", 12.5
				want = append(want, "TARGET_CPA", "12.5")
			case "clear":
				args.ClearTargetCPA = true
				want = append(want, "Remove the target CPA", "MAXIMIZE_CONVERSIONS")
			case "portfolio":
				args.PortfolioStrategyID = "9"
				want = append(want, "Pooled tCPA", "TARGET_CPA", "ID 9")
			}
			c := newTestClient(t, srv)
			preview, err := runUpdateCampaign(t.Context(), c, args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			t.Log(strings.ReplaceAll(preview.Preview, preview.Token, "<token>"))
			for _, fragment := range want {
				if !strings.Contains(preview.Preview, fragment) {
					t.Errorf("preview omits %q: %s", fragment, preview.Preview)
				}
			}
			if mutations != 0 || preview.Token == "" || preview.Applied {
				t.Fatalf("preview changed the account or did not stage a token: %+v", preview)
			}
			if reads["geo"] != 1 || reads["language"] != 1 {
				t.Errorf("constant lookups should batch IDs: %v", reads)
			}
			args.Confirm = preview.Token
			if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if mutations != 1 || reads["geo"] != 1 || reads["language"] != 1 {
				t.Errorf("confirm should apply staged operations without resolving names again: mutations=%d reads=%v", mutations, reads)
			}
			ops, _ := mutateBody["mutateOperations"].([]any)
			if len(ops) != 7 {
				t.Fatalf("got %d operations, want budget + campaign + 5 additions", len(ops))
			}
			if targeted, excluded := countLocationCriteria(t, ops); targeted != 2 || excluded != 1 {
				t.Errorf("targeted=%d excluded=%d, want 2 and 1", targeted, excluded)
			}
		})
	}
}

func TestUpdateCampaign_PreviewConstantNamesUnavailable(t *testing.T) {
	for _, failure := range []string{"missing rows", "read failure"} {
		t.Run(failure, func(t *testing.T) {
			useTempState(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if failure == "read failure" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"code":403,"message":"lookup unavailable"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"results":[]}`))
			}))
			defer srv.Close()
			preview, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", GeoTargetIDs: []string{"20835"}, ExcludeGeoTargetIDs: []string{"2840"}, LanguageIDs: []string{"1000"}})
			if err != nil {
				t.Fatalf("name lookup should fall back to IDs: %v", err)
			}
			for _, fragment := range []string{"add location target 20835 (name unavailable)", "add location exclusion 2840 (name unavailable)", "add language target 1000 (name unavailable)"} {
				if !strings.Contains(preview.Preview, fragment) {
					t.Errorf("fallback preview omits %q: %s", fragment, preview.Preview)
				}
			}
		})
	}
}

func TestUpdateCampaign_PreviewNamesBiddingTarget(t *testing.T) {
	cases := []struct {
		name, strategy string
		cpa, roas      float64
		explicit       bool
		want           []string
	}{
		{"explicit CPA", "TARGET_CPA", 12.5, 0, true, []string{"TARGET_CPA", "target CPA", "12.5"}},
		{"explicit ROAS", "TARGET_ROAS", 0, 3.75, true, []string{"TARGET_ROAS", "target ROAS", "3.75"}},
		{"standalone CPA", "MAXIMIZE_CONVERSIONS", 12.5, 0, false, []string{"target CPA", "12.5"}},
		{"standalone ROAS", "MAXIMIZE_CONVERSION_VALUE", 0, 3.75, false, []string{"target ROAS", "3.75"}},
		{"strategy only", "MANUAL_CPC", 0, 0, true, []string{"MANUAL_CPC"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				strategy := tc.strategy
				if tc.name == "strategy only" {
					strategy = "MAXIMIZE_CONVERSIONS"
				}
				_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"` + strategy + `"}}]}`))
			}))
			defer srv.Close()
			args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", TargetCPA: tc.cpa, TargetROAS: tc.roas}
			if tc.explicit {
				args.BiddingStrategy = tc.strategy
			}
			preview, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			for _, fragment := range tc.want {
				if !strings.Contains(preview.Preview, fragment) {
					t.Errorf("preview omits %q: %s", fragment, preview.Preview)
				}
			}
		})
	}
}

func TestUpdateCampaign_PreviewDoesNotInventCurrentValuesOrChanges(t *testing.T) {
	cases := []struct {
		name         string
		args         UpdateCampaignArgs
		want, absent string
	}{
		{"unknown budget", UpdateCampaignArgs{DailyBudget: 25}, "daily budget unknown → 25 currency units", "daily budget 0"},
		{"redundant strategy with geo addition", UpdateCampaignArgs{BiddingStrategy: "MAXIMIZE_CONVERSIONS", GeoTargetIDs: []string{"20835"}}, "add location target", "set standard bidding strategy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[{"campaign":{"campaignBudget":"customers/1/campaignBudgets/777","biddingStrategyType":"MAXIMIZE_CONVERSIONS"}}]}`))
			}))
			defer srv.Close()
			args := tc.args
			args.CustomerID, args.CampaignID = "1", "5"
			preview, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if !strings.Contains(preview.Preview, tc.want) || strings.Contains(preview.Preview, tc.absent) {
				t.Errorf("preview must contain %q and omit %q: %s", tc.want, tc.absent, preview.Preview)
			}
		})
	}
}
