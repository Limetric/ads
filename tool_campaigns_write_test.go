package main

import (
	"strings"
	"testing"
)

func TestDraftCampaign_PreviewThenApply(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := DraftCampaignArgs{
		CustomerID: "123-456-7890", CampaignName: "Spring Sale", DailyBudget: 30,
		BiddingStrategy: "MAXIMIZE_CONVERSIONS", TargetCPA: 5, ChannelType: "SEARCH", AdGroupName: "AG1",
		Keywords:     []KeywordWithMatchType{{Text: "shoes", MatchType: "EXACT"}},
		GeoTargetIDs: []string{"2840"}, LanguageIDs: []string{"1000"},
	}
	prev, err := runDraftCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.StatusAfterApply != "PAUSED" || prev.NextActionHint == nil {
		t.Errorf("expected PAUSED with hint, got %+v", prev)
	}

	args.Confirm = prev.Token
	if _, err := runDraftCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// budget + campaign + geo + language + ad group + keyword = 6
	ops := cap.lastOps()
	if len(ops) != 6 {
		t.Fatalf("expected 6 ops, got %d", len(ops))
	}
	camp := opCreate(t, ops[1].(map[string]any), "campaignOperation")
	if camp["advertisingChannelType"] != "SEARCH" || camp["status"] != "PAUSED" {
		t.Errorf("campaign op wrong: %v", camp)
	}
	if camp["containsEuPoliticalAdvertising"] != "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING" {
		t.Errorf("missing EU political advertising default: %v", camp)
	}
	mc, _ := camp["maximizeConversions"].(map[string]any)
	if mc == nil || mc["targetCpaMicros"] != "5000000" {
		t.Errorf("bidding strategy wrong: %v", camp)
	}
	ag := opCreate(t, ops[4].(map[string]any), "adGroupOperation")
	if ag["type"] != "SEARCH_STANDARD" || ag["status"] != "PAUSED" {
		t.Errorf("ad group op wrong: %v", ag)
	}
	budget := opCreate(t, ops[0].(map[string]any), "campaignBudgetOperation")
	if shared, ok := budget["explicitlyShared"].(bool); !ok || shared {
		t.Errorf("expected new budget to be dedicated (explicitlyShared: false), got %v", budget["explicitlyShared"])
	}
}

// TestDraftCampaign_DedicatedBudget guards against a regression where the
// budget's explicitlyShared field was left unset: the API then defaults it
// to true, which Google rejects for non-portfolio Smart Bidding strategies
// (MAXIMIZE_CONVERSIONS, TARGET_CPA, ...) with "Bidding strategy type is
// incompatible with shared budget".
func TestDraftCampaign_DedicatedBudget(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := DraftCampaignArgs{
		CustomerID: "1", CampaignName: "x", DailyBudget: 10,
		BiddingStrategy: "MAXIMIZE_CONVERSIONS", ChannelType: "SEARCH", AdGroupName: "ag",
	}
	prev, err := runDraftCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runDraftCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ops := cap.lastOps()
	budget := opCreate(t, ops[0].(map[string]any), "campaignBudgetOperation")
	if shared, ok := budget["explicitlyShared"].(bool); !ok || shared {
		t.Errorf("expected explicitlyShared: false, got %v", budget["explicitlyShared"])
	}
}

func TestDraftCampaign_BudgetCap(t *testing.T) {
	// Default cap is 50; 100/day must be rejected before any op is built.
	if _, err := runDraftCampaign(t.Context(), nil, DraftCampaignArgs{CustomerID: "1", CampaignName: "x", DailyBudget: 100, AdGroupName: "ag"}); err == nil {
		t.Fatal("expected budget cap rejection")
	}
}

func TestDraftCampaign_InvalidKeywordMatchType(t *testing.T) {
	args := DraftCampaignArgs{CustomerID: "1", CampaignName: "x", DailyBudget: 10, AdGroupName: "ag",
		Keywords: []KeywordWithMatchType{{Text: "k", MatchType: "FUZZY"}}}
	if _, err := runDraftCampaign(t.Context(), nil, args); err == nil {
		t.Fatal("expected invalid match type error")
	}
}

func TestDraftCampaign_DisplayChannelAdGroupType(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := DraftCampaignArgs{CustomerID: "1", CampaignName: "x", DailyBudget: 10, ChannelType: "DISPLAY", AdGroupName: "ag", BiddingStrategy: "MANUAL_CPC"}
	prev, _ := runDraftCampaign(t.Context(), c, args)
	args.Confirm = prev.Token
	if _, err := runDraftCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ops := cap.lastOps()
	ag := opCreate(t, ops[len(ops)-1].(map[string]any), "adGroupOperation")
	if ag["type"] != "DISPLAY_STANDARD" {
		t.Errorf("expected DISPLAY_STANDARD, got %v", ag["type"])
	}
}

func TestApplyBiddingStrategyCreate(t *testing.T) {
	cases := []struct {
		strategy, wantKey string
		cpa, roas         float64
		wantSub           map[string]any
	}{
		{"MANUAL_CPC", "manualCpc", 0, 0, map[string]any{}},
		{"MAXIMIZE_CLICKS", "targetSpend", 0, 0, map[string]any{}},
		{"TARGET_SPEND", "targetSpend", 0, 0, map[string]any{}},
		{"TARGET_IMPRESSION_SHARE", "targetImpressionShare", 0, 0, map[string]any{}},
		{"PERCENT_CPC", "percentCpc", 0, 0, map[string]any{}},
		{"MAXIMIZE_CONVERSIONS", "maximizeConversions", 5, 0, map[string]any{"targetCpaMicros": "5000000"}},
		{"MAXIMIZE_CONVERSIONS", "maximizeConversions", 0, 0, map[string]any{}},
		{"MAXIMIZE_CONVERSION_VALUE", "maximizeConversionValue", 0, 4, map[string]any{"targetRoas": 4.0}},
		{"MAXIMIZE_CONVERSION_VALUE", "maximizeConversionValue", 0, 0, map[string]any{}},
		{"TARGET_CPA", "targetCpa", 2.5, 0, map[string]any{"targetCpaMicros": "2500000"}},
		{"TARGET_ROAS", "targetRoas", 0, 3, map[string]any{"targetRoas": 3.0}},
	}
	for _, tc := range cases {
		m := map[string]any{}
		applyBiddingStrategyCreate(m, tc.strategy, tc.cpa, tc.roas)
		sub, ok := m[tc.wantKey].(map[string]any)
		if !ok {
			t.Errorf("%s (cpa=%v roas=%v) should set %s, got %v", tc.strategy, tc.cpa, tc.roas, tc.wantKey, m)
			continue
		}
		if len(sub) != len(tc.wantSub) {
			t.Errorf("%s sub-field = %v, want %v", tc.strategy, sub, tc.wantSub)
			continue
		}
		for k, want := range tc.wantSub {
			if sub[k] != want {
				t.Errorf("%s %s[%s] = %v, want %v", tc.strategy, tc.wantKey, k, sub[k], want)
			}
		}
	}

	t.Run("target strategies without a value leave bidding unset", func(t *testing.T) {
		for _, strategy := range []string{"TARGET_CPA", "TARGET_ROAS"} {
			m := map[string]any{}
			applyBiddingStrategyCreate(m, strategy, 0, 0)
			if len(m) != 0 {
				t.Errorf("%s with zero value should leave bidding unset, got %v", strategy, m)
			}
		}
	})

	t.Run("unknown strategy leaves bidding unset", func(t *testing.T) {
		m := map[string]any{}
		applyBiddingStrategyCreate(m, "NOT_A_STRATEGY", 1, 1)
		if len(m) != 0 {
			t.Errorf("unknown strategy should leave bidding unset, got %v", m)
		}
	})
}

func TestDraftCampaign_BlocksBroadManualCPC(t *testing.T) {
	useTempState(t)
	// The BROAD+MANUAL_CPC guard existed but was never wired in (issue #12).
	_, err := runDraftCampaign(t.Context(), nil, DraftCampaignArgs{
		CustomerID: "1", CampaignName: "C", DailyBudget: 5,
		BiddingStrategy: "MANUAL_CPC", AdGroupName: "AG",
		Keywords: []KeywordWithMatchType{{Text: "shoes", MatchType: "BROAD"}},
	})
	if err == nil || !strings.Contains(err.Error(), "BROAD") {
		t.Fatalf("expected the BROAD+MANUAL_CPC guard to block, got %v", err)
	}
}

// TestDraftCampaign_GeoTargetTypeSetting covers the campaign "Location
// options" on the create path: a create carries the setting as a sub-message
// (no field mask), and each side is written only when supplied.
func TestDraftCampaign_GeoTargetTypeSetting(t *testing.T) {
	tests := []struct {
		name        string
		positive    string
		negative    string
		wantSetting map[string]any
	}{
		{name: "omitted leaves the setting unset"},
		{
			name:        "positive only",
			positive:    "PRESENCE",
			wantSetting: map[string]any{"positiveGeoTargetType": "PRESENCE"},
		},
		{
			name:        "both sides, case insensitive",
			positive:    "presence",
			negative:    "presence_or_interest",
			wantSetting: map[string]any{"positiveGeoTargetType": "PRESENCE", "negativeGeoTargetType": "PRESENCE_OR_INTEREST"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			srv, cap := mutateServer(t)
			defer srv.Close()
			c := newTestClient(t, srv)

			args := DraftCampaignArgs{
				CustomerID: "1", CampaignName: "x", DailyBudget: 10,
				BiddingStrategy: "MAXIMIZE_CONVERSIONS", ChannelType: "SEARCH", AdGroupName: "ag",
				PositiveGeoTargetType: tc.positive,
				NegativeGeoTargetType: tc.negative,
			}
			prev, err := runDraftCampaign(t.Context(), c, args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			args.Confirm = prev.Token
			if _, err := runDraftCampaign(t.Context(), c, args); err != nil {
				t.Fatalf("apply: %v", err)
			}
			camp := opCreate(t, cap.lastOps()[1].(map[string]any), "campaignOperation")
			if tc.wantSetting == nil {
				if _, ok := camp["geoTargetTypeSetting"]; ok {
					t.Fatalf("geoTargetTypeSetting should be absent, got %v", camp["geoTargetTypeSetting"])
				}
				return
			}
			setting, _ := camp["geoTargetTypeSetting"].(map[string]any)
			if len(setting) != len(tc.wantSetting) {
				t.Fatalf("geoTargetTypeSetting = %v, want %v", camp["geoTargetTypeSetting"], tc.wantSetting)
			}
			for k, want := range tc.wantSetting {
				if setting[k] != want {
					t.Errorf("geoTargetTypeSetting[%s] = %v, want %v", k, setting[k], want)
				}
			}
		})
	}
}

func TestDraftCampaign_RejectsInvalidGeoTargetType(t *testing.T) {
	tests := []struct {
		name     string
		positive string
		negative string
		wantText string
	}{
		{"unknown positive", "PRESENSE", "", `unsupported positive_geo_target_type "PRESENSE"`},
		{"unknown negative", "", "INTEREST", `unsupported negative_geo_target_type "INTEREST"`},
		{"deprecated search interest", "SEARCH_INTEREST", "", "SEARCH_INTEREST is deprecated"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			srv, cap := mutateServer(t)
			defer srv.Close()
			c := newTestClient(t, srv)

			_, err := runDraftCampaign(t.Context(), c, DraftCampaignArgs{
				CustomerID: "1", CampaignName: "x", DailyBudget: 10,
				BiddingStrategy: "MAXIMIZE_CONVERSIONS", ChannelType: "SEARCH", AdGroupName: "ag",
				PositiveGeoTargetType: tc.positive,
				NegativeGeoTargetType: tc.negative,
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantText)
			}
			if cap.calls != 0 {
				t.Errorf("mutate calls = %d, want 0", cap.calls)
			}
		})
	}
}

func TestCampaignLocationCriterion_MarksExclusionsNegative(t *testing.T) {
	// The positive criterion must not carry the key at all: "negative": false
	// and an absent negative mean the same thing to Google, but the payload
	// this CLI has always sent for a targeted location is the bare one.
	positive := campaignLocationCriterion("customers/1/campaigns/2", "2840", false)
	create := positive["campaignCriterionOperation"].(map[string]any)["create"].(map[string]any)
	if _, ok := create["negative"]; ok {
		t.Errorf("a targeted location should not carry negative: %v", create)
	}
	negative := campaignLocationCriterion("customers/1/campaigns/2", "2840", true)
	create = negative["campaignCriterionOperation"].(map[string]any)["create"].(map[string]any)
	if create["negative"] != true {
		t.Errorf("an excluded location must be negative: %v", create)
	}
	loc, _ := create["location"].(map[string]any)
	if loc["geoTargetConstant"] != "geoTargetConstants/2840" {
		t.Errorf("exclusion targets the wrong constant: %v", create)
	}
}

func TestValidateGeoTargetSelection(t *testing.T) {
	if err := validateGeoTargetSelection([]string{"2840"}, []string{"2826"}); err != nil {
		t.Fatalf("distinct IDs should be accepted: %v", err)
	}
	// An exclusion wins, so listing the same place on both sides silently does
	// nothing — that is a mistake worth naming.
	err := validateGeoTargetSelection([]string{"2840", "2826"}, []string{"2840"})
	if err == nil || !strings.Contains(err.Error(), "2840") {
		t.Fatalf("expected the overlap to be rejected by ID, got %v", err)
	}
	if err := validateGeoTargetSelection(nil, []string{"abc"}); err == nil {
		t.Fatal("a non-numeric exclusion must be rejected")
	}
}

func TestDraftCampaign_ExcludesGeoTargets(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := DraftCampaignArgs{
		CustomerID: "1", CampaignName: "C", DailyBudget: 10, BiddingStrategy: "MAXIMIZE_CONVERSIONS",
		AdGroupName: "AG", GeoTargetIDs: []string{"2840"}, ExcludeGeoTargetIDs: []string{"2826"},
		NegativeGeoTargetType: "PRESENCE",
	}
	prev, err := runDraftCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runDraftCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	targeted, excluded := countLocationCriteria(t, cap.lastOps())
	if targeted != 1 || excluded != 1 {
		t.Fatalf("targeted=%d excluded=%d, want one of each", targeted, excluded)
	}
}

// countLocationCriteria counts targeted and excluded location criteria in a
// staged batch.
func countLocationCriteria(t *testing.T, ops []any) (targeted, excluded int) {
	t.Helper()
	for _, op := range ops {
		outer, _ := op.(map[string]any)
		criterionOp, _ := outer["campaignCriterionOperation"].(map[string]any)
		if criterionOp == nil {
			continue
		}
		create, _ := criterionOp["create"].(map[string]any)
		if _, isLocation := create["location"]; !isLocation {
			continue
		}
		if create["negative"] == true {
			excluded++
		} else {
			targeted++
		}
	}
	return targeted, excluded
}
