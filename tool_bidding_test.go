package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestCreatePortfolioBidding_TargetCPA(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := PortfolioBiddingArgs{CustomerID: "1", Name: "CPA", StrategyType: "TARGET_CPA", TargetCPA: 5.0}
	prev, err := runCreatePortfolioBidding(t.Context(), c, args)
	if err != nil || prev.Token == "" {
		t.Fatalf("preview: %+v err=%v", prev, err)
	}
	args.Confirm = prev.Token
	if _, err := runCreatePortfolioBidding(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	create := opCreate(t, cap.firstOp(t), "biddingStrategyOperation")
	cpa, _ := create["targetCpa"].(map[string]any)
	if cpa == nil || cpa["targetCpaMicros"] != "5000000" {
		t.Errorf("targetCpaMicros wrong: %v", create)
	}
}

func TestCreatePortfolioBidding_Validation(t *testing.T) {
	cases := []PortfolioBiddingArgs{
		{CustomerID: "1", Name: "x", StrategyType: "BOGUS"},
		{CustomerID: "1", StrategyType: "TARGET_CPA", TargetCPA: 5}, // empty name
		{CustomerID: "1", Name: "x", StrategyType: "TARGET_CPA"},    // missing cpa
		{CustomerID: "1", Name: "x", StrategyType: "TARGET_ROAS"},   // missing roas
	}
	for i, args := range cases {
		if _, err := runCreatePortfolioBidding(t.Context(), nil, args); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestCreatePortfolioBidding_ImpressionShare(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)
	args := PortfolioBiddingArgs{CustomerID: "1", Name: "IS", StrategyType: "TARGET_IMPRESSION_SHARE"}
	prev, _ := runCreatePortfolioBidding(t.Context(), c, args)
	args.Confirm = prev.Token
	if _, err := runCreatePortfolioBidding(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	create := opCreate(t, cap.firstOp(t), "biddingStrategyOperation")
	is, _ := create["targetImpressionShare"].(map[string]any)
	if is == nil || is["location"] != "ANYWHERE_ON_PAGE" {
		t.Errorf("impression share config wrong: %v", create)
	}
}

func TestUpdateKeywordBid_PreviewThenApply(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateKeywordBidArgs{CustomerID: "1", AdGroupID: "10", CriterionID: "20", CurrentBid: 1.0, NewBid: 1.5}
	prev, err := runUpdateKeywordBid(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateKeywordBid(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	upd := opUpdate(t, cap.firstOp(t), "adGroupCriterionOperation")
	if upd["cpcBidMicros"] != "1500000" || upd["resourceName"] != "customers/1/adGroupCriteria/10~20" {
		t.Errorf("unexpected bid update: %v", upd)
	}
}

func TestUpdateKeywordBid_RejectsExcessiveIncrease(t *testing.T) {
	// Default cap is 100%; 1.0 -> 2.5 is +150% and must be rejected.
	if _, err := runUpdateKeywordBid(t.Context(), nil, UpdateKeywordBidArgs{CustomerID: "1", AdGroupID: "10", CriterionID: "20", CurrentBid: 1.0, NewBid: 2.5}); err == nil {
		t.Fatal("expected bid-increase guard to reject +150%")
	}
}

func TestUpdateKeywordBid_FetchesBaselineWhenOmitted(t *testing.T) {
	useTempState(t)
	t.Setenv("GOOGLE_ADS_MAX_BID_INCREASE_PCT", "") // default 100%
	// Omitting current_bid used to bypass the bid-increase guard entirely
	// (issue #12); the real bid is now fetched from the API.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "googleAds:search") {
			_, _ = w.Write([]byte(`{"results":[{"adGroupCriterion":{"cpcBidMicros":"1000000"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{}]}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	// Current bid $1.00 fetched; $5.00 is a 400% increase > 100% cap.
	args := UpdateKeywordBidArgs{CustomerID: "1", AdGroupID: "10", CriterionID: "20", NewBid: 5}
	if _, err := runUpdateKeywordBid(t.Context(), c, args); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected bid-increase guard to use the fetched baseline, got %v", err)
	}

	// $1.50 is a 50% increase and passes.
	args.NewBid = 1.5
	if _, err := runUpdateKeywordBid(t.Context(), c, args); err != nil {
		t.Fatalf("50%% increase should pass: %v", err)
	}

	// An inflated caller-supplied current_bid must not beat the fetched
	// baseline: real bid is $1.00, claiming $10 doesn't make $5 a 0% change.
	args.CurrentBid, args.NewBid = 10, 5
	if _, err := runUpdateKeywordBid(t.Context(), c, args); err == nil {
		t.Fatal("fetched baseline should override the caller-supplied current_bid")
	}
}

func TestUpdateKeywordBid_RejectsNonNumericIDs(t *testing.T) {
	useTempState(t)
	if _, err := runUpdateKeywordBid(t.Context(), nil, UpdateKeywordBidArgs{CustomerID: "1", AdGroupID: "x~y", CriterionID: "20", NewBid: 1}); err == nil {
		t.Fatal("non-numeric ad_group_id should be rejected")
	}
}

func TestUpdateKeywordBid_RejectsMissingNewBid(t *testing.T) {
	useTempState(t)
	// Omitting new_bid used to stage a masked cpcBidMicros of "0", silently
	// wiping the keyword's bid (issue #50). It must name clear_bid instead.
	cases := []UpdateKeywordBidArgs{
		{CustomerID: "1", AdGroupID: "10", CriterionID: "20"},                            // no bid at all
		{CustomerID: "1", AdGroupID: "10", CriterionID: "20", NewBid: -1},                // negative
		{CustomerID: "1", AdGroupID: "10", CriterionID: "20", NewBid: 1, ClearBid: true}, // contradictory
	}
	for i, args := range cases {
		if _, err := runUpdateKeywordBid(t.Context(), nil, args); err == nil {
			t.Errorf("case %d: expected the bid change to be rejected", i)
		}
	}
	// The rejection has to point at the flag that expresses the clear.
	_, err := runUpdateKeywordBid(t.Context(), nil, UpdateKeywordBidArgs{CustomerID: "1", AdGroupID: "10", CriterionID: "20"})
	if err == nil || !strings.Contains(err.Error(), "clear_bid") {
		t.Fatalf("error should name clear_bid, got %v", err)
	}
}

func TestUpdateKeywordBid_ClearBidUnsetsTheLeaf(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateKeywordBidArgs{CustomerID: "1", AdGroupID: "10", CriterionID: "20", ClearBid: true}
	prev, err := runUpdateKeywordBid(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(prev.Preview, "Clear keyword 10~20") {
		t.Errorf("preview should name the clear, got %q", prev.Preview)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateKeywordBid(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op, _ := cap.firstOp(t)["adGroupCriterionOperation"].(map[string]any)
	if op["updateMask"] != "cpcBidMicros" {
		t.Errorf("clear must mask cpcBidMicros, got %v", op["updateMask"])
	}
	upd := opUpdate(t, cap.firstOp(t), "adGroupCriterionOperation")
	// Masked but absent is what Google reads as unset; a literal "0" would bid
	// zero rather than inherit the ad group default.
	if _, ok := upd["cpcBidMicros"]; ok {
		t.Errorf("clear must leave cpcBidMicros out of the update, got %v", upd)
	}
	if upd["resourceName"] != "customers/1/adGroupCriteria/10~20" {
		t.Errorf("unexpected resource name: %v", upd)
	}
}

func TestUpdateKeywordBid_ClearBidSkipsTheBaselineLookup(t *testing.T) {
	useTempState(t)
	// A clear is never an increase, so it must not pay for the guard's search
	// round trip — and must not fail when that lookup would.
	var searches int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "googleAds:search") {
			searches++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := runUpdateKeywordBid(t.Context(), c, UpdateKeywordBidArgs{CustomerID: "1", AdGroupID: "10", CriterionID: "20", ClearBid: true}); err != nil {
		t.Fatalf("clear should not need the baseline: %v", err)
	}
	if searches != 0 {
		t.Errorf("clear made %d baseline lookup(s); expected none", searches)
	}
}

func TestCLI_SetKeywordBidRequiresABidOrAClear(t *testing.T) {
	useTempState(t)
	t.Cleanup(func() {
		keywordBidArgs = UpdateKeywordBidArgs{}
		biddingKeywordBidCmd.Flags().VisitAll(func(f *pflag.Flag) {
			f.Changed = false
			_ = f.Value.Set(f.DefValue)
		})
	})
	// The CLI states the same rule the handler enforces, so the mistake is
	// caught before a client is built.
	out, err := runCLI(t, "google", "bidding", "set-keyword-bid", "--ad-group-id", "10", "--criterion-id", "20")
	if err == nil {
		t.Fatalf("expected the CLI to reject a bid update naming no bid; output: %s", out)
	}
	if !strings.Contains(err.Error(), "new-bid") || !strings.Contains(err.Error(), "clear-bid") {
		t.Errorf("error should name both flags, got %v", err)
	}
}
