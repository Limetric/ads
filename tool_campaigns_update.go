package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// This file updates a campaign's budget, bidding strategy, and location
// options, and can add geo/language targeting. A budget change targets the
// campaign's budget resource (a distinct ID), which is resolved from the API
// first.

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

// clearTargetRequest describes a request to remove a campaign-level bidding
// target. Only the two "maximize" strategies carry an optional target, so each
// clear is valid for exactly one strategy — TARGET_CPA and TARGET_ROAS require
// theirs, and a campaign on one of those switches strategy instead of clearing.
type clearTargetRequest struct {
	flag     string // argument name, for error messages
	label    string // the target's human name, for the preview summary
	strategy string // the only bidding strategy this clear applies to
	field    string // the campaign sub-message holding the target
	mask     string // the single leaf the clear masks
}

// clearTargetFor returns the clear the arguments ask for, or nil for none.
func clearTargetFor(args UpdateCampaignArgs) *clearTargetRequest {
	switch {
	case args.ClearTargetCPA:
		return &clearTargetRequest{
			flag: "clear_target_cpa", label: "target CPA", strategy: "MAXIMIZE_CONVERSIONS",
			field: "maximizeConversions", mask: "maximizeConversions.targetCpaMicros",
		}
	case args.ClearTargetROAS:
		return &clearTargetRequest{
			flag: "clear_target_roas", label: "target ROAS", strategy: "MAXIMIZE_CONVERSION_VALUE",
			field: "maximizeConversionValue", mask: "maximizeConversionValue.targetRoas",
		}
	default:
		return nil
	}
}

// validateClearTargetArgs rejects clear requests that contradict the rest of the
// update, before any API round trip.
func validateClearTargetArgs(args UpdateCampaignArgs) error {
	if args.ClearTargetCPA && args.ClearTargetROAS {
		return fmt.Errorf("clear_target_cpa and clear_target_roas cannot be set together — a campaign holds one bidding strategy, so only one of the two targets exists")
	}
	clearTarget := clearTargetFor(args)
	if clearTarget == nil {
		return nil
	}
	if args.TargetCPA != 0 || args.TargetROAS != 0 {
		return fmt.Errorf("%s cannot be combined with target_cpa/target_roas — pass %s alone to remove the campaign's target, or a target value alone to change it", clearTarget.flag, clearTarget.flag)
	}
	if args.PortfolioStrategyID != "" {
		return fmt.Errorf("%s cannot be set with portfolio_strategy_id — a portfolio strategy's targets belong to the shared strategy; change them there so every attached campaign moves together", clearTarget.flag)
	}
	if args.BiddingStrategy != "" && canonicalBiddingStrategy(args.BiddingStrategy) != clearTarget.strategy {
		return fmt.Errorf("%s applies only to %s, but bidding_strategy is %s, which has no optional target to remove", clearTarget.flag, clearTarget.strategy, args.BiddingStrategy)
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

// portfolioStrategyInfo describes a portfolio (shared) bidding strategy the
// campaign's account can attach to — either one it owns or one shared down from
// its manager, which is why the owner's customer ID is part of the answer: the
// resource name a campaign links to carries the *owner's* ID, not the
// campaign's.
type portfolioStrategyInfo struct {
	ID              string
	Name            string
	Type            string
	OwnerCustomerID string
}

// resourceName is the value campaign.bidding_strategy takes.
func (p portfolioStrategyInfo) resourceName() string {
	return fmt.Sprintf("customers/%s/biddingStrategies/%s", p.OwnerCustomerID, p.ID)
}

// parsePortfolioStrategyID accepts either a plain numeric strategy ID or the
// full resource name create_portfolio_bidding_strategy hands back in
// resource_names, so an agent can pass that value straight through. field names
// the argument it came from, for the error message.
func parsePortfolioStrategyID(field, value string) (string, error) {
	id := strings.TrimSpace(value)
	if strings.Contains(id, "/") {
		if !strings.Contains(id, "/biddingStrategies/") {
			return "", fmt.Errorf("%s %q is not a bidding strategy resource name — pass the numeric ID or customers/<customer_id>/biddingStrategies/<id>", field, value)
		}
		id = id[strings.LastIndex(id, "/")+1:]
	}
	return numericID(field, id)
}

// int64String renders a REST int64 field — serialized as a JSON string, though
// a bare number is tolerated — as a plain decimal string.
func int64String(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}

// fetchPortfolioStrategy resolves a portfolio strategy ID that the given
// customer can actually use. accessible_bidding_strategy (rather than
// bidding_strategy) is queried because a manager account's shared strategies
// are attachable by its child accounts but do not appear in the child's own
// bidding_strategy list.
func fetchPortfolioStrategy(ctx context.Context, c *Client, customerID, strategyID string) (portfolioStrategyInfo, error) {
	if c == nil {
		return portfolioStrategyInfo{}, fmt.Errorf("could not resolve portfolio bidding strategy %s: Google Ads client is unavailable", strategyID)
	}
	q := fmt.Sprintf("SELECT accessible_bidding_strategy.id, accessible_bidding_strategy.name, accessible_bidding_strategy.type, accessible_bidding_strategy.owner_customer_id FROM accessible_bidding_strategy WHERE accessible_bidding_strategy.id = %s", strategyID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return portfolioStrategyInfo{}, fmt.Errorf("could not resolve portfolio bidding strategy %s: %w", strategyID, err)
	}
	if len(rows) == 0 {
		return portfolioStrategyInfo{}, fmt.Errorf("no portfolio bidding strategy with ID %s is accessible from customer %s — create one with create_portfolio_bidding_strategy, or list the existing ones with: ads google search --query \"SELECT accessible_bidding_strategy.id, accessible_bidding_strategy.name, accessible_bidding_strategy.type FROM accessible_bidding_strategy\"", strategyID, customerID)
	}
	var row struct {
		AccessibleBiddingStrategy struct {
			ID              any    `json:"id"`
			Name            string `json:"name"`
			Type            string `json:"type"`
			OwnerCustomerID any    `json:"ownerCustomerId"`
		} `json:"accessibleBiddingStrategy"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return portfolioStrategyInfo{}, fmt.Errorf("could not decode portfolio bidding strategy %s: %w", strategyID, err)
	}
	owner := int64String(row.AccessibleBiddingStrategy.OwnerCustomerID)
	if owner == "" {
		// Older responses may omit the owner; the strategy is then the
		// customer's own.
		owner = customerID
	}
	return portfolioStrategyInfo{
		ID:              strategyID,
		Name:            row.AccessibleBiddingStrategy.Name,
		Type:            row.AccessibleBiddingStrategy.Type,
		OwnerCustomerID: owner,
	}, nil
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

// Campaign run dates live in campaign.start_date_time / end_date_time, which
// take "yyyy-MM-dd HH:mm:ss" in the customer's time zone. v23 has no plain
// start_date/end_date field. Google's own instruction for a whole-day boundary
// is to set the time component to 00:00:00 at the start and 23:59:59 at the
// end, so a bare date supplied here is completed to the right end of its day.
const (
	campaignDayStart = " 00:00:00"
	campaignDayEnd   = " 23:59:59"
)

// parseCampaignDate validates a campaign start/end boundary and returns it in
// the "yyyy-MM-dd HH:mm:ss" form the API takes. A bare YYYY-MM-DD is completed
// with dayTime, the whole-day boundary for that end of the range; an explicit
// time is passed through, because minute granularity is real for the campaign
// types that support it. A typo caught here costs no API round trip, and
// time.Parse rejects the almost-right values a length check would let through
// (2026-13-45).
func parseCampaignDate(field, value, dayTime string) (string, error) {
	v := strings.TrimSpace(value)
	// time.Parse accepts non-canonical components (2026-1-5), which would reach
	// the API in a shape it does not take, so the round trip has to match.
	if t, err := time.Parse(time.DateTime, v); err == nil && t.Format(time.DateTime) == v {
		return v, nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil && t.Format(time.DateOnly) == v {
		return v + dayTime, nil
	}
	return "", fmt.Errorf("%s must be YYYY-MM-DD, or YYYY-MM-DD HH:MM:SS for a campaign type that supports minute granularity, got %q", field, value)
}

// applyCampaignScheduleUpdate sets the campaign name and run dates on an update
// map, recording each touched leaf in mask and returning a description of each
// change for the preview summary. Only supplied fields are masked: masking an
// omitted leaf clears it, so a rename must not also wipe the dates.
func applyCampaignScheduleUpdate(args UpdateCampaignArgs, update map[string]any, mask *[]string) ([]string, error) {
	if args.ClearEndDate && args.EndDate != "" {
		return nil, fmt.Errorf("clear_end_date cannot be combined with end_date — pass clear_end_date alone to let the campaign run indefinitely, or end_date alone to move the finish line")
	}
	var changes []string
	if args.Name != "" {
		update["name"] = args.Name
		*mask = append(*mask, "name")
		changes = append(changes, fmt.Sprintf("rename to %q", args.Name))
	}
	var start, end string
	if args.StartDate != "" {
		parsed, err := parseCampaignDate("start_date", args.StartDate, campaignDayStart)
		if err != nil {
			return nil, err
		}
		start = parsed
		update["startDateTime"] = start
		*mask = append(*mask, "startDateTime")
		changes = append(changes, "start "+start)
	}
	switch {
	case args.EndDate != "":
		parsed, err := parseCampaignDate("end_date", args.EndDate, campaignDayEnd)
		if err != nil {
			return nil, err
		}
		end = parsed
		update["endDateTime"] = end
		*mask = append(*mask, "endDateTime")
		changes = append(changes, "end "+end)
	case args.ClearEndDate:
		// "To set an existing campaign to run indefinitely, clear this field" —
		// so the leaf is masked and left out of the update, the same shape
		// every other clear here uses.
		*mask = append(*mask, "endDateTime")
		changes = append(changes, "no end date")
	}
	// Both are zero-padded "yyyy-MM-dd HH:mm:ss", so they compare lexically.
	if start != "" && end != "" && end < start {
		return nil, fmt.Errorf("end_date %s is before start_date %s — a campaign cannot finish before it begins", end, start)
	}
	return changes, nil
}

// UpdateCampaignArgs updates an existing campaign's settings. Only the provided
// fields change; at least one change must be specified.
type UpdateCampaignArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string `json:"campaign_id" jsonschema:"the campaign ID to update"`
	// Name and the run dates were settable at create time (name) or nowhere at
	// all (dates); an end date is how a campaign is wound down on a schedule
	// rather than by someone being present at the right moment (issue #54).
	Name string `json:"name,omitempty" jsonschema:"a new name for the campaign"`
	// The dates set campaign.start_date_time / end_date_time. A bare date is
	// completed to the whole-day boundary for its end of the range.
	StartDate       string `json:"start_date,omitempty" jsonschema:"the campaign's first day as YYYY-MM-DD, or YYYY-MM-DD HH:MM:SS where the campaign type supports minute granularity (Google rejects a change once the campaign has started)"`
	EndDate         string `json:"end_date,omitempty" jsonschema:"the campaign's last day as YYYY-MM-DD, or YYYY-MM-DD HH:MM:SS where the campaign type supports minute granularity"`
	ClearEndDate    bool   `json:"clear_end_date,omitempty" jsonschema:"remove the campaign's end date so it runs indefinitely; cannot be combined with end_date"`
	BiddingStrategy string `json:"bidding_strategy,omitempty" jsonschema:"new standard (campaign-level) bidding strategy, e.g. MAXIMIZE_CONVERSIONS; mutually exclusive with portfolio_strategy_id"`
	// PortfolioStrategyID attaches the campaign to a shared bidding strategy so
	// several campaigns pool their conversion volume into one learning set.
	PortfolioStrategyID string  `json:"portfolio_strategy_id,omitempty" jsonschema:"ID (or resource name) of a portfolio bidding strategy to attach this campaign to; its targets live on the shared strategy, so do not pass target_cpa/target_roas with it"`
	TargetCPA           float64 `json:"target_cpa,omitempty" jsonschema:"target CPA in currency units"`
	TargetROAS          float64 `json:"target_roas,omitempty" jsonschema:"target ROAS ratio"`
	// The clear flags remove an optional target so the campaign bids on the
	// bare strategy. Omitting target_cpa/target_roas cannot express this: an
	// omitted value means "leave it alone".
	ClearTargetCPA  bool     `json:"clear_target_cpa,omitempty" jsonschema:"remove the campaign's target CPA, leaving pure MAXIMIZE_CONVERSIONS; only valid for a MAXIMIZE_CONVERSIONS campaign and cannot be combined with target_cpa"`
	ClearTargetROAS bool     `json:"clear_target_roas,omitempty" jsonschema:"remove the campaign's target ROAS, leaving pure MAXIMIZE_CONVERSION_VALUE; only valid for a MAXIMIZE_CONVERSION_VALUE campaign and cannot be combined with target_roas"`
	DailyBudget     float64  `json:"daily_budget,omitempty" jsonschema:"new daily budget in currency units (capped by the budget guard)"`
	GeoTargetIDs    []string `json:"geo_target_ids,omitempty" jsonschema:"geo target constant IDs to add as targeted locations"`
	// ExcludeGeoTargetIDs adds excluded locations — the same criterion with
	// negative set. negative_geo_target_type configures how they match.
	ExcludeGeoTargetIDs []string `json:"exclude_geo_target_ids,omitempty" jsonschema:"geo target constant IDs to add as EXCLUDED locations; how they match is set by negative_geo_target_type"`
	LanguageIDs         []string `json:"language_ids,omitempty" jsonschema:"language constant IDs to add"`
	// Location options — how targeted/excluded locations are matched. Each
	// side is left untouched when omitted.
	PositiveGeoTargetType string `json:"positive_geo_target_type,omitempty" jsonschema:"how targeted locations are matched: PRESENCE_OR_INTEREST or PRESENCE for people in the location only"`
	NegativeGeoTargetType string `json:"negative_geo_target_type,omitempty" jsonschema:"how excluded locations are matched: PRESENCE (recommended) or PRESENCE_OR_INTEREST, which most campaign types no longer accept"`
	// Dynamic Search Ads. Google requires the domain and the language
	// together, so the setting is written as one whole: passing them also
	// writes use_supplied_urls_only, which is false unless asked for.
	DSADomain              string `json:"dsa_domain,omitempty" jsonschema:"set the campaign's Dynamic Search Ads domain, e.g. example.com; requires dsa_language_code and rewrites the whole setting"`
	DSALanguageCode        string `json:"dsa_language_code,omitempty" jsonschema:"the language of the DSA domain, e.g. en; required with dsa_domain"`
	DSAUseSuppliedURLsOnly bool   `json:"dsa_use_supplied_urls_only,omitempty" jsonschema:"serve only URLs supplied by page feeds rather than Google's crawl of the domain; written whenever dsa_domain is set, so pass it every time it should stay on"`
	Confirm                string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
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
	if err := validateClearTargetArgs(args); err != nil {
		return WriteResult{}, err
	}
	clearTarget := clearTargetFor(args)
	// A portfolio strategy carries its own type and targets on the shared
	// resource, so it cannot be combined with a campaign-level strategy or a
	// campaign-level target.
	portfolioID := ""
	if args.PortfolioStrategyID != "" {
		if args.BiddingStrategy != "" {
			return WriteResult{}, fmt.Errorf("bidding_strategy and portfolio_strategy_id cannot be set together — a portfolio strategy carries its own type; pass portfolio_strategy_id alone to attach, or bidding_strategy alone to move back to a standard strategy")
		}
		if args.TargetCPA != 0 || args.TargetROAS != 0 {
			return WriteResult{}, fmt.Errorf("target_cpa/target_roas cannot be set with portfolio_strategy_id — a portfolio strategy's targets belong to the shared strategy; change them there so every attached campaign moves together")
		}
		portfolioID, err = parsePortfolioStrategyID("portfolio_strategy_id", args.PortfolioStrategyID)
		if err != nil {
			return WriteResult{}, err
		}
	}
	// Validated before any lookup so a typo costs no API round trip.
	geoSetting, geoMask, err := geoTargetTypeSetting(args.PositiveGeoTargetType, args.NegativeGeoTargetType)
	if err != nil {
		return WriteResult{}, err
	}
	// A campaign that was created without a dynamic search ads setting can be
	// given one here — the only way back for a Search campaign that should
	// have been a DSA campaign (issue #60). The ad groups still have to be
	// dynamic, and their type is fixed at creation, so an existing standard ad
	// group cannot be converted along with the campaign.
	dsaSetting, dsaMask, err := dynamicSearchAdsSetting(args.DSADomain, args.DSALanguageCode, args.DSAUseSuppliedURLsOnly)
	if err != nil {
		return WriteResult{}, err
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
	if strategy == "" && (args.TargetCPA != 0 || args.TargetROAS != 0 || clearTarget != nil) {
		strategy, err = resolveCampaignBiddingStrategy(ctx, c, cid, campaignID)
		if err != nil {
			return WriteResult{}, toolError(tool, err)
		}
	}
	if clearTarget != nil && strategy != clearTarget.strategy {
		return WriteResult{}, toolError(tool, fmt.Errorf("campaign %s bids with %s, which has no optional %s to remove — %s applies to %s; pass bidding_strategy %s to move the campaign onto it", args.CampaignID, strategy, clearTarget.label, clearTarget.flag, clearTarget.strategy, clearTarget.strategy))
	}
	// A clear wants exactly the empty-message update the redundancy check below
	// suppresses, so it never takes that branch.
	if clearTarget == nil && strategy != "" && args.TargetCPA == 0 && args.TargetROAS == 0 && biddingStrategyAllowsEmptyUpdate(strategy) {
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
	// Bidding and location options both live on the campaign resource, so they
	// share one operation rather than staging two updates of the same resource
	// in a single batch.
	update := map[string]any{"resourceName": campaignResource}
	var mask []string
	var portfolio portfolioStrategyInfo
	switch {
	case clearTarget != nil:
		// A clear masks the target leaf and nothing else. Reusing the
		// strategy-selection mask would also blank the leaves that merely
		// travel with the strategy: MAXIMIZE_CONVERSION_VALUE's
		// target_roas_tolerance_percent_millis is a separate campaign-level
		// setting, and the operator asked only for the target. Masking one leaf
		// still selects the strategy's oneof member, so the campaign keeps
		// bidding with it.
		update[clearTarget.field] = map[string]any{}
		mask = append(mask, clearTarget.mask)
	case strategy != "":
		if err := applyBiddingStrategyUpdate(update, &mask, strategy, args.TargetCPA, args.TargetROAS); err != nil {
			return WriteResult{}, err
		}
	}
	if portfolioID != "" {
		// Resolved rather than assembled from the campaign's customer ID: a
		// manager-owned strategy's resource name carries the manager's ID, and
		// an ID that is not attachable here fails at preview instead of on the
		// confirmed mutate.
		portfolio, err = fetchPortfolioStrategy(ctx, c, cid, portfolioID)
		if err != nil {
			return WriteResult{}, toolError(tool, err)
		}
		// biddingStrategy is a member of the campaign_bidding_strategy union, so
		// setting it clears whatever standard strategy the campaign held (and
		// setting a standard strategy later detaches the portfolio the same way).
		update["biddingStrategy"] = portfolio.resourceName()
		mask = append(mask, "biddingStrategy")
	}
	if geoSetting != nil {
		update["geoTargetTypeSetting"] = geoSetting
		mask = append(mask, geoMask...)
	}
	if dsaSetting != nil {
		update["dynamicSearchAdsSetting"] = dsaSetting
		mask = append(mask, dsaMask...)
	}
	changes, err := applyCampaignScheduleUpdate(args, update, &mask)
	if err != nil {
		return WriteResult{}, err
	}
	if dsa := dsaCampaignSummary(dsaSetting); dsa != "" {
		changes = append(changes, dsa)
	}
	if len(mask) > 0 {
		ops = append(ops, map[string]any{"campaignOperation": map[string]any{"update": update, "updateMask": strings.Join(mask, ",")}})
	}

	// Geo and language additions.
	if err := validateGeoTargetSelection(args.GeoTargetIDs, args.ExcludeGeoTargetIDs); err != nil {
		return WriteResult{}, err
	}
	if err := numericIDs("language_id", args.LanguageIDs); err != nil {
		return WriteResult{}, err
	}
	for _, geoID := range args.GeoTargetIDs {
		ops = append(ops, campaignLocationCriterion(campaignResource, geoID, false))
	}
	for _, geoID := range args.ExcludeGeoTargetIDs {
		ops = append(ops, campaignLocationCriterion(campaignResource, geoID, true))
	}
	for _, langID := range args.LanguageIDs {
		ops = append(ops, campaignLanguageCriterion(campaignResource, langID))
	}

	if len(ops) == 0 {
		return WriteResult{}, fmt.Errorf("no changes specified for campaign update")
	}
	summary := fmt.Sprintf("Update campaign %s (%d operation(s))", args.CampaignID, len(ops))
	if len(changes) > 0 {
		summary = fmt.Sprintf("Update campaign %s: %s (%d operation(s))", args.CampaignID, strings.Join(changes, ", "), len(ops))
	}
	if clearTarget != nil {
		summary = fmt.Sprintf("Remove the %s from campaign %s, leaving it on %s with no target", clearTarget.label, args.CampaignID, clearTarget.strategy)
		summary += otherChangesSuffix(changes, len(ops))
	}
	if portfolioID != "" {
		summary = fmt.Sprintf("Attach campaign %s to portfolio bidding strategy %q (%s, ID %s)", args.CampaignID, portfolio.Name, portfolio.Type, portfolio.ID)
		summary += otherChangesSuffix(changes, len(ops))
	}
	if doubleConfirm {
		return previewMutateDouble(tool, cid, summary, ops)
	}
	return previewMutate(tool, cid, summary, ops)
}

// otherChangesSuffix names what else a summary that leads with one headline
// change is carrying. The operation count alone cannot say: bidding, location
// options, run dates, and the dynamic search ads setting all write into the
// same campaign operation, so a change travelling with a target clear or a
// portfolio attachment would be staged without ever appearing in the preview
// it is confirmed from.
func otherChangesSuffix(changes []string, ops int) string {
	var parts []string
	if len(changes) > 0 {
		parts = append(parts, strings.Join(changes, ", "))
	}
	if ops > 1 {
		parts = append(parts, fmt.Sprintf("%d other change(s)", ops-1))
	}
	if len(parts) == 0 {
		return ""
	}
	return " and " + strings.Join(parts, ", ")
}

// --- CLI front-end ---

var updateCampaignArgs UpdateCampaignArgs

var campaignUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update a campaign's budget, bidding, targeting, or location options (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
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
	f.StringVar(&updateCampaignArgs.Name, "name", "", "new campaign name")
	f.StringVar(&updateCampaignArgs.StartDate, "start-date", "", "campaign start, YYYY-MM-DD (or YYYY-MM-DD HH:MM:SS)")
	f.StringVar(&updateCampaignArgs.EndDate, "end-date", "", "campaign end, YYYY-MM-DD (or YYYY-MM-DD HH:MM:SS)")
	f.BoolVar(&updateCampaignArgs.ClearEndDate, "clear-end-date", false, "remove the campaign's end date so it runs indefinitely")
	f.StringVar(&updateCampaignArgs.BiddingStrategy, "bidding-strategy", "", "new standard bidding strategy (mutually exclusive with --portfolio-strategy-id)")
	f.StringVar(&updateCampaignArgs.PortfolioStrategyID, "portfolio-strategy-id", "", "attach the campaign to this portfolio bidding strategy (ID or resource name)")
	f.Float64Var(&updateCampaignArgs.TargetCPA, "target-cpa", 0, "target CPA in currency units")
	f.Float64Var(&updateCampaignArgs.TargetROAS, "target-roas", 0, "target ROAS ratio")
	f.BoolVar(&updateCampaignArgs.ClearTargetCPA, "clear-target-cpa", false, "remove the campaign's target CPA, leaving pure MAXIMIZE_CONVERSIONS")
	f.BoolVar(&updateCampaignArgs.ClearTargetROAS, "clear-target-roas", false, "remove the campaign's target ROAS, leaving pure MAXIMIZE_CONVERSION_VALUE")
	f.Float64Var(&updateCampaignArgs.DailyBudget, "daily-budget", 0, "new daily budget in currency units")
	f.StringArrayVar(&updateCampaignArgs.GeoTargetIDs, "geo-target-id", nil, "geo target constant ID to target (repeatable)")
	f.StringArrayVar(&updateCampaignArgs.ExcludeGeoTargetIDs, "exclude-geo-target-id", nil, "geo target constant ID to exclude (repeatable)")
	f.StringArrayVar(&updateCampaignArgs.LanguageIDs, "language-id", nil, "language constant ID to add (repeatable)")
	f.StringVar(&updateCampaignArgs.PositiveGeoTargetType, "positive-geo-target-type", "", "location option for targeted locations: PRESENCE_OR_INTEREST or PRESENCE")
	f.StringVar(&updateCampaignArgs.NegativeGeoTargetType, "negative-geo-target-type", "", "location option for excluded locations: PRESENCE (recommended) or PRESENCE_OR_INTEREST")
	f.StringVar(&updateCampaignArgs.DSADomain, "dsa-domain", "", "set the campaign's Dynamic Search Ads domain, e.g. example.com (needs --dsa-language-code; rewrites the whole setting)")
	f.StringVar(&updateCampaignArgs.DSALanguageCode, "dsa-language-code", "", "language of the DSA domain, e.g. en (needs --dsa-domain)")
	f.BoolVar(&updateCampaignArgs.DSAUseSuppliedURLsOnly, "dsa-use-supplied-urls-only", false, "serve only page-feed URLs; written whenever --dsa-domain is set, so pass it every time it should stay on")
	f.StringVar(&updateCampaignArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = campaignUpdateCmd.MarkFlagRequired("campaign-id")

	campaignCmd.AddCommand(campaignUpdateCmd)
}
