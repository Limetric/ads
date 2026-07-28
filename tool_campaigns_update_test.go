package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateCampaign_BudgetResolvesResource(t *testing.T) {
	useTempState(t)
	var mutateBody map[string]any
	var mutateCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			// Budget-resource resolution query.
			_, _ = w.Write([]byte(`{"results":[{"campaign":{"campaignBudget":"customers/1/campaignBudgets/777"}}]}`))
		case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
			mutateCalls++
			_ = decodeJSONBody(r, &mutateBody)
			_, _ = w.Write([]byte(`{"results":[{}]}`))
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "555", DailyBudget: 25}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if mutateCalls != 0 {
		t.Fatal("preview must not call mutate")
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ops, _ := mutateBody["mutateOperations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %v", mutateBody)
	}
	op, _ := ops[0].(map[string]any)
	bud, _ := op["campaignBudgetOperation"].(map[string]any)
	upd, _ := bud["update"].(map[string]any)
	if upd["resourceName"] != "customers/1/campaignBudgets/777" || upd["amountMicros"] != "25000000" {
		t.Errorf("budget update wrong: %v", upd)
	}
}

func TestUpdateCampaign_BiddingAndTargeting(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", BiddingStrategy: "TARGET_CPA", TargetCPA: 10, GeoTargetIDs: []string{"2840"}, LanguageIDs: []string{"1000"}}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// campaign update + geo + language = 3
	if len(cap.lastOps()) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(cap.lastOps()))
	}
	op, _ := cap.firstOp(t)["campaignOperation"].(map[string]any)
	if op["updateMask"] != "targetCpa.targetCpaMicros" {
		t.Errorf("updateMask = %v", op["updateMask"])
	}
}

func TestApplyBiddingStrategyUpdate(t *testing.T) {
	cases := []struct {
		name      string
		strategy  string
		cpa, roas float64
		wantKey   string
		wantMask  string
		wantSub   map[string]any
	}{
		{"maximize conversions with cpa", "MAXIMIZE_CONVERSIONS", 5, 0, "maximizeConversions", "maximizeConversions.targetCpaMicros", map[string]any{"targetCpaMicros": "5000000"}},
		{"maximize conversions without cpa", "MAXIMIZE_CONVERSIONS", 0, 0, "maximizeConversions", "maximizeConversions.targetCpaMicros", map[string]any{}},
		{"maximize conversion value with roas", "MAXIMIZE_CONVERSION_VALUE", 0, 3.5, "maximizeConversionValue", "maximizeConversionValue.targetRoas", map[string]any{"targetRoas": 3.5}},
		{"maximize conversion value without roas", "MAXIMIZE_CONVERSION_VALUE", 0, 0, "maximizeConversionValue", "maximizeConversionValue.targetRoas,maximizeConversionValue.targetRoasTolerancePercentMillis", map[string]any{}},
		{"target cpa", "TARGET_CPA", 10, 0, "targetCpa", "targetCpa.targetCpaMicros", map[string]any{"targetCpaMicros": "10000000"}},
		{"target roas", "TARGET_ROAS", 0, 2, "targetRoas", "targetRoas.targetRoas", map[string]any{"targetRoas": 2.0}},
		{"manual cpc", "MANUAL_CPC", 0, 0, "manualCpc", "manualCpc.enhancedCpcEnabled", map[string]any{}},
		{"target spend", "TARGET_SPEND", 0, 0, "targetSpend", "targetSpend.cpcBidCeilingMicros,targetSpend.targetSpendMicros", map[string]any{}},
		{"maximize clicks maps to target spend", "MAXIMIZE_CLICKS", 0, 0, "targetSpend", "targetSpend.cpcBidCeilingMicros,targetSpend.targetSpendMicros", map[string]any{}},
		{"percent cpc", "PERCENT_CPC", 0, 0, "percentCpc", "percentCpc.cpcBidCeilingMicros,percentCpc.enhancedCpcEnabled", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			campaign := map[string]any{}
			var mask []string
			if err := applyBiddingStrategyUpdate(campaign, &mask, tc.strategy, tc.cpa, tc.roas); err != nil {
				t.Fatalf("applyBiddingStrategyUpdate: %v", err)
			}
			if got := strings.Join(mask, ","); got != tc.wantMask {
				t.Fatalf("mask = %q, want %q", got, tc.wantMask)
			}
			sub, _ := campaign[tc.wantKey].(map[string]any)
			if sub == nil {
				t.Fatalf("campaign[%s] missing: %v", tc.wantKey, campaign)
			}
			if len(sub) != len(tc.wantSub) {
				t.Fatalf("campaign[%s] = %v, want %v", tc.wantKey, sub, tc.wantSub)
			}
			for k, want := range tc.wantSub {
				if sub[k] != want {
					t.Errorf("campaign[%s][%s] = %v, want %v", tc.wantKey, k, sub[k], want)
				}
			}
		})
	}

	t.Run("target cpa/roas without a value error at preview", func(t *testing.T) {
		// Zero targets used to stage an op with an empty updateMask that the
		// API rejected only at confirm time (issue #8).
		for _, strategy := range []string{"TARGET_CPA", "TARGET_ROAS"} {
			campaign := map[string]any{}
			var mask []string
			if err := applyBiddingStrategyUpdate(campaign, &mask, strategy, 0, 0); err == nil {
				t.Errorf("%s with zero value should error", strategy)
			}
		}
	})

	t.Run("target impression share errors with guidance", func(t *testing.T) {
		// An empty targetImpressionShare object is rejected by v23 at confirm
		// time (it requires location/fraction parameters).
		campaign := map[string]any{}
		var mask []string
		if err := applyBiddingStrategyUpdate(campaign, &mask, "TARGET_IMPRESSION_SHARE", 0, 0); err == nil || !strings.Contains(err.Error(), "create_portfolio_bidding_strategy") {
			t.Fatalf("expected guidance error, got %v", err)
		}
	})

	t.Run("unknown strategy errors instead of writing output-only field", func(t *testing.T) {
		// The old fallback wrote biddingStrategyType, which is OUTPUT_ONLY in
		// v23 — Google rejected the confirmed mutate every time (issue #8).
		campaign := map[string]any{}
		var mask []string
		if err := applyBiddingStrategyUpdate(campaign, &mask, "SUPER_BIDDING", 0, 0); err == nil {
			t.Fatal("unknown strategy should error at preview")
		}
		if _, ok := campaign["biddingStrategyType"]; ok {
			t.Fatal("output-only biddingStrategyType must never be written")
		}
	})
}

func TestUpdateCampaign_StandaloneTargetUsesCurrentStrategy(t *testing.T) {
	cases := []struct {
		name         string
		strategy     string
		targetCPA    float64
		targetROAS   float64
		wantKey      string
		wantSubKey   string
		wantSubValue any
		wantMask     string
	}{
		{"target cpa", "TARGET_CPA", 0.25, 0, "targetCpa", "targetCpaMicros", "250000", "targetCpa.targetCpaMicros"},
		{"maximize conversions cpa", "MAXIMIZE_CONVERSIONS", 0.25, 0, "maximizeConversions", "targetCpaMicros", "250000", "maximizeConversions.targetCpaMicros"},
		{"target roas", "TARGET_ROAS", 0, 3.5, "targetRoas", "targetRoas", 3.5, "targetRoas.targetRoas"},
		{"maximize conversion value roas", "MAXIMIZE_CONVERSION_VALUE", 0, 3.5, "maximizeConversionValue", "targetRoas", 3.5, "maximizeConversionValue.targetRoas"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var searchQuery string
			var searchCalls int
			var mutateBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "googleAds:search"):
					searchCalls++
					var body map[string]any
					_ = decodeJSONBody(r, &body)
					searchQuery, _ = body["query"].(string)
					_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"` + tc.strategy + `"}}]}`))
				case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
					_ = decodeJSONBody(r, &mutateBody)
					_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
				}
			}))
			defer srv.Close()
			c := newTestClient(t, srv)

			args := UpdateCampaignArgs{
				CustomerID: "1",
				CampaignID: "5",
				TargetCPA:  tc.targetCPA,
				TargetROAS: tc.targetROAS,
			}
			preview, err := runUpdateCampaign(t.Context(), c, args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if !strings.Contains(searchQuery, "campaign.bidding_strategy_type, campaign.bidding_strategy") ||
				!strings.Contains(searchQuery, "campaign.id = 5") {
				t.Fatalf("unexpected current-strategy query: %q", searchQuery)
			}
			if searchCalls != 1 {
				t.Fatalf("search calls after preview = %d, want 1", searchCalls)
			}
			args.Confirm = preview.Token
			if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if searchCalls != 1 {
				t.Fatalf("confirm repeated current-strategy lookup: search calls = %d", searchCalls)
			}

			ops, _ := mutateBody["mutateOperations"].([]any)
			if len(ops) != 1 {
				t.Fatalf("mutate operations = %v, want one", mutateBody["mutateOperations"])
			}
			outer, _ := ops[0].(map[string]any)
			op, _ := outer["campaignOperation"].(map[string]any)
			if op["updateMask"] != tc.wantMask {
				t.Errorf("updateMask = %v, want %s", op["updateMask"], tc.wantMask)
			}
			update, _ := op["update"].(map[string]any)
			sub, _ := update[tc.wantKey].(map[string]any)
			if sub[tc.wantSubKey] != tc.wantSubValue {
				t.Errorf("update[%s][%s] = %v, want %v", tc.wantKey, tc.wantSubKey, sub[tc.wantSubKey], tc.wantSubValue)
			}
		})
	}
}

func TestUpdateCampaign_StandaloneTargetRequiresCompatibleCurrentStrategy(t *testing.T) {
	tests := []struct {
		name       string
		targetCPA  float64
		targetROAS float64
	}{
		{"cpa", 0.25, 0},
		{"roas", 0, 3.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"MANUAL_CPC"}}]}`))
			}))
			defer srv.Close()

			_, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), UpdateCampaignArgs{
				CustomerID: "1",
				CampaignID: "5",
				TargetCPA:  tc.targetCPA,
				TargetROAS: tc.targetROAS,
			})
			if err == nil || !strings.Contains(err.Error(), "MANUAL_CPC") || !strings.Contains(err.Error(), "bidding_strategy") {
				t.Fatalf("expected actionable incompatible-strategy error, got %v", err)
			}
		})
	}
}

func TestResolveCampaignBiddingStrategy(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		want       string
		wantErr    string
	}{
		{"success", http.StatusOK, `{"results":[{"campaign":{"biddingStrategyType":"TARGET_CPA"}}]}`, "TARGET_CPA", ""},
		{"no campaign", http.StatusOK, `{"results":[]}`, "", "could not resolve"},
		{"missing strategy type", http.StatusOK, `{"results":[{"campaign":{}}]}`, "", "no bidding strategy type"},
		{"malformed campaign", http.StatusOK, `{"results":[{"campaign":"bad"}]}`, "", "could not decode"},
		{"portfolio strategy", http.StatusOK, `{"results":[{"campaign":{"biddingStrategyType":"TARGET_CPA","biddingStrategy":"customers/1/biddingStrategies/7"}}]}`, "", "portfolio bidding strategy"},
		{"api error", http.StatusBadRequest, `{"error":{"code":400,"message":"bad query"}}`, "", "bad query"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			got, err := resolveCampaignBiddingStrategy(t.Context(), newTestClient(t, srv), "1", "5")
			if got != tc.want {
				t.Errorf("strategy = %q, want %q", got, tc.want)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestUpdateCampaign_StandaloneTargetLookupErrorsAreActionable(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		want       string
	}{
		{"no campaign", http.StatusOK, `{"results":[]}`, "could not resolve"},
		{"api error", http.StatusBadRequest, `{"error":{"code":400,"message":"bad query"}}`, "bad query"},
		{"portfolio strategy", http.StatusOK, `{"results":[{"campaign":{"biddingStrategyType":"TARGET_CPA","biddingStrategy":"customers/1/biddingStrategies/7"}}]}`, "portfolio bidding strategy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var mutateCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "googleAds:mutate") {
					mutateCalls++
				}
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			_, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), UpdateCampaignArgs{
				CustomerID: "1",
				CampaignID: "5",
				TargetCPA:  0.25,
			})
			if err == nil || !strings.Contains(err.Error(), "update_campaign") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want update_campaign context and %q", err, tc.want)
			}
			if mutateCalls != 0 {
				t.Fatalf("lookup failure made %d mutate calls", mutateCalls)
			}
		})
	}
}

func TestUpdateCampaign_RejectsInvalidTargetCombinationsBeforeLookup(t *testing.T) {
	tests := []struct {
		name string
		args UpdateCampaignArgs
		want string
	}{
		{
			"both targets",
			UpdateCampaignArgs{TargetCPA: 0.25, TargetROAS: 3.5},
			"cannot be updated together",
		},
		{
			"negative cpa",
			UpdateCampaignArgs{TargetCPA: -0.25},
			"target_cpa must be positive",
		},
		{
			"negative roas",
			UpdateCampaignArgs{TargetROAS: -3.5},
			"target_roas must be positive",
		},
		{
			"explicit incompatible target",
			UpdateCampaignArgs{BiddingStrategy: "MANUAL_CPC", TargetCPA: 0.25},
			"target_cpa cannot be set",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			tc.args.CustomerID = "1"
			tc.args.CampaignID = "5"
			_, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			if calls != 0 {
				t.Fatalf("invalid input made %d API calls", calls)
			}
		})
	}
}

func TestUpdateCampaign_ExplicitStrategyDoesNotLookupCurrentStrategy(t *testing.T) {
	useTempState(t)
	var searchCalls int
	var mutateCalls int
	var mutateBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			searchCalls++
			_, _ = w.Write([]byte(`{"results":[]}`))
		case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
			mutateCalls++
			_ = decodeJSONBody(r, &mutateBody)
			_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{
		CustomerID:      "1",
		CampaignID:      "5",
		BiddingStrategy: "MAXIMIZE_CONVERSION_VALUE",
	}
	preview, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = preview.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if searchCalls != 0 {
		t.Errorf("explicit strategy made %d search calls", searchCalls)
	}
	if mutateCalls != 1 {
		t.Errorf("mutate calls = %d, want 1", mutateCalls)
	}
	ops, _ := mutateBody["mutateOperations"].([]any)
	outer, _ := ops[0].(map[string]any)
	op, _ := outer["campaignOperation"].(map[string]any)
	wantMask := "maximizeConversionValue.targetRoas,maximizeConversionValue.targetRoasTolerancePercentMillis"
	if op["updateMask"] != wantMask {
		t.Errorf("updateMask = %v, want %s", op["updateMask"], wantMask)
	}
	update, _ := op["update"].(map[string]any)
	message, _ := update["maximizeConversionValue"].(map[string]any)
	if message == nil || len(message) != 0 {
		t.Errorf("update maximizeConversionValue = %v, want empty message", update["maximizeConversionValue"])
	}
}

func TestUpdateCampaign_RejectsNonNumericCampaignID(t *testing.T) {
	useTempState(t)
	// campaign_id is interpolated into GAQL when resolving the budget
	// resource; a crafted ID must be rejected before any query (issue #8).
	_, err := runUpdateCampaign(t.Context(), nil, UpdateCampaignArgs{
		CustomerID: "1", CampaignID: "1 OR campaign.id > 0", DailyBudget: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Fatalf("expected numeric-ID rejection, got %v", err)
	}
}

func TestUpdateCampaign_HonorsBlockedOps(t *testing.T) {
	useTempState(t)
	t.Setenv("GOOGLE_ADS_BLOCKED_OPS", "update_campaign")
	if _, err := runUpdateCampaign(t.Context(), nil, UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", DailyBudget: 5}); err == nil {
		t.Fatal("blocked operation should be rejected")
	}
}

func TestUpdateCampaign_NoChanges(t *testing.T) {
	useTempState(t)
	srv, _ := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)
	if _, err := runUpdateCampaign(t.Context(), c, UpdateCampaignArgs{CustomerID: "1", CampaignID: "5"}); err == nil {
		t.Fatal("expected error when no changes specified")
	}
}

func TestUpdateCampaign_BudgetCap(t *testing.T) {
	useTempState(t)
	srv, _ := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)
	if _, err := runUpdateCampaign(t.Context(), c, UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", DailyBudget: 100}); err == nil {
		t.Fatal("expected budget cap rejection")
	}
}
