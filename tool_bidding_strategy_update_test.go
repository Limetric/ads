package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// strategyUpdateServer fakes an account holding one portfolio bidding strategy
// of the given type, and records the mutate body.
func strategyUpdateServer(t *testing.T, strategyType, ownerID string, mutateBody *map[string]any, mutates *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			var body struct {
				Query string `json:"query"`
			}
			_ = decodeJSONBody(r, &body)
			if !strings.Contains(body.Query, "accessible_bidding_strategy") {
				t.Errorf("unexpected search query: %s", body.Query)
			}
			_, _ = w.Write([]byte(`{"results":[{"accessibleBiddingStrategy":{"id":"9","name":"Pooled","type":"` +
				strategyType + `","ownerCustomerId":"` + ownerID + `"}}]}`))
		case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
			if mutates != nil {
				*mutates++
			}
			if mutateBody != nil {
				_ = decodeJSONBody(r, mutateBody)
			}
			_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
		}
	}))
}

// strategyUpdateOp digs the biddingStrategyOperation out of a captured mutate.
func strategyUpdateOp(t *testing.T, body map[string]any) (update map[string]any, mask string) {
	t.Helper()
	ops, _ := body["mutateOperations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("mutate operations = %v, want exactly one", body["mutateOperations"])
	}
	outer, _ := ops[0].(map[string]any)
	op, _ := outer["biddingStrategyOperation"].(map[string]any)
	if op == nil {
		t.Fatalf("operation is not a biddingStrategyOperation: %v", outer)
	}
	update, _ = op["update"].(map[string]any)
	mask, _ = op["updateMask"].(string)
	return update, mask
}

// applyStrategyUpdate walks a preview through however many confirmations it
// asks for, and reports how many the write took.
func applyStrategyUpdate(t *testing.T, c *Client, args UpdatePortfolioBiddingArgs, preview WriteResult) int {
	t.Helper()
	rounds := 0
	for token := preview.Token; token != ""; {
		args.Confirm = token
		res, err := runUpdatePortfolioBidding(t.Context(), c, args)
		if err != nil {
			t.Fatalf("confirm round %d: %v", rounds+1, err)
		}
		rounds++
		if res.Applied {
			return rounds
		}
		token = res.Token
	}
	t.Fatal("the write never applied")
	return rounds
}

func TestUpdatePortfolioBidding_ChangesTargetCPA(t *testing.T) {
	useTempState(t)
	var body map[string]any
	srv := strategyUpdateServer(t, "TARGET_CPA", "1", &body, nil)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", TargetCPA: 35}
	preview, err := runUpdatePortfolioBidding(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// The preview names the strategy and says how far the change reaches.
	if !strings.Contains(preview.Preview, "Pooled") || !strings.Contains(preview.Preview, "every campaign attached") {
		t.Errorf("preview does not describe the change: %s", preview.Preview)
	}
	// A target change moves every attached campaign at once, so it takes two.
	if rounds := applyStrategyUpdate(t, c, args, preview); rounds != 2 {
		t.Errorf("a target change took %d confirmation(s), want 2", rounds)
	}
	update, mask := strategyUpdateOp(t, body)
	if update["resourceName"] != "customers/1/biddingStrategies/9" {
		t.Errorf("resourceName = %v", update["resourceName"])
	}
	cpa, _ := update["targetCpa"].(map[string]any)
	if cpa == nil || cpa["targetCpaMicros"] != "35000000" {
		t.Errorf("targetCpa = %v", update["targetCpa"])
	}
	if mask != "targetCpa.targetCpaMicros" {
		t.Errorf("updateMask = %q", mask)
	}
}

func TestUpdatePortfolioBidding_RenameTakesOneConfirmation(t *testing.T) {
	useTempState(t)
	var body map[string]any
	srv := strategyUpdateServer(t, "TARGET_ROAS", "1", &body, nil)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", Name: "Pooled tROAS — EU"}
	preview, err := runUpdatePortfolioBidding(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// A rename changes no bidding behaviour, so it does not take the second
	// confirmation a target change does.
	if rounds := applyStrategyUpdate(t, c, args, preview); rounds != 1 {
		t.Errorf("a rename took %d confirmation(s), want 1", rounds)
	}
	update, mask := strategyUpdateOp(t, body)
	if update["name"] != "Pooled tROAS — EU" || mask != "name" {
		t.Errorf("rename staged %v mask=%q", update, mask)
	}
}

func TestUpdatePortfolioBidding_RenameAndTargetTogether(t *testing.T) {
	useTempState(t)
	var body map[string]any
	srv := strategyUpdateServer(t, "TARGET_ROAS", "1", &body, nil)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", Name: "Renamed", TargetROAS: 4.5}
	preview, err := runUpdatePortfolioBidding(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	applyStrategyUpdate(t, c, args, preview)
	update, mask := strategyUpdateOp(t, body)
	roas, _ := update["targetRoas"].(map[string]any)
	if roas == nil || roas["targetRoas"] != 4.5 {
		t.Errorf("targetRoas = %v", update["targetRoas"])
	}
	if mask != "name,targetRoas.targetRoas" {
		t.Errorf("updateMask = %q, want both leaves", mask)
	}
}

func TestUpdatePortfolioBidding_ImpressionShareMasksOnlySuppliedLeaves(t *testing.T) {
	useTempState(t)
	var body map[string]any
	srv := strategyUpdateServer(t, "TARGET_IMPRESSION_SHARE", "1", &body, nil)
	defer srv.Close()
	c := newTestClient(t, srv)

	// Only the fraction is supplied; masking the location too would reset a
	// position the operator never mentioned.
	args := UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", ImpressionSharePercent: 65}
	preview, err := runUpdatePortfolioBidding(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	applyStrategyUpdate(t, c, args, preview)
	update, mask := strategyUpdateOp(t, body)
	is, _ := update["targetImpressionShare"].(map[string]any)
	if is == nil || is["locationFractionMicros"] != "650000" {
		t.Errorf("targetImpressionShare = %v, want 65%% as 650000 micros", update["targetImpressionShare"])
	}
	if _, ok := is["location"]; ok {
		t.Errorf("location was not supplied but was staged: %v", is)
	}
	if mask != "targetImpressionShare.locationFractionMicros" {
		t.Errorf("updateMask = %q, want only the supplied leaf", mask)
	}
}

func TestUpdatePortfolioBidding_ImpressionShareTakesBothLeaves(t *testing.T) {
	useTempState(t)
	var body map[string]any
	srv := strategyUpdateServer(t, "TARGET_IMPRESSION_SHARE", "1", &body, nil)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", ImpressionShareLocation: "absolute_top_of_page", ImpressionSharePercent: 50}
	preview, err := runUpdatePortfolioBidding(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	applyStrategyUpdate(t, c, args, preview)
	update, mask := strategyUpdateOp(t, body)
	is, _ := update["targetImpressionShare"].(map[string]any)
	if is["location"] != "ABSOLUTE_TOP_OF_PAGE" || is["locationFractionMicros"] != "500000" {
		t.Errorf("targetImpressionShare = %v", is)
	}
	if mask != "targetImpressionShare.location,targetImpressionShare.locationFractionMicros" {
		t.Errorf("updateMask = %q", mask)
	}
}

func TestUpdatePortfolioBidding_MaximizeStrategiesCarryOptionalTargets(t *testing.T) {
	useTempState(t)
	for _, tc := range []struct {
		strategyType, field, mask string
		args                      UpdatePortfolioBiddingArgs
	}{
		{"MAXIMIZE_CONVERSIONS", "maximizeConversions", "maximizeConversions.targetCpaMicros",
			UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", TargetCPA: 12}},
		{"MAXIMIZE_CONVERSION_VALUE", "maximizeConversionValue", "maximizeConversionValue.targetRoas",
			UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", TargetROAS: 3}},
	} {
		t.Run(tc.strategyType, func(t *testing.T) {
			var body map[string]any
			srv := strategyUpdateServer(t, tc.strategyType, "1", &body, nil)
			defer srv.Close()
			c := newTestClient(t, srv)
			preview, err := runUpdatePortfolioBidding(t.Context(), c, tc.args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			applyStrategyUpdate(t, c, tc.args, preview)
			update, mask := strategyUpdateOp(t, body)
			if _, ok := update[tc.field].(map[string]any); !ok {
				t.Errorf("update has no %s: %v", tc.field, update)
			}
			if mask != tc.mask {
				t.Errorf("updateMask = %q, want %q", mask, tc.mask)
			}
		})
	}
}

func TestUpdatePortfolioBidding_RejectsMismatchedTargetBeforeStaging(t *testing.T) {
	useTempState(t)
	var mutates int
	srv := strategyUpdateServer(t, "TARGET_CPA", "1", nil, &mutates)
	defer srv.Close()
	c := newTestClient(t, srv)

	// A shared strategy's type is fixed after creation, so a ROAS target on a
	// TARGET_CPA strategy is a mistake, not a type switch.
	args := UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", TargetROAS: 3}
	_, err := runUpdatePortfolioBidding(t.Context(), c, args)
	if err == nil || !strings.Contains(err.Error(), "target_cpa") {
		t.Fatalf("expected the error to name the target this strategy takes, got %v", err)
	}
	if mutates != 0 {
		t.Errorf("a rejected update reached the API %d time(s)", mutates)
	}
}

func TestUpdatePortfolioBidding_RejectsAManagerOwnedStrategy(t *testing.T) {
	useTempState(t)
	var mutates int
	// Owned by the manager (99), merely visible from the child (1).
	srv := strategyUpdateServer(t, "TARGET_CPA", "99", nil, &mutates)
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := runUpdatePortfolioBidding(t.Context(), c, UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", TargetCPA: 20})
	if err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("expected an error naming the owning account, got %v", err)
	}
	if mutates != 0 {
		t.Errorf("a rejected update reached the API %d time(s)", mutates)
	}
}

func TestUpdatePortfolioBidding_AcceptsAResourceName(t *testing.T) {
	useTempState(t)
	var body map[string]any
	srv := strategyUpdateServer(t, "TARGET_CPA", "1", &body, nil)
	defer srv.Close()
	c := newTestClient(t, srv)

	// create_portfolio_bidding_strategy returns resource names; passing one
	// straight back must work, as it does for update_campaign.
	args := UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "customers/1/biddingStrategies/9", TargetCPA: 8}
	preview, err := runUpdatePortfolioBidding(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	applyStrategyUpdate(t, c, args, preview)
	update, _ := strategyUpdateOp(t, body)
	if update["resourceName"] != "customers/1/biddingStrategies/9" {
		t.Errorf("resourceName = %v", update["resourceName"])
	}
}

func TestUpdatePortfolioBidding_ValidatesArgumentsWithoutAnAPICall(t *testing.T) {
	useTempState(t)
	cases := map[string]UpdatePortfolioBiddingArgs{
		"no changes":                  {CustomerID: "1", StrategyID: "9"},
		"both targets":                {CustomerID: "1", StrategyID: "9", TargetCPA: 5, TargetROAS: 3},
		"negative target":             {CustomerID: "1", StrategyID: "9", TargetCPA: -5},
		"unknown location":            {CustomerID: "1", StrategyID: "9", ImpressionShareLocation: "BOTTOM_OF_PAGE"},
		"percent above 100":           {CustomerID: "1", StrategyID: "9", ImpressionSharePercent: 150},
		"percent below 1":             {CustomerID: "1", StrategyID: "9", ImpressionSharePercent: 0.5},
		"non-numeric strategy ID":     {CustomerID: "1", StrategyID: "abc", TargetCPA: 5},
		"wrong kind of resource name": {CustomerID: "1", StrategyID: "customers/1/campaigns/9", TargetCPA: 5},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			// A nil client would panic on any request, so reaching the API at
			// all fails the test.
			if _, err := runUpdatePortfolioBidding(t.Context(), nil, args); err == nil {
				t.Error("expected a validation error")
			}
		})
	}
}

func TestUpdatePortfolioBidding_HonorsBlockedOps(t *testing.T) {
	t.Setenv("GOOGLE_ADS_BLOCKED_OPS", "update_portfolio_bidding_strategy")
	if _, err := runUpdatePortfolioBidding(t.Context(), nil, UpdatePortfolioBiddingArgs{CustomerID: "1", StrategyID: "9", TargetCPA: 5}); err == nil {
		t.Fatal("a blocked operation must fail before anything is staged")
	}
}

func TestImpressionShareMicros(t *testing.T) {
	for percent, want := range map[float64]int64{50: 500_000, 100: 1_000_000, 1: 10_000, 65.5: 655_000} {
		if got := impressionShareMicros(percent); got != want {
			t.Errorf("impressionShareMicros(%v) = %d, want %d", percent, got, want)
		}
	}
}
