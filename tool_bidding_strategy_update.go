package main

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/spf13/cobra"
)

// This file updates a portfolio (shared) bidding strategy. The create half
// lives in tool_bidding.go.
//
// A portfolio strategy is the only place its target can live: update_campaign
// rejects target_cpa/target_roas beside portfolio_strategy_id, and
// resolveCampaignBiddingStrategy refuses a target-only update for an attached
// campaign — both because the target belongs to the shared resource, so that
// every attached campaign moves together when it changes. That promise needs a
// tool to keep it (issue #51).

// impressionShareLocations are the page positions TARGET_IMPRESSION_SHARE can
// bid toward.
var impressionShareLocations = map[string]bool{
	"ANYWHERE_ON_PAGE": true, "TOP_OF_PAGE": true, "ABSOLUTE_TOP_OF_PAGE": true,
}

// UpdatePortfolioBiddingArgs updates a portfolio (shared) bidding strategy's
// name and/or the target its type carries.
type UpdatePortfolioBiddingArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that OWNS the strategy; omit to use the configured default customer"`
	StrategyID string `json:"strategy_id" jsonschema:"ID (or resource name) of the portfolio bidding strategy to update"`
	Name       string `json:"name,omitempty" jsonschema:"a new name for the strategy"`
	// Each target applies to the strategy types that carry it; the strategy's
	// own type is resolved first and a mismatched target is refused.
	TargetCPA               float64 `json:"target_cpa,omitempty" jsonschema:"new target CPA in currency units (TARGET_CPA and MAXIMIZE_CONVERSIONS strategies)"`
	TargetROAS              float64 `json:"target_roas,omitempty" jsonschema:"new target ROAS as a ratio, e.g. 3.0 (TARGET_ROAS and MAXIMIZE_CONVERSION_VALUE strategies)"`
	ImpressionShareLocation string  `json:"impression_share_location,omitempty" jsonschema:"where to bid for impression share: ANYWHERE_ON_PAGE, TOP_OF_PAGE, or ABSOLUTE_TOP_OF_PAGE (TARGET_IMPRESSION_SHARE strategies)"`
	ImpressionSharePercent  float64 `json:"impression_share_percent,omitempty" jsonschema:"the share of impressions to target, as a percentage from 1 to 100 (TARGET_IMPRESSION_SHARE strategies)"`
	Confirm                 string  `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

// suppliedTargetArgs names every target argument the caller actually passed,
// in the wording portfolioTargetArg uses, so a mismatch can name both what was
// given and what the strategy takes.
func (a UpdatePortfolioBiddingArgs) suppliedTargetArgs() []string {
	var supplied []string
	if a.TargetCPA != 0 {
		supplied = append(supplied, "target_cpa")
	}
	if a.TargetROAS != 0 {
		supplied = append(supplied, "target_roas")
	}
	if strings.TrimSpace(a.ImpressionShareLocation) != "" || a.ImpressionSharePercent != 0 {
		supplied = append(supplied, "impression_share_location/impression_share_percent")
	}
	return supplied
}

// wantsTarget reports whether the arguments ask for a target change at all.
func (a UpdatePortfolioBiddingArgs) wantsTarget() bool {
	return len(a.suppliedTargetArgs()) > 0
}

// validatePortfolioBiddingUpdate rejects arguments that contradict each other
// or fall outside their range, before any API round trip.
func validatePortfolioBiddingUpdate(args UpdatePortfolioBiddingArgs) error {
	if args.Name == "" && !args.wantsTarget() {
		return fmt.Errorf("no changes specified — pass name to rename the strategy, or the target its type carries (target_cpa, target_roas, or impression_share_location/impression_share_percent)")
	}
	if err := validateBiddingTargetValues(args.TargetCPA, args.TargetROAS); err != nil {
		return err
	}
	if strings.TrimSpace(args.ImpressionShareLocation) != "" {
		loc := strings.ToUpper(strings.TrimSpace(args.ImpressionShareLocation))
		if !impressionShareLocations[loc] {
			return fmt.Errorf("unsupported impression_share_location %q — use ANYWHERE_ON_PAGE, TOP_OF_PAGE, or ABSOLUTE_TOP_OF_PAGE", args.ImpressionShareLocation)
		}
	}
	if args.ImpressionSharePercent != 0 && (args.ImpressionSharePercent < 1 || args.ImpressionSharePercent > 100) {
		return fmt.Errorf("impression_share_percent must be between 1 and 100, got %v", args.ImpressionSharePercent)
	}
	return nil
}

// portfolioTargetArg names the argument a strategy type takes its target in.
// An empty result means the type carries no target this tool can change.
func portfolioTargetArg(strategyType string) string {
	switch strategyType {
	case "TARGET_CPA", "MAXIMIZE_CONVERSIONS":
		return "target_cpa"
	case "TARGET_ROAS", "MAXIMIZE_CONVERSION_VALUE":
		return "target_roas"
	case "TARGET_IMPRESSION_SHARE":
		return "impression_share_location/impression_share_percent"
	default:
		return ""
	}
}

// impressionShareMicros converts a percentage (1-100) to the micros of a
// fraction that location_fraction_micros holds: 50% is 500000, not 50000000.
func impressionShareMicros(percent float64) int64 {
	return int64(math.Round(percent * 10_000))
}

// applyPortfolioTargetUpdate sets the target leaf belonging to the strategy's
// own type and records the touched leaves in mask. It returns a description of
// the change for the preview summary, empty when no target was asked for.
//
// A strategy's type is fixed after creation, so a target that belongs to a
// different type means the caller is holding the wrong strategy or the wrong
// argument; either way it fails here rather than at confirm. EVERY supplied
// target is checked, not just whether the matching one is present: a stray
// argument that rode along would otherwise be silently discarded, and a
// confirmed write would change less than the operator asked for — on a resource
// that moves every attached campaign at once.
func applyPortfolioTargetUpdate(strategy portfolioStrategyInfo, args UpdatePortfolioBiddingArgs, update map[string]any, mask *[]string) (string, error) {
	supplied := args.suppliedTargetArgs()
	if len(supplied) == 0 {
		return "", nil
	}
	want := portfolioTargetArg(strategy.Type)
	if want == "" {
		return "", fmt.Errorf("portfolio bidding strategy %s bids with %s, which carries no target this tool can change — pass name on its own to rename it", strategy.ID, strategy.Type)
	}
	if len(supplied) != 1 || supplied[0] != want {
		var wrong []string
		for _, arg := range supplied {
			if arg != want {
				wrong = append(wrong, arg)
			}
		}
		return "", fmt.Errorf("portfolio bidding strategy %s (%q) bids with %s, whose target is %s — %s does not apply to it, and a shared strategy's type cannot be changed after creation",
			strategy.ID, strategy.Name, strategy.Type, want, strings.Join(wrong, " and "))
	}
	switch strategy.Type {
	case "TARGET_CPA":
		update["targetCpa"] = map[string]any{"targetCpaMicros": microsString(dollarsToMicros(args.TargetCPA))}
		*mask = append(*mask, "targetCpa.targetCpaMicros")
		return fmt.Sprintf("target CPA %.2f", args.TargetCPA), nil
	case "MAXIMIZE_CONVERSIONS":
		update["maximizeConversions"] = map[string]any{"targetCpaMicros": microsString(dollarsToMicros(args.TargetCPA))}
		*mask = append(*mask, "maximizeConversions.targetCpaMicros")
		return fmt.Sprintf("target CPA %.2f", args.TargetCPA), nil
	case "TARGET_ROAS":
		update["targetRoas"] = map[string]any{"targetRoas": args.TargetROAS}
		*mask = append(*mask, "targetRoas.targetRoas")
		return fmt.Sprintf("target ROAS %v", args.TargetROAS), nil
	case "MAXIMIZE_CONVERSION_VALUE":
		update["maximizeConversionValue"] = map[string]any{"targetRoas": args.TargetROAS}
		*mask = append(*mask, "maximizeConversionValue.targetRoas")
		return fmt.Sprintf("target ROAS %v", args.TargetROAS), nil
	case "TARGET_IMPRESSION_SHARE":
		// Only the supplied side is masked: location and fraction are separate
		// leaves, and masking an omitted one would reset it.
		leaf := map[string]any{}
		var parts []string
		if location := strings.ToUpper(strings.TrimSpace(args.ImpressionShareLocation)); location != "" {
			leaf["location"] = location
			*mask = append(*mask, "targetImpressionShare.location")
			parts = append(parts, location)
		}
		if args.ImpressionSharePercent != 0 {
			leaf["locationFractionMicros"] = microsString(impressionShareMicros(args.ImpressionSharePercent))
			*mask = append(*mask, "targetImpressionShare.locationFractionMicros")
			parts = append(parts, fmt.Sprintf("%g%% of impressions", args.ImpressionSharePercent))
		}
		update["targetImpressionShare"] = leaf
		return "impression share target " + strings.Join(parts, ", "), nil
	default:
		// portfolioTargetArg returned a target for this type, so every type it
		// names must be handled above.
		return "", fmt.Errorf("portfolio bidding strategy %s bids with %s, which this tool cannot set a target on", strategy.ID, strategy.Type)
	}
}

func runUpdatePortfolioBidding(ctx context.Context, c *Client, args UpdatePortfolioBiddingArgs) (WriteResult, error) {
	const tool = "update_portfolio_bidding_strategy"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	if err := validatePortfolioBiddingUpdate(args); err != nil {
		return WriteResult{}, err
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	strategyID, err := parsePortfolioStrategyID("strategy_id", args.StrategyID)
	if err != nil {
		return WriteResult{}, err
	}
	// Resolved before anything is staged: the strategy's type decides which
	// target leaf is legal, and an ID that is not reachable from this customer
	// fails at preview rather than on the confirmed mutate.
	strategy, err := fetchPortfolioStrategy(ctx, c, cid, strategyID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	// accessible_bidding_strategy also covers strategies shared down from a
	// manager account. Those are attachable here but belong to the manager, and
	// only the owner can mutate one, so say which account to run against.
	if strategy.OwnerCustomerID != cid {
		return WriteResult{}, toolError(tool, fmt.Errorf("portfolio bidding strategy %s is owned by customer %s, not %s — a shared strategy can only be changed on the account that owns it; re-run with customer_id %s", strategyID, strategy.OwnerCustomerID, cid, strategy.OwnerCustomerID))
	}

	update := map[string]any{"resourceName": strategy.resourceName()}
	var mask []string
	var changes []string
	if args.Name != "" {
		update["name"] = args.Name
		mask = append(mask, "name")
		changes = append(changes, fmt.Sprintf("rename to %q", args.Name))
	}
	targetChange, err := applyPortfolioTargetUpdate(strategy, args, update, &mask)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if targetChange != "" {
		changes = append(changes, "set "+targetChange)
	}

	op := map[string]any{"biddingStrategyOperation": map[string]any{
		"update":     update,
		"updateMask": strings.Join(mask, ","),
	}}
	summary := fmt.Sprintf("Update portfolio bidding strategy %q (%s, ID %s): %s",
		strategy.Name, strategy.Type, strategy.ID, strings.Join(changes, " and "))
	if targetChange == "" {
		return previewMutate(tool, cid, summary, []any{op})
	}
	// A target change moves every campaign attached to the strategy at once —
	// the whole reason a portfolio exists — so it takes a second confirmation.
	// A rename changes no bidding behaviour and does not.
	return previewMutateDouble(tool, cid, summary+" — this moves every campaign attached to the strategy", []any{op})
}

// --- CLI front-end ---

var updatePortfolioArgs UpdatePortfolioBiddingArgs

var biddingUpdateStrategyCmd = &cobra.Command{
	Use:   "update-strategy",
	Short: "Update a portfolio bidding strategy's name or target (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runUpdatePortfolioBidding(cmd.Context(), client, updatePortfolioArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	f := biddingUpdateStrategyCmd.Flags()
	f.StringVar(&updatePortfolioArgs.CustomerID, "customer-id", "", "Google Ads customer ID that owns the strategy (falls back to the configured default)")
	f.StringVar(&updatePortfolioArgs.StrategyID, "strategy-id", "", "portfolio bidding strategy ID or resource name (required)")
	f.StringVar(&updatePortfolioArgs.Name, "name", "", "new strategy name")
	f.Float64Var(&updatePortfolioArgs.TargetCPA, "target-cpa", 0, "new target CPA in currency units")
	f.Float64Var(&updatePortfolioArgs.TargetROAS, "target-roas", 0, "new target ROAS ratio")
	f.StringVar(&updatePortfolioArgs.ImpressionShareLocation, "impression-share-location", "", "ANYWHERE_ON_PAGE, TOP_OF_PAGE, or ABSOLUTE_TOP_OF_PAGE")
	f.Float64Var(&updatePortfolioArgs.ImpressionSharePercent, "impression-share-percent", 0, "impression share to target, 1-100")
	f.StringVar(&updatePortfolioArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = biddingUpdateStrategyCmd.MarkFlagRequired("strategy-id")

	biddingCmd.AddCommand(biddingUpdateStrategyCmd)
}
