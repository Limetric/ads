package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dsaCapture records what the fake API saw: the mutate body, and the GAQL the
// dynamic ad group guard looked the type up with.
type dsaCapture struct {
	mutateCapture
	guardQuery string
}

// dsaServer is mutateServer plus an ad_group.type lookup, so the dynamic ad
// group guard can be exercised against a known type. An empty adGroupType
// answers with no rows, which is what an unresolvable ad group looks like.
func dsaServer(t *testing.T, adGroupType string) (*httptest.Server, *dsaCapture) {
	t.Helper()
	cap := &dsaCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			var body struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			cap.guardQuery = body.Query
			rows := "[]"
			if adGroupType != "" {
				rows = `[{"adGroup":{"type":"` + adGroupType + `"}}]`
			}
			_, _ = w.Write([]byte(`{"results":` + rows + `}`))
		case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
			cap.calls++
			_ = json.NewDecoder(r.Body).Decode(&cap.lastBody)
			_, _ = w.Write([]byte(`{"mutateOperationResponses":[{"adGroupCriterionResult":{"resourceName":"customers/1/adGroupCriteria/9~2"}}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	return srv, cap
}

// webpageOf digs the webpage criterion out of an adGroupCriterionOperation create.
func webpageOf(t *testing.T, op map[string]any) map[string]any {
	t.Helper()
	create := opCreate(t, op, "adGroupCriterionOperation")
	webpage, ok := create["webpage"].(map[string]any)
	if !ok {
		t.Fatalf("criterion has no webpage: %v", create)
	}
	return webpage
}

// --- dynamicSearchAdsSetting ---

func TestDynamicSearchAdsSetting(t *testing.T) {
	tests := []struct {
		name             string
		domain, language string
		suppliedOnly     bool
		wantNil          bool
		wantErr          string
	}{
		{name: "unset leaves the campaign alone", wantNil: true},
		{
			name: "supplied-urls-only alone is not a DSA campaign",
			// The flag configures a setting that is not being created, so it
			// would silently do nothing.
			suppliedOnly: true, wantErr: "dsa_use_supplied_urls_only",
		},
		{name: "domain without language", domain: "example.com", wantErr: "dsa_language_code is required"},
		{name: "language without domain", language: "en", wantErr: "dsa_domain is required"},
		{name: "domain written as a URL", domain: "https://example.com", language: "en", wantErr: "must be a bare domain"},
		{name: "domain with a path", domain: "example.com/shop", language: "en", wantErr: "must be a bare domain"},
		{name: "complete setting", domain: "example.com", language: "en"},
		{name: "supplied urls only", domain: "www.example.com", language: "de", suppliedOnly: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setting, mask, err := dynamicSearchAdsSetting(tt.domain, tt.language, tt.suppliedOnly)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got setting %v", tt.wantErr, setting)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if setting != nil || mask != nil {
					t.Fatalf("expected no setting, got %v / %v", setting, mask)
				}
				return
			}
			if setting["domainName"] != tt.domain || setting["languageCode"] != tt.language {
				t.Errorf("setting = %v", setting)
			}
			if setting["useSuppliedUrlsOnly"] != tt.suppliedOnly {
				t.Errorf("useSuppliedUrlsOnly = %v, want %v", setting["useSuppliedUrlsOnly"], tt.suppliedOnly)
			}
			// The message has defined sub-fields, so it can never appear bare
			// in a field mask; all three leaves travel together because Google
			// requires the domain and the language on every write.
			want := "dynamicSearchAdsSetting.domainName,dynamicSearchAdsSetting.languageCode,dynamicSearchAdsSetting.useSuppliedUrlsOnly"
			if got := strings.Join(mask, ","); got != want {
				t.Errorf("mask = %q, want %q", got, want)
			}
		})
	}
}

func TestDynamicSearchAdsSetting_TrimsSurroundingSpace(t *testing.T) {
	setting, _, err := dynamicSearchAdsSetting("  example.com ", " en ", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if setting["domainName"] != "example.com" || setting["languageCode"] != "en" {
		t.Errorf("setting = %v", setting)
	}
}

// --- parseAdGroupType ---

func TestParseAdGroupType(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "SEARCH_STANDARD", want: "SEARCH_STANDARD"},
		{in: "SEARCH_DYNAMIC_ADS", want: "SEARCH_DYNAMIC_ADS"},
		{in: "search_dynamic_ads", want: "SEARCH_DYNAMIC_ADS"},
		{in: "  DISPLAY_STANDARD  ", want: "DISPLAY_STANDARD"},
		// A type that exists in the enum but belongs to a campaign kind with
		// its own creation path is still refused here.
		{in: "SHOPPING_PRODUCT_ADS", wantErr: true},
		{in: "NOPE", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseAdGroupType(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseAdGroupType(%q) = %q, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("parseAdGroupType(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

// --- parseWebpageConditionFlag ---

func TestParseWebpageConditionFlag(t *testing.T) {
	tests := []struct {
		in                string
		operand, argument string
	}{
		{in: "URL=/specialoffers", operand: "URL", argument: "/specialoffers"},
		{in: "category=Shoes", operand: "CATEGORY", argument: "Shoes"},
		// The split is faithful; the surrounding space is trimmed when the
		// condition is staged (see TestAddWebpageTargets_TrimsConditionArguments).
		{in: " PAGE_TITLE = Sale ", operand: "PAGE_TITLE", argument: " Sale "},
		// A URL argument may carry its own "=", so only the first one splits.
		{in: "URL=/p?id=7&ref=a", operand: "URL", argument: "/p?id=7&ref=a"},
		// Without a separator the whole value is left as the operand so the
		// validator names it rather than targeting something unintended.
		{in: "URL/specialoffers", operand: "URL/specialoffers", argument: ""},
	}
	for _, tt := range tests {
		got := parseWebpageConditionFlag(tt.in)
		if got.Operand != tt.operand || got.Argument != tt.argument {
			t.Errorf("parseWebpageConditionFlag(%q) = %+v, want operand %q argument %q", tt.in, got, tt.operand, tt.argument)
		}
	}
}

// --- add_webpage_targets ---

func TestAddWebpageTargets_StagesConditionsAndBid(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_DYNAMIC_ADS")
	defer srv.Close()
	c := newTestClient(t, srv)

	args := AddWebpageTargetsArgs{
		CustomerID: "123-456-7890",
		AdGroupID:  "9",
		Targets: []WebpageTarget{{
			CriterionName: "Special offers",
			Conditions: []WebpageCondition{
				{Operand: "url", Argument: "/specialoffers"},
				{Operand: "PAGE_TITLE", Argument: "Special Offer"},
			},
			CpcBidMicros: 1_500_000,
		}},
	}
	prev, err := runAddWebpageTargets(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(prev.Preview, "URL=/specialoffers AND PAGE_TITLE=Special Offer") {
		t.Errorf("preview should describe the conditions, got %q", prev.Preview)
	}
	if cap.calls != 0 {
		t.Fatalf("preview must not mutate, got %d call(s)", cap.calls)
	}
	// The guard fails open, so a lookup that stopped asking the right question
	// would leave it silently dead rather than failing a test.
	for _, want := range []string{"ad_group.type", "ad_group.id = 9"} {
		if !strings.Contains(cap.guardQuery, want) {
			t.Errorf("guard query missing %q:\n%s", want, cap.guardQuery)
		}
	}

	args.Confirm = prev.Token
	if _, err := runAddWebpageTargets(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op := cap.firstOp(t)
	create := opCreate(t, op, "adGroupCriterionOperation")
	if create["adGroup"] != "customers/1234567890/adGroups/9" {
		t.Errorf("adGroup = %v", create["adGroup"])
	}
	if create["cpcBidMicros"] != "1500000" {
		t.Errorf("cpcBidMicros = %v", create["cpcBidMicros"])
	}
	webpage := webpageOf(t, op)
	if webpage["criterionName"] != "Special offers" {
		t.Errorf("criterionName = %v", webpage["criterionName"])
	}
	conditions, _ := webpage["conditions"].([]any)
	if len(conditions) != 2 {
		t.Fatalf("expected 2 conditions, got %v", webpage["conditions"])
	}
	first, _ := conditions[0].(map[string]any)
	// A lower-case operand from the caller is normalised to the enum value.
	if first["operand"] != "URL" || first["argument"] != "/specialoffers" {
		t.Errorf("first condition = %v", first)
	}
	// The operator is deliberately not sent: Google's own DSA sample omits it.
	if _, exists := first["operator"]; exists {
		t.Errorf("condition should not carry an operator: %v", first)
	}
}

func TestAddWebpageTargets_AllWebpagesSendsEmptyConditions(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_DYNAMIC_ADS")
	defer srv.Close()
	c := newTestClient(t, srv)

	args := AddWebpageTargetsArgs{
		CustomerID: "1", AdGroupID: "9",
		Targets: []WebpageTarget{{CriterionName: "All pages", AllWebpages: true}},
	}
	prev, err := runAddWebpageTargets(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(prev.Preview, "all webpages") {
		t.Errorf("preview should say the whole site is targeted, got %q", prev.Preview)
	}
	args.Confirm = prev.Token
	if _, err := runAddWebpageTargets(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	webpage := webpageOf(t, cap.firstOp(t))
	// An empty condition list is what Google reads as "every page of the
	// domain", so it is sent rather than omitted.
	conditions, ok := webpage["conditions"].([]any)
	if !ok || len(conditions) != 0 {
		t.Errorf("conditions = %v, want an empty list", webpage["conditions"])
	}
}

func TestAddWebpageTargets_StagesEveryTargetInOneBatch(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_DYNAMIC_ADS")
	defer srv.Close()
	c := newTestClient(t, srv)

	args := AddWebpageTargetsArgs{
		CustomerID: "1", AdGroupID: "9",
		Targets: []WebpageTarget{
			{CriterionName: "Shoes", Conditions: []WebpageCondition{{Operand: "CATEGORY", Argument: "Shoes"}}},
			{CriterionName: "Sale", Conditions: []WebpageCondition{{Operand: "URL", Argument: "/sale"}}},
		},
	}
	prev, err := runAddWebpageTargets(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runAddWebpageTargets(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ops := cap.lastOps(); len(ops) != 2 {
		t.Fatalf("expected 2 operations in one batch, got %d", len(ops))
	}
}

func TestAddWebpageTargets_Validation(t *testing.T) {
	tests := []struct {
		name    string
		targets []WebpageTarget
		wantErr string
	}{
		{name: "no targets", wantErr: "at least one dynamic ad target"},
		{
			name:    "no criterion name",
			targets: []WebpageTarget{{Conditions: []WebpageCondition{{Operand: "URL", Argument: "/x"}}}},
			wantErr: "criterion_name is required",
		},
		{
			// Silently targeting the whole site is the expensive mistake here,
			// so the empty condition list has to be asked for.
			name:    "neither conditions nor all_webpages",
			targets: []WebpageTarget{{CriterionName: "Empty"}},
			wantErr: "at least one condition is required",
		},
		{
			name:    "conditions and all_webpages together",
			targets: []WebpageTarget{{CriterionName: "Both", AllWebpages: true, Conditions: []WebpageCondition{{Operand: "URL", Argument: "/x"}}}},
			wantErr: "cannot be combined with conditions",
		},
		{
			name:    "unknown operand",
			targets: []WebpageTarget{{CriterionName: "T", Conditions: []WebpageCondition{{Operand: "PAGE_URL", Argument: "/x"}}}},
			wantErr: "unsupported operand",
		},
		{
			name:    "operand with no argument",
			targets: []WebpageTarget{{CriterionName: "T", Conditions: []WebpageCondition{{Operand: "URL"}}}},
			wantErr: "needs an argument",
		},
		{
			name:    "negative bid",
			targets: []WebpageTarget{{CriterionName: "T", AllWebpages: true, CpcBidMicros: -1}},
			wantErr: "must be positive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runAddWebpageTargets(t.Context(), nil, AddWebpageTargetsArgs{CustomerID: "1", AdGroupID: "9", Targets: tt.targets})
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestAddWebpageTargets_RejectsAStandardAdGroup(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_STANDARD")
	defer srv.Close()
	c := newTestClient(t, srv)

	// Ad group type is immutable, so this can never be fixed after the fact —
	// the preview says so rather than staging a write Google rejects at confirm.
	_, err := runAddWebpageTargets(t.Context(), c, AddWebpageTargetsArgs{
		CustomerID: "1", AdGroupID: "9",
		Targets: []WebpageTarget{{CriterionName: "T", AllWebpages: true}},
	})
	if err == nil {
		t.Fatal("expected an error for a standard ad group")
	}
	if !strings.Contains(err.Error(), "SEARCH_DYNAMIC_ADS") {
		t.Errorf("error should name the type needed, got %q", err)
	}
	if cap.calls != 0 {
		t.Errorf("nothing should have been mutated, got %d call(s)", cap.calls)
	}
}

func TestAddWebpageTargets_ProceedsWhenTheAdGroupTypeIsUnknown(t *testing.T) {
	useTempState(t)
	// No rows: the guard only turns a confirm-time rejection into a
	// preview-time error, so a read that cannot answer must not block the write.
	srv, _ := dsaServer(t, "")
	defer srv.Close()

	if _, err := runAddWebpageTargets(t.Context(), newTestClient(t, srv), AddWebpageTargetsArgs{
		CustomerID: "1", AdGroupID: "9",
		Targets: []WebpageTarget{{CriterionName: "T", AllWebpages: true}},
	}); err != nil {
		t.Fatalf("an unresolvable ad group type must not block the preview: %v", err)
	}
}

func TestAddWebpageTargets_ProceedsWhenTheLookupFails(t *testing.T) {
	useTempState(t)
	srv := errServer(t)
	defer srv.Close()

	if _, err := runAddWebpageTargets(t.Context(), newTestClient(t, srv), AddWebpageTargetsArgs{
		CustomerID: "1", AdGroupID: "9",
		Targets: []WebpageTarget{{CriterionName: "T", AllWebpages: true}},
	}); err != nil {
		t.Fatalf("a failed guard lookup must not block the preview: %v", err)
	}
}

func TestAddWebpageTargets_RejectsNonNumericAdGroupID(t *testing.T) {
	useTempState(t)
	_, err := runAddWebpageTargets(t.Context(), nil, AddWebpageTargetsArgs{
		CustomerID: "1", AdGroupID: "abc",
		Targets: []WebpageTarget{{CriterionName: "T", AllWebpages: true}},
	})
	if err == nil {
		t.Fatal("expected an error for a non-numeric ad group ID")
	}
}

// --- draft_dynamic_search_ad ---

func TestDraftDynamicSearchAd_StagesDescriptionsOnly(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_DYNAMIC_ADS")
	defer srv.Close()
	c := newTestClient(t, srv)

	args := DraftDsaArgs{
		CustomerID: "123-456-7890", AdGroupID: "9",
		Description: "Buy tickets now!", Description2: "Free delivery on every order.",
	}
	prev, err := runDraftDynamicSearchAd(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.StatusAfterApply != "PAUSED" || prev.NextActionHint == nil {
		t.Fatalf("expected PAUSED with an enable hint, got %+v", prev)
	}
	if prev.NextActionHint.Params["entity_type"] != "ad" {
		t.Errorf("hint = %v", prev.NextActionHint.Params)
	}

	args.Confirm = prev.Token
	if _, err := runDraftDynamicSearchAd(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	create := opCreate(t, cap.firstOp(t), "adGroupAdOperation")
	if create["status"] != "PAUSED" || create["adGroup"] != "customers/1234567890/adGroups/9" {
		t.Errorf("create = %v", create)
	}
	ad, _ := create["ad"].(map[string]any)
	// Google generates the headline and the landing page, so neither headline
	// assets nor finalUrls belong on a DSA — the RSA builder's shape does not
	// carry over.
	if _, exists := ad["finalUrls"]; exists {
		t.Errorf("a dynamic search ad must not carry finalUrls: %v", ad)
	}
	if _, exists := ad["responsiveSearchAd"]; exists {
		t.Errorf("ad should not be an RSA: %v", ad)
	}
	dsa, ok := ad["expandedDynamicSearchAd"].(map[string]any)
	if !ok {
		t.Fatalf("ad has no expandedDynamicSearchAd: %v", ad)
	}
	if dsa["description"] != "Buy tickets now!" || dsa["description2"] != "Free delivery on every order." {
		t.Errorf("expandedDynamicSearchAd = %v", dsa)
	}
	if _, exists := dsa["headlines"]; exists {
		t.Errorf("a dynamic search ad has no headlines: %v", dsa)
	}
}

func TestDraftDynamicSearchAd_OmitsAnAbsentSecondDescription(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_DYNAMIC_ADS")
	defer srv.Close()
	c := newTestClient(t, srv)

	args := DraftDsaArgs{CustomerID: "1", AdGroupID: "9", Description: "Buy tickets now!"}
	prev, err := runDraftDynamicSearchAd(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runDraftDynamicSearchAd(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	create := opCreate(t, cap.firstOp(t), "adGroupAdOperation")
	ad, _ := create["ad"].(map[string]any)
	dsa, _ := ad["expandedDynamicSearchAd"].(map[string]any)
	if _, exists := dsa["description2"]; exists {
		t.Errorf("description2 should be absent when not supplied: %v", dsa)
	}
}

func TestDraftDynamicSearchAd_ExplicitEnabledCarriesNoHint(t *testing.T) {
	useTempState(t)
	srv, _ := dsaServer(t, "SEARCH_DYNAMIC_ADS")
	defer srv.Close()

	prev, err := runDraftDynamicSearchAd(t.Context(), newTestClient(t, srv), DraftDsaArgs{
		CustomerID: "1", AdGroupID: "9", Description: "Buy now", Status: "ENABLED",
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.StatusAfterApply != "ENABLED" || prev.NextActionHint != nil {
		t.Errorf("ENABLED should carry no hint, got %+v", prev)
	}
}

func TestDraftDynamicSearchAd_Validation(t *testing.T) {
	tests := []struct {
		name    string
		args    DraftDsaArgs
		wantErr string
	}{
		{
			name:    "missing description",
			args:    DraftDsaArgs{CustomerID: "1", AdGroupID: "9"},
			wantErr: "description is required",
		},
		{
			name:    "blank description",
			args:    DraftDsaArgs{CustomerID: "1", AdGroupID: "9", Description: "   "},
			wantErr: "description is required",
		},
		{
			name:    "description too long",
			args:    DraftDsaArgs{CustomerID: "1", AdGroupID: "9", Description: strings.Repeat("a", 91)},
			wantErr: "description",
		},
		{
			name:    "second description too long",
			args:    DraftDsaArgs{CustomerID: "1", AdGroupID: "9", Description: "ok", Description2: strings.Repeat("a", 91)},
			wantErr: "description",
		},
		{
			name:    "removed status",
			args:    DraftDsaArgs{CustomerID: "1", AdGroupID: "9", Description: "ok", Status: "REMOVED"},
			wantErr: "REMOVED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runDraftDynamicSearchAd(t.Context(), nil, tt.args)
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestDraftDynamicSearchAd_RejectsAStandardAdGroup(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_STANDARD")
	defer srv.Close()

	_, err := runDraftDynamicSearchAd(t.Context(), newTestClient(t, srv), DraftDsaArgs{
		CustomerID: "1", AdGroupID: "9", Description: "Buy now",
	})
	if err == nil {
		t.Fatal("expected an error for a standard ad group")
	}
	if !strings.Contains(err.Error(), "SEARCH_DYNAMIC_ADS") {
		t.Errorf("error should name the type needed, got %q", err)
	}
	if cap.calls != 0 {
		t.Errorf("nothing should have been mutated, got %d call(s)", cap.calls)
	}
}

func TestAddWebpageTargets_TrimsConditionArguments(t *testing.T) {
	useTempState(t)
	srv, cap := dsaServer(t, "SEARCH_DYNAMIC_ADS")
	defer srv.Close()
	c := newTestClient(t, srv)

	// Google matches the argument literally, so a condition written with
	// natural spacing must not stage a target that matches nothing.
	args := AddWebpageTargetsArgs{
		CustomerID: "1", AdGroupID: "9",
		Targets: []WebpageTarget{{
			CriterionName: "Sale",
			Conditions:    []WebpageCondition{{Operand: "URL", Argument: " /sale "}},
		}},
	}
	prev, err := runAddWebpageTargets(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runAddWebpageTargets(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	conditions, _ := webpageOf(t, cap.firstOp(t))["conditions"].([]any)
	first, _ := conditions[0].(map[string]any)
	if first["argument"] != "/sale" {
		t.Errorf("argument = %q, want it trimmed to \"/sale\"", first["argument"])
	}
}
