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

func TestUpdateCampaign_ExplicitStrategySwitchUsesLeafMasks(t *testing.T) {
	useTempState(t)
	var searchCalls int
	var mutateCalls int
	var mutateBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			searchCalls++
			_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"MANUAL_CPC"}}]}`))
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
	if searchCalls != 1 {
		t.Errorf("current-strategy search calls = %d, want 1", searchCalls)
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

func TestUpdateCampaign_RedundantStrategyOnlyPreservesExistingSettings(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		current   string
	}{
		{"maximize conversions", "MAXIMIZE_CONVERSIONS", "MAXIMIZE_CONVERSIONS"},
		{"maximize conversion value", "MAXIMIZE_CONVERSION_VALUE", "MAXIMIZE_CONVERSION_VALUE"},
		{"manual cpc", "MANUAL_CPC", "MANUAL_CPC"},
		{"target spend", "TARGET_SPEND", "TARGET_SPEND"},
		{"maximize clicks alias", "MAXIMIZE_CLICKS", "TARGET_SPEND"},
		{"percent cpc", "PERCENT_CPC", "PERCENT_CPC"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var searchCalls int
			var mutateBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "googleAds:search"):
					searchCalls++
					_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"` + tc.current + `"}}]}`))
				case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
					_ = decodeJSONBody(r, &mutateBody)
					_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
				}
			}))
			defer srv.Close()
			c := newTestClient(t, srv)

			args := UpdateCampaignArgs{
				CustomerID:      "1",
				CampaignID:      "5",
				BiddingStrategy: tc.requested,
				GeoTargetIDs:    []string{"2840"},
			}
			preview, err := runUpdateCampaign(t.Context(), c, args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			args.Confirm = preview.Token
			if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if searchCalls != 1 {
				t.Errorf("current-strategy search calls = %d, want 1", searchCalls)
			}
			ops, _ := mutateBody["mutateOperations"].([]any)
			if len(ops) != 1 {
				t.Fatalf("mutate operations = %v, want only the geo criterion", mutateBody["mutateOperations"])
			}
			op, _ := ops[0].(map[string]any)
			if _, ok := op["campaignOperation"]; ok {
				t.Fatalf("redundant strategy staged a campaign update: %v", op)
			}
			if _, ok := op["campaignCriterionOperation"]; !ok {
				t.Fatalf("expected geo criterion operation, got %v", op)
			}
		})
	}
}

func TestUpdateCampaign_PortfolioStrategyStillSwitchesToStandard(t *testing.T) {
	useTempState(t)
	var mutateBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"MAXIMIZE_CONVERSION_VALUE","biddingStrategy":"customers/1/biddingStrategies/7"}}]}`))
		case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
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
	ops, _ := mutateBody["mutateOperations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("mutate operations = %v, want one strategy switch", mutateBody["mutateOperations"])
	}
	outer, _ := ops[0].(map[string]any)
	op, _ := outer["campaignOperation"].(map[string]any)
	wantMask := "maximizeConversionValue.targetRoas,maximizeConversionValue.targetRoasTolerancePercentMillis"
	if op["updateMask"] != wantMask {
		t.Errorf("updateMask = %v, want %s", op["updateMask"], wantMask)
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

// campaignUpdateOp digs the single campaignOperation out of a captured mutate
// body and returns its update map and field mask.
func campaignUpdateOp(t *testing.T, body map[string]any) (map[string]any, string) {
	t.Helper()
	ops, _ := body["mutateOperations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d: %v", len(ops), body)
	}
	outer, _ := ops[0].(map[string]any)
	op, ok := outer["campaignOperation"].(map[string]any)
	if !ok {
		t.Fatalf("operation is not a campaignOperation: %v", outer)
	}
	update, _ := op["update"].(map[string]any)
	mask, _ := op["updateMask"].(string)
	return update, mask
}

// TestUpdateCampaign_GeoTargetTypeMasksOnlySuppliedSides covers the campaign
// "Location options" setting: each side is masked by its own leaf, so setting
// one never clears the other, and geoTargetTypeSetting itself never appears
// bare in the mask (the API rejects a message with defined sub-fields there).
func TestUpdateCampaign_GeoTargetTypeMasksOnlySuppliedSides(t *testing.T) {
	tests := []struct {
		name        string
		positive    string
		negative    string
		wantSetting map[string]any
		wantMask    string
	}{
		{
			name:        "positive only",
			positive:    "PRESENCE",
			wantSetting: map[string]any{"positiveGeoTargetType": "PRESENCE"},
			wantMask:    "geoTargetTypeSetting.positiveGeoTargetType",
		},
		{
			name:        "negative only",
			negative:    "PRESENCE",
			wantSetting: map[string]any{"negativeGeoTargetType": "PRESENCE"},
			wantMask:    "geoTargetTypeSetting.negativeGeoTargetType",
		},
		{
			name:        "both sides",
			positive:    "PRESENCE",
			negative:    "PRESENCE_OR_INTEREST",
			wantSetting: map[string]any{"positiveGeoTargetType": "PRESENCE", "negativeGeoTargetType": "PRESENCE_OR_INTEREST"},
			wantMask:    "geoTargetTypeSetting.positiveGeoTargetType,geoTargetTypeSetting.negativeGeoTargetType",
		},
		{
			name:        "case insensitive",
			positive:    " presence_or_interest ",
			wantSetting: map[string]any{"positiveGeoTargetType": "PRESENCE_OR_INTEREST"},
			wantMask:    "geoTargetTypeSetting.positiveGeoTargetType",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var searchCalls int
			var mutateBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "googleAds:search"):
					searchCalls++
					_, _ = w.Write([]byte(`{"results":[]}`))
				case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
					_ = decodeJSONBody(r, &mutateBody)
					_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
				}
			}))
			defer srv.Close()
			c := newTestClient(t, srv)

			args := UpdateCampaignArgs{
				CustomerID:            "1",
				CampaignID:            "5",
				PositiveGeoTargetType: tc.positive,
				NegativeGeoTargetType: tc.negative,
			}
			preview, err := runUpdateCampaign(t.Context(), c, args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			args.Confirm = preview.Token
			if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
				t.Fatalf("confirm: %v", err)
			}
			// A location-options-only update needs no bidding or budget lookup.
			if searchCalls != 0 {
				t.Errorf("search calls = %d, want 0", searchCalls)
			}
			update, mask := campaignUpdateOp(t, mutateBody)
			if update["resourceName"] != "customers/1/campaigns/5" {
				t.Errorf("resourceName = %v", update["resourceName"])
			}
			setting, _ := update["geoTargetTypeSetting"].(map[string]any)
			if len(setting) != len(tc.wantSetting) {
				t.Fatalf("geoTargetTypeSetting = %v, want %v", update["geoTargetTypeSetting"], tc.wantSetting)
			}
			for k, want := range tc.wantSetting {
				if setting[k] != want {
					t.Errorf("geoTargetTypeSetting[%s] = %v, want %v", k, setting[k], want)
				}
			}
			if mask != tc.wantMask {
				t.Errorf("updateMask = %q, want %q", mask, tc.wantMask)
			}
		})
	}
}

// TestUpdateCampaign_GeoTargetTypeSharesBiddingOperation guards against staging
// two updates of the same campaign resource in one batch: bidding and location
// options belong to the same campaign, so they travel as one operation.
func TestUpdateCampaign_GeoTargetTypeSharesBiddingOperation(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{
		CustomerID:            "1",
		CampaignID:            "5",
		BiddingStrategy:       "TARGET_CPA",
		TargetCPA:             10,
		PositiveGeoTargetType: "PRESENCE",
	}
	preview, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = preview.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	update, mask := campaignUpdateOp(t, cap.lastBody)
	targetCPA, _ := update["targetCpa"].(map[string]any)
	if targetCPA == nil || targetCPA["targetCpaMicros"] != "10000000" {
		t.Errorf("targetCpa = %v", update["targetCpa"])
	}
	setting, _ := update["geoTargetTypeSetting"].(map[string]any)
	if setting == nil || setting["positiveGeoTargetType"] != "PRESENCE" {
		t.Errorf("geoTargetTypeSetting = %v", update["geoTargetTypeSetting"])
	}
	wantMask := "targetCpa.targetCpaMicros,geoTargetTypeSetting.positiveGeoTargetType"
	if mask != wantMask {
		t.Errorf("updateMask = %q, want %q", mask, wantMask)
	}
}

// TestUpdateCampaign_RejectsInvalidGeoTargetTypeBeforeLookup keeps a bad
// location option from costing an API round trip or staging an operation
// Google would only reject at confirm time.
func TestUpdateCampaign_RejectsInvalidGeoTargetTypeBeforeLookup(t *testing.T) {
	tests := []struct {
		name     string
		args     UpdateCampaignArgs
		wantText string
	}{
		{
			name:     "unknown positive value",
			args:     UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PositiveGeoTargetType: "PRESENSE"},
			wantText: `unsupported positive_geo_target_type "PRESENSE"`,
		},
		{
			name:     "unknown negative value",
			args:     UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", NegativeGeoTargetType: "EVERYONE"},
			wantText: `unsupported negative_geo_target_type "EVERYONE"`,
		},
		{
			name:     "deprecated search interest",
			args:     UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PositiveGeoTargetType: "SEARCH_INTEREST"},
			wantText: "SEARCH_INTEREST is deprecated",
		},
		{
			// The bad value must be caught even when a budget change would
			// otherwise send the handler to the API first.
			name:     "rejected ahead of a budget lookup",
			args:     UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", DailyBudget: 10, PositiveGeoTargetType: "nope"},
			wantText: `unsupported positive_geo_target_type "nope"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var apiCalls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				apiCalls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"results":[]}`))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)

			_, err := runUpdateCampaign(t.Context(), c, tc.args)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantText)
			}
			if apiCalls != 0 {
				t.Errorf("API calls = %d, want 0", apiCalls)
			}
		})
	}
}

// portfolioStrategyServer answers the accessible_bidding_strategy lookup with
// one strategy owned by ownerID and captures the mutate body.
func portfolioStrategyServer(t *testing.T, ownerID string, mutateBody *map[string]any) *httptest.Server {
	t.Helper()
	var searchQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			var body struct {
				Query string `json:"query"`
			}
			_ = decodeJSONBody(r, &body)
			searchQueries = append(searchQueries, body.Query)
			if !strings.Contains(body.Query, "accessible_bidding_strategy") {
				t.Errorf("unexpected search query: %s", body.Query)
			}
			_, _ = w.Write([]byte(`{"results":[{"accessibleBiddingStrategy":{"id":"9","name":"Pooled tCPA","type":"TARGET_CPA","ownerCustomerId":"` + ownerID + `"}}]}`))
		case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
			_ = decodeJSONBody(r, mutateBody)
			_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
		}
	}))
	return srv
}

func TestUpdateCampaign_AttachesPortfolioStrategy(t *testing.T) {
	useTempState(t)
	var mutateBody map[string]any
	srv := portfolioStrategyServer(t, "1", &mutateBody)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "9"}
	preview, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// The preview names the strategy so the operator confirms against what the
	// campaign will actually bid with, not a bare ID.
	if !strings.Contains(preview.Preview, "Pooled tCPA") || !strings.Contains(preview.Preview, "TARGET_CPA") {
		t.Errorf("preview does not describe the strategy: %s", preview.Preview)
	}
	args.Confirm = preview.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	ops, _ := mutateBody["mutateOperations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("mutate operations = %v, want one campaign update", mutateBody["mutateOperations"])
	}
	outer, _ := ops[0].(map[string]any)
	op, _ := outer["campaignOperation"].(map[string]any)
	update, _ := op["update"].(map[string]any)
	if update["biddingStrategy"] != "customers/1/biddingStrategies/9" {
		t.Errorf("biddingStrategy = %v", update["biddingStrategy"])
	}
	if op["updateMask"] != "biddingStrategy" {
		t.Errorf("updateMask = %v, want biddingStrategy", op["updateMask"])
	}
}

func TestUpdateCampaign_PortfolioStrategyUsesOwnerCustomerID(t *testing.T) {
	useTempState(t)
	// A manager-owned strategy is attachable by the child account, but its
	// resource name carries the manager's customer ID.
	var mutateBody map[string]any
	srv := portfolioStrategyServer(t, "99", &mutateBody)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "9"}
	preview, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = preview.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	ops, _ := mutateBody["mutateOperations"].([]any)
	outer, _ := ops[0].(map[string]any)
	op, _ := outer["campaignOperation"].(map[string]any)
	update, _ := op["update"].(map[string]any)
	if update["biddingStrategy"] != "customers/99/biddingStrategies/9" {
		t.Errorf("biddingStrategy = %v, want the manager-owned resource name", update["biddingStrategy"])
	}
}

func TestUpdateCampaign_PortfolioStrategyAcceptsResourceName(t *testing.T) {
	useTempState(t)
	var mutateBody map[string]any
	srv := portfolioStrategyServer(t, "1", &mutateBody)
	defer srv.Close()
	c := newTestClient(t, srv)

	// create_portfolio_bidding_strategy returns resource names; passing one
	// straight back must work.
	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "customers/1/biddingStrategies/9"}
	preview, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = preview.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	ops, _ := mutateBody["mutateOperations"].([]any)
	outer, _ := ops[0].(map[string]any)
	op, _ := outer["campaignOperation"].(map[string]any)
	update, _ := op["update"].(map[string]any)
	if update["biddingStrategy"] != "customers/1/biddingStrategies/9" {
		t.Errorf("biddingStrategy = %v", update["biddingStrategy"])
	}
}

func TestUpdateCampaign_PortfolioStrategyUnknownIDFailsAtPreview(t *testing.T) {
	useTempState(t)
	var mutateCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "googleAds:mutate") {
			mutateCalls++
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := runUpdateCampaign(t.Context(), c, UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "9"})
	if err == nil {
		t.Fatal("expected an error for an inaccessible strategy")
	}
	if !strings.Contains(err.Error(), "create_portfolio_bidding_strategy") {
		t.Errorf("error is not actionable: %v", err)
	}
	if mutateCalls != 0 {
		t.Error("preview must not call mutate")
	}
}

func TestUpdateCampaign_PortfolioStrategyRejectsConflicts(t *testing.T) {
	useTempState(t)
	tests := []struct {
		name string
		args UpdateCampaignArgs
		want string
	}{
		{
			name: "with a standard strategy",
			args: UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "9", BiddingStrategy: "TARGET_CPA", TargetCPA: 10},
			want: "cannot be set together",
		},
		{
			name: "with a campaign-level target",
			args: UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "9", TargetCPA: 10},
			want: "belong to the shared strategy",
		},
		{
			name: "non-numeric ID",
			args: UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "9 OR 1=1"},
			want: "must be a plain numeric ID",
		},
		{
			name: "resource name of another entity",
			args: UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", PortfolioStrategyID: "customers/1/campaigns/9"},
			want: "not a bidding strategy resource name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// nil client: every one of these must fail before any API call.
			_, err := runUpdateCampaign(t.Context(), nil, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestUpdateCampaign_ClearTargetRemovesOptionalTarget covers the one shape an
// omitted target cannot express: dropping the optional target off a "maximize"
// strategy so the campaign bids without one. The strategy itself is preserved —
// only the target leaf is masked, with the message sent empty.
func TestUpdateCampaign_ClearTargetRemovesOptionalTarget(t *testing.T) {
	tests := []struct {
		name        string
		args        UpdateCampaignArgs
		current     string
		wantKey     string
		wantMask    string
		wantSummary string
	}{
		{
			name:        "target cpa from resolved strategy",
			args:        UpdateCampaignArgs{ClearTargetCPA: true},
			current:     "MAXIMIZE_CONVERSIONS",
			wantKey:     "maximizeConversions",
			wantMask:    "maximizeConversions.targetCpaMicros",
			wantSummary: "Remove the target CPA from campaign 5",
		},
		{
			name:        "target roas from resolved strategy",
			args:        UpdateCampaignArgs{ClearTargetROAS: true},
			current:     "MAXIMIZE_CONVERSION_VALUE",
			wantKey:     "maximizeConversionValue",
			wantMask:    "maximizeConversionValue.targetRoas",
			wantSummary: "Remove the target ROAS from campaign 5",
		},
		{
			// The strategy is already what the campaign uses, so the redundant
			// strategy-only branch would normally drop the operation entirely.
			name:        "target cpa with the strategy named explicitly",
			args:        UpdateCampaignArgs{BiddingStrategy: "MAXIMIZE_CONVERSIONS", ClearTargetCPA: true},
			current:     "MAXIMIZE_CONVERSIONS",
			wantKey:     "maximizeConversions",
			wantMask:    "maximizeConversions.targetCpaMicros",
			wantSummary: "Remove the target CPA from campaign 5",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var mutateBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "googleAds:search"):
					_, _ = w.Write([]byte(`{"results":[{"campaign":{"biddingStrategyType":"` + tc.current + `"}}]}`))
				case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
					_ = decodeJSONBody(r, &mutateBody)
					_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
				}
			}))
			defer srv.Close()
			c := newTestClient(t, srv)

			args := tc.args
			args.CustomerID = "1"
			args.CampaignID = "5"
			preview, err := runUpdateCampaign(t.Context(), c, args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			// The operator confirms against a summary that names the removal,
			// not a bare "1 operation(s)".
			if !strings.Contains(preview.Preview, tc.wantSummary) {
				t.Errorf("preview = %q, want it to mention %q", preview.Preview, tc.wantSummary)
			}
			args.Confirm = preview.Token
			if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
				t.Fatalf("confirm: %v", err)
			}
			update, mask := campaignUpdateOp(t, mutateBody)
			if mask != tc.wantMask {
				t.Errorf("updateMask = %q, want %q", mask, tc.wantMask)
			}
			// Only the target leaf may be masked. The strategy-selection mask
			// also covers target_roas_tolerance_percent_millis, a separate
			// campaign-level setting a clear must not blank.
			if strings.Contains(mask, "Tolerance") {
				t.Errorf("updateMask = %q, want it to leave the ROAS degradation tolerance alone", mask)
			}
			message, ok := update[tc.wantKey].(map[string]any)
			if !ok || len(message) != 0 {
				t.Errorf("update[%s] = %v, want an empty message", tc.wantKey, update[tc.wantKey])
			}
		})
	}
}

// TestUpdateCampaign_ClearTargetRequiresACompatibleStrategy keeps a clear off a
// strategy that has no optional target: TARGET_CPA and TARGET_ROAS require
// theirs, and a portfolio strategy holds its target on the shared resource.
func TestUpdateCampaign_ClearTargetRequiresACompatibleStrategy(t *testing.T) {
	tests := []struct {
		name     string
		args     UpdateCampaignArgs
		response string
		want     string
	}{
		{
			name:     "target cpa campaign",
			args:     UpdateCampaignArgs{ClearTargetCPA: true},
			response: `{"results":[{"campaign":{"biddingStrategyType":"TARGET_CPA"}}]}`,
			want:     "pass bidding_strategy MAXIMIZE_CONVERSIONS",
		},
		{
			name:     "manual cpc campaign",
			args:     UpdateCampaignArgs{ClearTargetROAS: true},
			response: `{"results":[{"campaign":{"biddingStrategyType":"MANUAL_CPC"}}]}`,
			want:     "pass bidding_strategy MAXIMIZE_CONVERSION_VALUE",
		},
		{
			name:     "portfolio campaign",
			args:     UpdateCampaignArgs{ClearTargetCPA: true},
			response: `{"results":[{"campaign":{"biddingStrategyType":"MAXIMIZE_CONVERSIONS","biddingStrategy":"customers/1/biddingStrategies/7"}}]}`,
			want:     "portfolio bidding strategy",
		},
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
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			args := tc.args
			args.CustomerID = "1"
			args.CampaignID = "5"
			_, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			if mutateCalls != 0 {
				t.Fatalf("incompatible clear made %d mutate calls", mutateCalls)
			}
		})
	}
}

func TestUpdateCampaign_RejectsInvalidClearCombinationsBeforeLookup(t *testing.T) {
	tests := []struct {
		name string
		args UpdateCampaignArgs
		want string
	}{
		{
			"both clears",
			UpdateCampaignArgs{ClearTargetCPA: true, ClearTargetROAS: true},
			"cannot be set together",
		},
		{
			"clear alongside the value it removes",
			UpdateCampaignArgs{ClearTargetCPA: true, TargetCPA: 25},
			"clear_target_cpa cannot be combined with target_cpa/target_roas",
		},
		{
			"clear alongside the other target",
			UpdateCampaignArgs{ClearTargetROAS: true, TargetCPA: 25},
			"clear_target_roas cannot be combined with target_cpa/target_roas",
		},
		{
			"clear with a portfolio strategy",
			UpdateCampaignArgs{ClearTargetCPA: true, PortfolioStrategyID: "9"},
			"cannot be set with portfolio_strategy_id",
		},
		{
			"clear with an incompatible explicit strategy",
			UpdateCampaignArgs{ClearTargetCPA: true, BiddingStrategy: "MAXIMIZE_CONVERSION_VALUE"},
			"clear_target_cpa applies only to MAXIMIZE_CONVERSIONS",
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

func TestUpdateCampaign_AddsExcludedLocations(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	// --negative-geo-target-type was fully plumbed while nothing could create
	// an exclusion for it to govern (issue #53); the two now travel together.
	args := UpdateCampaignArgs{
		CustomerID: "1", CampaignID: "5",
		GeoTargetIDs:          []string{"2840"},
		ExcludeGeoTargetIDs:   []string{"2826", "2372"},
		NegativeGeoTargetType: "PRESENCE",
	}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	targeted, excluded := countLocationCriteria(t, cap.lastOps())
	if targeted != 1 || excluded != 2 {
		t.Fatalf("targeted=%d excluded=%d, want 1 and 2", targeted, excluded)
	}
	// The location-option update still shares the single campaign operation.
	var campaignOps int
	for _, op := range cap.lastOps() {
		if outer, _ := op.(map[string]any); outer["campaignOperation"] != nil {
			campaignOps++
		}
	}
	if campaignOps != 1 {
		t.Errorf("campaign operations = %d, want 1", campaignOps)
	}
}

func TestUpdateCampaign_ExclusionsAloneAreAChange(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", ExcludeGeoTargetIDs: []string{"2826"}}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("an exclusion on its own is a change: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if targeted, excluded := countLocationCriteria(t, cap.lastOps()); targeted != 0 || excluded != 1 {
		t.Errorf("targeted=%d excluded=%d, want 0 and 1", targeted, excluded)
	}
}

func TestUpdateCampaign_RejectsATargetedAndExcludedLocation(t *testing.T) {
	useTempState(t)
	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", GeoTargetIDs: []string{"2840"}, ExcludeGeoTargetIDs: []string{"2840"}}
	// A nil client would panic on any request, so reaching the API fails here.
	if _, err := runUpdateCampaign(t.Context(), nil, args); err == nil {
		t.Fatal("targeting and excluding the same place must be rejected")
	}
}

func TestUpdateCampaign_RenamesAndSetsRunDates(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{
		CustomerID: "1", CampaignID: "5",
		Name: "Brand — EU", StartDate: "2026-09-01", EndDate: "2026-12-31",
	}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// The preview names what changes; "1 operation(s)" says nothing about a
	// rename or a finish line.
	if !strings.Contains(prev.Preview, "Brand — EU") || !strings.Contains(prev.Preview, "end 2026-12-31") {
		t.Errorf("preview does not describe the change: %s", prev.Preview)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op, _ := cap.firstOp(t)["campaignOperation"].(map[string]any)
	upd := opUpdate(t, cap.firstOp(t), "campaignOperation")
	// v23 has no plain start_date/end_date on a campaign; the run dates live in
	// start_date_time/end_date_time, and a bare date takes the whole-day
	// boundary for its end of the range.
	if upd["name"] != "Brand — EU" || upd["startDateTime"] != "2026-09-01 00:00:00" || upd["endDateTime"] != "2026-12-31 23:59:59" {
		t.Errorf("staged %v", upd)
	}
	if op["updateMask"] != "name,startDateTime,endDateTime" {
		t.Errorf("updateMask = %v", op["updateMask"])
	}
}

func TestUpdateCampaign_AcceptsAnExplicitTimeOfDay(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	// Minute granularity is real for the campaign types that support it, so an
	// explicit time passes through instead of being rounded to a whole day.
	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", EndDate: "2026-12-31 17:30:00"}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if upd := opUpdate(t, cap.firstOp(t), "campaignOperation"); upd["endDateTime"] != "2026-12-31 17:30:00" {
		t.Errorf("endDateTime = %v", upd["endDateTime"])
	}
}

func TestUpdateCampaign_ClearEndDateUnsetsTheLeaf(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", ClearEndDate: true}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op, _ := cap.firstOp(t)["campaignOperation"].(map[string]any)
	if op["updateMask"] != "endDateTime" {
		t.Errorf("updateMask = %v", op["updateMask"])
	}
	// "To set an existing campaign to run indefinitely, clear this field":
	// masked, and left out of the update, is what Google reads as cleared.
	if upd := opUpdate(t, cap.firstOp(t), "campaignOperation"); upd["endDateTime"] != nil {
		t.Errorf("clear must leave endDateTime out of the update, got %v", upd)
	}
}

func TestUpdateCampaign_RenameMasksOnlyTheName(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	// Masking an omitted leaf clears it, so a rename must not touch the dates.
	args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", Name: "Renamed"}
	prev, err := runUpdateCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op, _ := cap.firstOp(t)["campaignOperation"].(map[string]any)
	if op["updateMask"] != "name" {
		t.Errorf("updateMask = %v, want name alone", op["updateMask"])
	}
}

func TestUpdateCampaign_RejectsBadDatesBeforeLookup(t *testing.T) {
	useTempState(t)
	cases := map[string]UpdateCampaignArgs{
		"not a date":       {CustomerID: "1", CampaignID: "5", EndDate: "next Tuesday"},
		"impossible date":  {CustomerID: "1", CampaignID: "5", EndDate: "2026-13-45"},
		"unpadded date":    {CustomerID: "1", CampaignID: "5", StartDate: "2026-1-5"},
		"end before start": {CustomerID: "1", CampaignID: "5", StartDate: "2026-09-01", EndDate: "2026-08-31"},
		"clear and set":    {CustomerID: "1", CampaignID: "5", EndDate: "2026-12-31", ClearEndDate: true},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			// A nil client would panic on any request, so reaching the API at
			// all fails the test.
			if _, err := runUpdateCampaign(t.Context(), nil, args); err == nil {
				t.Error("expected the arguments to be rejected")
			}
		})
	}
}

func TestParseCampaignDate(t *testing.T) {
	// A bare date is completed to the whole-day boundary for its end of the
	// range: 00:00:00 opening, 23:59:59 closing.
	if got, err := parseCampaignDate("start_date", " 2026-09-01 ", campaignDayStart); err != nil || got != "2026-09-01 00:00:00" {
		t.Errorf("start = %q err=%v", got, err)
	}
	if got, err := parseCampaignDate("end_date", "2026-12-31", campaignDayEnd); err != nil || got != "2026-12-31 23:59:59" {
		t.Errorf("end = %q err=%v", got, err)
	}
	if got, err := parseCampaignDate("end_date", "2026-12-31 17:30:00", campaignDayEnd); err != nil || got != "2026-12-31 17:30:00" {
		t.Errorf("explicit time = %q err=%v", got, err)
	}
	for _, bad := range []string{"", "20261231", "2026-12-32", "2026-1-1", "31/12/2026", "2026-12-31T17:30:00", "2026-12-31 17:30"} {
		if _, err := parseCampaignDate("end_date", bad, campaignDayEnd); err == nil {
			t.Errorf("parseCampaignDate(%q) should fail", bad)
		}
	}
}
