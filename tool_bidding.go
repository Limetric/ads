package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// This file creates portfolio bidding strategies and updates keyword CPC bids.
// Both writes preview first; the keyword-bid update also enforces the
// bid-increase guard.

var validStrategyTypes = map[string]bool{
	"TARGET_CPA": true, "TARGET_ROAS": true, "TARGET_IMPRESSION_SHARE": true,
}

// PortfolioBiddingArgs creates a portfolio (shared) bidding strategy.
type PortfolioBiddingArgs struct {
	CustomerID   string  `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that will own the strategy; omit to use the configured default customer"`
	Name         string  `json:"name" jsonschema:"a name for the bidding strategy"`
	StrategyType string  `json:"strategy_type" jsonschema:"one of: TARGET_CPA, TARGET_ROAS, TARGET_IMPRESSION_SHARE"`
	TargetCPA    float64 `json:"target_cpa,omitempty" jsonschema:"target CPA in currency units (required for TARGET_CPA)"`
	TargetROAS   float64 `json:"target_roas,omitempty" jsonschema:"target ROAS as a ratio, e.g. 3.0 (required for TARGET_ROAS)"`
	Confirm      string  `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreatePortfolioBidding(ctx context.Context, c *Client, args PortfolioBiddingArgs) (WriteResult, error) {
	const tool = "create_portfolio_bidding_strategy"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if !validStrategyTypes[args.StrategyType] {
		return WriteResult{}, fmt.Errorf("invalid strategy type %q: must be TARGET_CPA, TARGET_ROAS, or TARGET_IMPRESSION_SHARE", args.StrategyType)
	}
	if args.Name == "" {
		return WriteResult{}, fmt.Errorf("strategy name cannot be empty")
	}

	strategy := map[string]any{"name": args.Name, "type": args.StrategyType}
	switch args.StrategyType {
	case "TARGET_CPA":
		if args.TargetCPA <= 0 {
			return WriteResult{}, fmt.Errorf("target_cpa is required for TARGET_CPA strategy")
		}
		strategy["targetCpa"] = map[string]any{"targetCpaMicros": microsString(dollarsToMicros(args.TargetCPA))}
	case "TARGET_ROAS":
		if args.TargetROAS <= 0 {
			return WriteResult{}, fmt.Errorf("target_roas is required for TARGET_ROAS strategy")
		}
		strategy["targetRoas"] = map[string]any{"targetRoas": args.TargetROAS}
	case "TARGET_IMPRESSION_SHARE":
		strategy["targetImpressionShare"] = map[string]any{
			"location":               "ANYWHERE_ON_PAGE",
			"locationFractionMicros": "500000",
		}
	}

	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	op := map[string]any{"biddingStrategyOperation": map[string]any{"create": strategy}}
	summary := fmt.Sprintf("Create %s portfolio bidding strategy %q", args.StrategyType, args.Name)
	return previewMutate(tool, cid, summary, []any{op})
}

// UpdateKeywordBidArgs updates a keyword's CPC bid, enforcing the bid-increase
// guard against the supplied current bid.
type UpdateKeywordBidArgs struct {
	CustomerID  string  `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the keyword; omit to use the configured default customer"`
	AdGroupID   string  `json:"ad_group_id" jsonschema:"the ad group ID"`
	CriterionID string  `json:"criterion_id" jsonschema:"the keyword criterion ID"`
	CurrentBid  float64 `json:"current_bid" jsonschema:"the current bid in currency units (for the safety check)"`
	NewBid      float64 `json:"new_bid" jsonschema:"the desired new bid in currency units; required unless clear_bid is set"`
	// ClearBid removes the keyword's own bid. Omitting new_bid cannot express
	// this: every other numeric field in this CLI reads an omitted value as
	// "leave it alone", and a bid wipe is far too destructive to infer from an
	// absent flag (issue #50).
	ClearBid bool   `json:"clear_bid,omitempty" jsonschema:"remove the keyword's own CPC bid so it falls back to the ad group default; cannot be combined with new_bid"`
	Confirm  string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

// validateKeywordBidChange rejects a bid update that names no bid to set. A
// zero new_bid used to pass straight through and stage a masked cpcBidMicros of
// 0, which clears the keyword's bid — the destructive reading of an omitted
// value, arrived at by accident (issue #50).
func validateKeywordBidChange(args UpdateKeywordBidArgs) error {
	if args.ClearBid {
		if args.NewBid != 0 {
			return fmt.Errorf("clear_bid cannot be combined with new_bid — pass clear_bid alone to drop the keyword's own bid, or new_bid alone to change it")
		}
		return nil
	}
	if args.NewBid <= 0 {
		return fmt.Errorf("new_bid must be positive (currency units), got %v — pass clear_bid to remove the keyword's own bid and let it fall back to the ad group default", args.NewBid)
	}
	return nil
}

func runUpdateKeywordBid(ctx context.Context, c *Client, args UpdateKeywordBidArgs) (WriteResult, error) {
	const tool = "update_keyword_bid"
	cfg := loadSafetyConfig()
	if err := checkBlockedOperation(tool, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	if err := validateKeywordBidChange(args); err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	adGroupID, err := numericID("ad_group_id", args.AdGroupID)
	if err != nil {
		return WriteResult{}, err
	}
	criterionID, err := numericID("criterion_id", args.CriterionID)
	if err != nil {
		return WriteResult{}, err
	}
	resource := fmt.Sprintf("customers/%s/adGroupCriteria/%s~%s", cid, adGroupID, criterionID)
	update := map[string]any{"resourceName": resource}
	summary := fmt.Sprintf("Clear keyword %s~%s CPC bid so it falls back to the ad group default", args.AdGroupID, args.CriterionID)
	if !args.ClearBid {
		// The bid-increase guard needs a trustworthy baseline, so the real bid
		// is always fetched from the API — a caller-supplied current_bid
		// (omitted or inflated) used to bypass the guard entirely (issue #12).
		// The supplied value is only a fallback for keywords with no explicit
		// bid yet. A clear is never an increase, so it skips the round trip.
		baseline, err := fetchCurrentKeywordBid(ctx, c, cid, adGroupID, criterionID)
		if err != nil {
			return WriteResult{}, toolError(tool, fmt.Errorf("could not verify the current bid for the bid-increase guard: %w", err))
		}
		if baseline <= 0 {
			baseline = args.CurrentBid
		}
		if err := checkBidIncrease(baseline, args.NewBid, cfg); err != nil {
			return WriteResult{}, toolError(tool, err)
		}
		update["cpcBidMicros"] = microsString(dollarsToMicros(args.NewBid))
		summary = fmt.Sprintf("Update keyword %s~%s CPC bid to %.2f", args.AdGroupID, args.CriterionID, args.NewBid)
	}
	// A clear masks cpcBidMicros while leaving it out of the update: Google
	// reads the masked-but-absent int64 as unset, which is what makes the
	// keyword inherit the ad group default rather than bid a literal zero.
	op := map[string]any{
		"adGroupCriterionOperation": map[string]any{
			"update":     update,
			"updateMask": "cpcBidMicros",
		},
	}
	return previewMutate(tool, cid, summary, []any{op})
}

// fetchCurrentKeywordBid looks up a keyword's current CPC bid in currency
// units. A result of 0 with a nil error means the keyword has no explicit bid
// (no baseline for the guard).
func fetchCurrentKeywordBid(ctx context.Context, c *Client, customerID, adGroupID, criterionID string) (float64, error) {
	if c == nil {
		return 0, nil
	}
	q := fmt.Sprintf("SELECT ad_group_criterion.cpc_bid_micros FROM ad_group_criterion WHERE ad_group.id = %s AND ad_group_criterion.criterion_id = %s", adGroupID, criterionID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var row struct {
		AdGroupCriterion struct {
			// REST serializes int64 as a JSON string; tolerate numbers too.
			CpcBidMicros any `json:"cpcBidMicros"`
		} `json:"adGroupCriterion"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return 0, nil
	}
	switch v := row.AdGroupCriterion.CpcBidMicros.(type) {
	case string:
		micros, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, nil
		}
		return float64(micros) / 1_000_000.0, nil
	case float64:
		return v / 1_000_000.0, nil
	default:
		return 0, nil
	}
}

// --- CLI front-end ---

var (
	portfolioArgs  PortfolioBiddingArgs
	keywordBidArgs UpdateKeywordBidArgs
)

var biddingCmd = &cobra.Command{
	Use:   "bidding",
	Short: "Manage bidding strategies and keyword bids",
}

var biddingPortfolioCmd = &cobra.Command{
	Use:   "create-strategy",
	Short: "Create a portfolio bidding strategy (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCreatePortfolioBidding(cmd.Context(), client, portfolioArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var biddingKeywordBidCmd = &cobra.Command{
	Use:   "set-keyword-bid",
	Short: "Update a keyword's CPC bid (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runUpdateKeywordBid(cmd.Context(), client, keywordBidArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	biddingPortfolioCmd.Flags().StringVar(&portfolioArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	biddingPortfolioCmd.Flags().StringVar(&portfolioArgs.Name, "name", "", "strategy name (required)")
	biddingPortfolioCmd.Flags().StringVar(&portfolioArgs.StrategyType, "type", "", "TARGET_CPA, TARGET_ROAS, or TARGET_IMPRESSION_SHARE (required)")
	biddingPortfolioCmd.Flags().Float64Var(&portfolioArgs.TargetCPA, "target-cpa", 0, "target CPA in currency units (TARGET_CPA)")
	biddingPortfolioCmd.Flags().Float64Var(&portfolioArgs.TargetROAS, "target-roas", 0, "target ROAS ratio (TARGET_ROAS)")
	biddingPortfolioCmd.Flags().StringVar(&portfolioArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = biddingPortfolioCmd.MarkFlagRequired("name")
	_ = biddingPortfolioCmd.MarkFlagRequired("type")

	biddingKeywordBidCmd.Flags().StringVar(&keywordBidArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	biddingKeywordBidCmd.Flags().StringVar(&keywordBidArgs.AdGroupID, "ad-group-id", "", "ad group ID (required)")
	biddingKeywordBidCmd.Flags().StringVar(&keywordBidArgs.CriterionID, "criterion-id", "", "keyword criterion ID (required)")
	biddingKeywordBidCmd.Flags().Float64Var(&keywordBidArgs.CurrentBid, "current-bid", 0, "current bid in currency units (for the safety check)")
	biddingKeywordBidCmd.Flags().Float64Var(&keywordBidArgs.NewBid, "new-bid", 0, "new bid in currency units (required unless --clear-bid)")
	biddingKeywordBidCmd.Flags().BoolVar(&keywordBidArgs.ClearBid, "clear-bid", false, "remove the keyword's own CPC bid so it falls back to the ad group default")
	biddingKeywordBidCmd.Flags().StringVar(&keywordBidArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = biddingKeywordBidCmd.MarkFlagRequired("ad-group-id")
	_ = biddingKeywordBidCmd.MarkFlagRequired("criterion-id")
	// Naming neither used to mean "clear the bid"; the intent is now stated.
	biddingKeywordBidCmd.MarkFlagsOneRequired("new-bid", "clear-bid")
	biddingKeywordBidCmd.MarkFlagsMutuallyExclusive("new-bid", "clear-bid")

	biddingCmd.AddCommand(biddingPortfolioCmd, biddingKeywordBidCmd)
}
