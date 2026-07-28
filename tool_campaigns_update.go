package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// This file updates a campaign's budget and bidding strategy, and can add
// geo/language targeting. A budget change targets the campaign's budget
// resource (a distinct ID), which is resolved from the API first.

// applyBiddingStrategyUpdate sets the bidding sub-field on a campaign update map
// and records the touched fields in mask. In v23 bidding_strategy_type is
// OUTPUT_ONLY, so the strategy is selected by setting the matching sub-field;
// unknown strategies and missing targets error at preview time rather than
// staging an op Google will reject at confirm (issue #8). Message fields with
// defined subfields cannot appear bare in a field mask, even when the message
// is empty; those strategy selections mask every mutable leaf instead.
func applyBiddingStrategyUpdate(campaign map[string]any, mask *[]string, strategy string, cpa, roas float64) error {
	if err := validateBiddingStrategyTargets(strategy, cpa, roas); err != nil {
		return err
	}
	switch strategy {
	case "MAXIMIZE_CONVERSIONS":
		mc := map[string]any{}
		if cpa != 0 {
			mc["targetCpaMicros"] = microsString(dollarsToMicros(cpa))
		}
		campaign["maximizeConversions"] = mc
		*mask = append(*mask, "maximizeConversions.targetCpaMicros")
	case "MAXIMIZE_CONVERSION_VALUE":
		mcv := map[string]any{}
		if roas != 0 {
			mcv["targetRoas"] = roas
			*mask = append(*mask, "maximizeConversionValue.targetRoas")
		} else {
			*mask = append(*mask,
				"maximizeConversionValue.targetRoas",
				"maximizeConversionValue.targetRoasTolerancePercentMillis",
			)
		}
		campaign["maximizeConversionValue"] = mcv
	case "TARGET_CPA":
		if cpa == 0 {
			return fmt.Errorf("TARGET_CPA requires target_cpa (currency units)")
		}
		campaign["targetCpa"] = map[string]any{"targetCpaMicros": microsString(dollarsToMicros(cpa))}
		*mask = append(*mask, "targetCpa.targetCpaMicros")
	case "TARGET_ROAS":
		if roas == 0 {
			return fmt.Errorf("TARGET_ROAS requires target_roas (a ratio, e.g. 3.5)")
		}
		campaign["targetRoas"] = map[string]any{"targetRoas": roas}
		*mask = append(*mask, "targetRoas.targetRoas")
	case "MANUAL_CPC":
		campaign["manualCpc"] = map[string]any{}
		*mask = append(*mask, "manualCpc.enhancedCpcEnabled")
	case "TARGET_SPEND", "MAXIMIZE_CLICKS":
		campaign["targetSpend"] = map[string]any{}
		*mask = append(*mask,
			"targetSpend.cpcBidCeilingMicros",
			"targetSpend.targetSpendMicros",
		)
	case "TARGET_IMPRESSION_SHARE":
		// v23 requires location + fraction (and optionally a CPC ceiling) —
		// an empty object previews fine and is rejected at confirm. Use
		// create_portfolio_bidding_strategy, which stages those fields.
		return fmt.Errorf("TARGET_IMPRESSION_SHARE cannot be set via update_campaign (it requires location/fraction/ceiling parameters) — create it with create_portfolio_bidding_strategy instead")
	case "PERCENT_CPC":
		campaign["percentCpc"] = map[string]any{}
		*mask = append(*mask,
			"percentCpc.cpcBidCeilingMicros",
			"percentCpc.enhancedCpcEnabled",
		)
	default:
		return fmt.Errorf("unsupported bidding strategy %q — use one of MAXIMIZE_CONVERSIONS, MAXIMIZE_CONVERSION_VALUE, TARGET_CPA, TARGET_ROAS, MANUAL_CPC, TARGET_SPEND/MAXIMIZE_CLICKS, PERCENT_CPC", strategy)
	}
	return nil
}

func validateBiddingStrategyTargets(strategy string, cpa, roas float64) error {
	if err := validateBiddingTargetValues(cpa, roas); err != nil {
		return err
	}
	if cpa != 0 && strategy != "TARGET_CPA" && strategy != "MAXIMIZE_CONVERSIONS" {
		return fmt.Errorf("target_cpa cannot be set with bidding strategy %s — use TARGET_CPA or MAXIMIZE_CONVERSIONS as bidding_strategy", strategy)
	}
	if roas != 0 && strategy != "TARGET_ROAS" && strategy != "MAXIMIZE_CONVERSION_VALUE" {
		return fmt.Errorf("target_roas cannot be set with bidding strategy %s — use TARGET_ROAS or MAXIMIZE_CONVERSION_VALUE as bidding_strategy", strategy)
	}
	return nil
}

func validateBiddingTargetValues(cpa, roas float64) error {
	if cpa < 0 {
		return fmt.Errorf("target_cpa must be positive (currency units), got %v", cpa)
	}
	if roas < 0 {
		return fmt.Errorf("target_roas must be positive (a ratio, e.g. 3.5), got %v", roas)
	}
	if cpa != 0 && roas != 0 {
		return fmt.Errorf("target_cpa and target_roas cannot be updated together — choose the target used by bidding_strategy")
	}
	return nil
}

type campaignBiddingStrategyState struct {
	Type      string
	Portfolio string
}

func fetchCampaignBiddingStrategyState(ctx context.Context, c *Client, customerID, campaignID string) (campaignBiddingStrategyState, error) {
	if c == nil {
		return campaignBiddingStrategyState{}, fmt.Errorf("could not resolve the bidding strategy for campaign %s: Google Ads client is unavailable", campaignID)
	}
	q := fmt.Sprintf("SELECT campaign.bidding_strategy_type, campaign.bidding_strategy FROM campaign WHERE campaign.id = %s", campaignID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return campaignBiddingStrategyState{}, fmt.Errorf("could not resolve the bidding strategy for campaign %s: %w", campaignID, err)
	}
	if len(rows) == 0 {
		return campaignBiddingStrategyState{}, fmt.Errorf("could not resolve the bidding strategy for campaign %s — the campaign may not exist", campaignID)
	}
	var row struct {
		Campaign struct {
			BiddingStrategyType string `json:"biddingStrategyType"`
			BiddingStrategy     string `json:"biddingStrategy"`
		} `json:"campaign"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return campaignBiddingStrategyState{}, fmt.Errorf("could not decode the bidding strategy for campaign %s: %w", campaignID, err)
	}
	if row.Campaign.BiddingStrategyType == "" {
		return campaignBiddingStrategyState{}, fmt.Errorf("could not resolve the bidding strategy for campaign %s — Google Ads returned no bidding strategy type", campaignID)
	}
	return campaignBiddingStrategyState{
		Type:      row.Campaign.BiddingStrategyType,
		Portfolio: row.Campaign.BiddingStrategy,
	}, nil
}

// resolveCampaignBiddingStrategy returns the campaign's current standard
// bidding strategy type. Target-only updates must preserve the active bidding
// strategy oneof, so they resolve this before staging a mutation. Portfolio
// strategies are rejected because their targets belong to the shared bidding
// strategy resource rather than the campaign.
func resolveCampaignBiddingStrategy(ctx context.Context, c *Client, customerID, campaignID string) (string, error) {
	state, err := fetchCampaignBiddingStrategyState(ctx, c, customerID, campaignID)
	if err != nil {
		return "", err
	}
	if state.Portfolio != "" {
		return "", fmt.Errorf("campaign %s uses portfolio bidding strategy %s — target-only campaign updates are not supported for shared strategies; update the portfolio strategy or specify bidding_strategy to switch to a standard strategy", campaignID, state.Portfolio)
	}
	return state.Type, nil
}

func biddingStrategyAllowsEmptyUpdate(strategy string) bool {
	switch strategy {
	case "MAXIMIZE_CONVERSIONS", "MAXIMIZE_CONVERSION_VALUE", "MANUAL_CPC", "TARGET_SPEND", "MAXIMIZE_CLICKS", "PERCENT_CPC":
		return true
	default:
		return false
	}
}

func canonicalBiddingStrategy(strategy string) string {
	if strategy == "MAXIMIZE_CLICKS" {
		return "TARGET_SPEND"
	}
	return strategy
}

// resolveCampaignBudgetResource looks up a campaign's budget resource name and
// current daily amount. A campaign budget has its own ID distinct from the
// campaign ID, so a budget update must target the real budget resource. The
// amount is best-effort (0 when not returned) and feeds the >50%-increase
// double-confirm heuristic.
func resolveCampaignBudgetResource(ctx context.Context, c *Client, customerID, campaignID string) (string, int64, error) {
	q := fmt.Sprintf("SELECT campaign.campaign_budget, campaign_budget.amount_micros FROM campaign WHERE campaign.id = %s", campaignID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return "", 0, err
	}
	if len(rows) > 0 {
		var row struct {
			Campaign struct {
				CampaignBudget string `json:"campaignBudget"`
			} `json:"campaign"`
			CampaignBudget struct {
				AmountMicros any `json:"amountMicros"`
			} `json:"campaignBudget"`
		}
		if json.Unmarshal(rows[0], &row) == nil && row.Campaign.CampaignBudget != "" {
			var amount int64
			switch v := row.CampaignBudget.AmountMicros.(type) {
			case string:
				amount, _ = strconv.ParseInt(v, 10, 64)
			case float64:
				amount = int64(v)
			}
			return row.Campaign.CampaignBudget, amount, nil
		}
	}
	return "", 0, fmt.Errorf("could not resolve a campaign budget for campaign %s — the campaign may not exist or has no associated budget", campaignID)
}

// UpdateCampaignArgs updates an existing campaign's settings. Only the provided
// fields change; at least one change must be specified.
type UpdateCampaignArgs struct {
	CustomerID      string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID      string   `json:"campaign_id" jsonschema:"the campaign ID to update"`
	BiddingStrategy string   `json:"bidding_strategy,omitempty" jsonschema:"new bidding strategy, e.g. MAXIMIZE_CONVERSIONS"`
	TargetCPA       float64  `json:"target_cpa,omitempty" jsonschema:"target CPA in currency units"`
	TargetROAS      float64  `json:"target_roas,omitempty" jsonschema:"target ROAS ratio"`
	DailyBudget     float64  `json:"daily_budget,omitempty" jsonschema:"new daily budget in currency units (capped by the budget guard)"`
	GeoTargetIDs    []string `json:"geo_target_ids,omitempty" jsonschema:"geo target constant IDs to add"`
	LanguageIDs     []string `json:"language_ids,omitempty" jsonschema:"language constant IDs to add"`
	Confirm         string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runUpdateCampaign(ctx context.Context, c *Client, args UpdateCampaignArgs) (WriteResult, error) {
	const tool = "update_campaign"
	// Blocked-op check runs before the confirm branch so an operation blocked
	// between preview and confirm cannot still be applied with its token.
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	campaignID, err := numericID("campaign_id", args.CampaignID)
	if err != nil {
		return WriteResult{}, err
	}
	campaignResource := fmt.Sprintf("customers/%s/campaigns/%s", cid, campaignID)
	if err := validateBiddingTargetValues(args.TargetCPA, args.TargetROAS); err != nil {
		return WriteResult{}, err
	}
	if args.BiddingStrategy != "" {
		if err := validateBiddingStrategyTargets(args.BiddingStrategy, args.TargetCPA, args.TargetROAS); err != nil {
			return WriteResult{}, err
		}
	}
	var ops []any
	doubleConfirm := false

	// Budget update — resolve the real budget resource first.
	if args.DailyBudget != 0 {
		if args.DailyBudget < 0 {
			return WriteResult{}, fmt.Errorf("daily_budget must be positive (currency units), got %v", args.DailyBudget)
		}
		if err := checkBudgetCap(args.DailyBudget, loadSafetyConfig()); err != nil {
			return WriteResult{}, toolError(tool, err)
		}
		budgetResource, currentMicros, err := resolveCampaignBudgetResource(ctx, c, cid, campaignID)
		if err != nil {
			return WriteResult{}, toolError(tool, err)
		}
		// Budget increases over 50% take a second confirmation (issue #12).
		if currentMicros > 0 {
			cur := float64(currentMicros) / 1_000_000.0
			doubleConfirm = requiresDoubleConfirmation(tool, &cur, &args.DailyBudget)
		}
		ops = append(ops, map[string]any{"campaignBudgetOperation": map[string]any{
			"update":     map[string]any{"resourceName": budgetResource, "amountMicros": microsString(dollarsToMicros(args.DailyBudget))},
			"updateMask": "amountMicros",
		}})
	}

	// Bidding strategy update. A target can stand alone when the campaign
	// already uses a compatible strategy; resolve that strategy so setting the
	// target leaf preserves the current bidding oneof.
	strategy := args.BiddingStrategy
	if strategy == "" && (args.TargetCPA != 0 || args.TargetROAS != 0) {
		strategy, err = resolveCampaignBiddingStrategy(ctx, c, cid, campaignID)
		if err != nil {
			return WriteResult{}, toolError(tool, err)
		}
	}
	if strategy != "" && args.TargetCPA == 0 && args.TargetROAS == 0 && biddingStrategyAllowsEmptyUpdate(strategy) {
		current, err := fetchCampaignBiddingStrategyState(ctx, c, cid, campaignID)
		if err != nil {
			return WriteResult{}, toolError(tool, err)
		}
		// Masking an omitted leaf clears it. A redundant strategy-only update
		// must therefore be a no-op, or it could silently remove an existing
		// target, bid ceiling, or enhanced-CPC setting. A portfolio resource is
		// not redundant: the explicit strategy selects a standard strategy.
		if current.Portfolio == "" && current.Type == canonicalBiddingStrategy(strategy) {
			strategy = ""
		}
	}
	if strategy != "" {
		update := map[string]any{"resourceName": campaignResource}
		var mask []string
		if err := applyBiddingStrategyUpdate(update, &mask, strategy, args.TargetCPA, args.TargetROAS); err != nil {
			return WriteResult{}, err
		}
		ops = append(ops, map[string]any{"campaignOperation": map[string]any{"update": update, "updateMask": strings.Join(mask, ",")}})
	}

	// Geo and language additions.
	if err := numericIDs("geo_target_id", args.GeoTargetIDs); err != nil {
		return WriteResult{}, err
	}
	if err := numericIDs("language_id", args.LanguageIDs); err != nil {
		return WriteResult{}, err
	}
	for _, geoID := range args.GeoTargetIDs {
		ops = append(ops, campaignLocationCriterion(campaignResource, geoID))
	}
	for _, langID := range args.LanguageIDs {
		ops = append(ops, campaignLanguageCriterion(campaignResource, langID))
	}

	if len(ops) == 0 {
		return WriteResult{}, fmt.Errorf("no changes specified for campaign update")
	}
	summary := fmt.Sprintf("Update campaign %s (%d operation(s))", args.CampaignID, len(ops))
	if doubleConfirm {
		return previewMutateDouble(tool, cid, summary, ops)
	}
	return previewMutate(tool, cid, summary, ops)
}

// --- CLI front-end ---

var updateCampaignArgs UpdateCampaignArgs

var campaignUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update a campaign's budget, bidding, or targeting (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runUpdateCampaign(cmd.Context(), client, updateCampaignArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	f := campaignUpdateCmd.Flags()
	f.StringVar(&updateCampaignArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&updateCampaignArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	f.StringVar(&updateCampaignArgs.BiddingStrategy, "bidding-strategy", "", "new bidding strategy")
	f.Float64Var(&updateCampaignArgs.TargetCPA, "target-cpa", 0, "target CPA in currency units")
	f.Float64Var(&updateCampaignArgs.TargetROAS, "target-roas", 0, "target ROAS ratio")
	f.Float64Var(&updateCampaignArgs.DailyBudget, "daily-budget", 0, "new daily budget in currency units")
	f.StringArrayVar(&updateCampaignArgs.GeoTargetIDs, "geo-target-id", nil, "geo target constant ID to add (repeatable)")
	f.StringArrayVar(&updateCampaignArgs.LanguageIDs, "language-id", nil, "language constant ID to add (repeatable)")
	f.StringVar(&updateCampaignArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = campaignUpdateCmd.MarkFlagRequired("campaign-id")

	campaignCmd.AddCommand(campaignUpdateCmd)
}
