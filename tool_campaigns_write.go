package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// draft_campaign builds budget + campaign + ad group (+ optional keywords) as
// one atomic batch using negative temp resource IDs (-1 budget, -2 campaign,
// -3 ad group). The update half lives in tool_campaigns_update.go; the shared
// bidding-strategy helpers live here.

// applyBiddingStrategyCreate sets the bidding sub-field on a campaign create
// map. In v23 bidding_strategy_type is OUTPUT_ONLY, so the strategy is selected
// by setting the matching sub-field. TARGET_CPA/TARGET_ROAS require their
// target and unknown strategies error at preview — leaving bidding unset used
// to stage a campaign Google rejected only at confirm time.
func applyBiddingStrategyCreate(campaign map[string]any, strategy string, cpa, roas float64) error {
	switch strategy {
	case "MAXIMIZE_CONVERSIONS":
		mc := map[string]any{}
		if cpa != 0 {
			mc["targetCpaMicros"] = microsString(dollarsToMicros(cpa))
		}
		campaign["maximizeConversions"] = mc
	case "MAXIMIZE_CONVERSION_VALUE":
		mcv := map[string]any{}
		if roas != 0 {
			mcv["targetRoas"] = roas
		}
		campaign["maximizeConversionValue"] = mcv
	case "TARGET_CPA":
		if cpa == 0 {
			return fmt.Errorf("TARGET_CPA requires target_cpa (currency units)")
		}
		campaign["targetCpa"] = map[string]any{"targetCpaMicros": microsString(dollarsToMicros(cpa))}
	case "TARGET_ROAS":
		if roas == 0 {
			return fmt.Errorf("TARGET_ROAS requires target_roas (a ratio, e.g. 3.5)")
		}
		campaign["targetRoas"] = map[string]any{"targetRoas": roas}
	case "MANUAL_CPC":
		campaign["manualCpc"] = map[string]any{}
	case "TARGET_SPEND", "MAXIMIZE_CLICKS":
		campaign["targetSpend"] = map[string]any{}
	case "TARGET_IMPRESSION_SHARE":
		campaign["targetImpressionShare"] = map[string]any{}
	case "PERCENT_CPC":
		campaign["percentCpc"] = map[string]any{}
	case "":
		return fmt.Errorf("bidding_strategy is required (e.g. MAXIMIZE_CONVERSIONS)")
	default:
		return fmt.Errorf("unsupported bidding strategy %q — use one of MAXIMIZE_CONVERSIONS, MAXIMIZE_CONVERSION_VALUE, TARGET_CPA, TARGET_ROAS, MANUAL_CPC, TARGET_SPEND/MAXIMIZE_CLICKS, TARGET_IMPRESSION_SHARE, PERCENT_CPC", strategy)
	}
	return nil
}

// campaign.geo_target_type_setting decides how Google matches people to a
// campaign's targeted and excluded locations — the "Location options" the Ads
// UI puts under a campaign's Locations setting. PRESENCE_OR_INTEREST also
// reaches (or excludes) people who merely show interest in a location;
// PRESENCE is limited to people regularly in it. The positive enum carries a
// third value, SEARCH_INTEREST, that most campaign types no longer accept.
const (
	geoTargetTypePresenceOrInterest = "PRESENCE_OR_INTEREST"
	geoTargetTypePresence           = "PRESENCE"
)

// parseGeoTargetType validates a caller-supplied geo target type
// (case-insensitive) for the named argument.
func parseGeoTargetType(field, s string) (string, error) {
	switch v := strings.ToUpper(strings.TrimSpace(s)); v {
	case geoTargetTypePresenceOrInterest, geoTargetTypePresence:
		return v, nil
	case "SEARCH_INTEREST":
		return "", fmt.Errorf("%s SEARCH_INTEREST is deprecated in the Google Ads API and is no longer settable for most campaign types — use PRESENCE_OR_INTEREST or PRESENCE", field)
	default:
		return "", fmt.Errorf("unsupported %s %q — use PRESENCE_OR_INTEREST (people in, or interested in, the location) or PRESENCE (people in the location only)", field, s)
	}
}

// geoTargetTypeSetting builds a campaign's geoTargetTypeSetting sub-message
// from the supplied positive/negative types, plus the field-mask leaves naming
// the ones actually set (creates ignore the mask). It returns a nil map when
// neither is given, so an omitted setting stays untouched.
//
// The leaves are masked rather than the message: geoTargetTypeSetting has
// defined sub-fields, so it cannot appear bare in a field mask, and masking
// only the supplied side keeps the other from being cleared.
func geoTargetTypeSetting(positive, negative string) (map[string]any, []string, error) {
	setting := map[string]any{}
	var mask []string
	if strings.TrimSpace(positive) != "" {
		v, err := parseGeoTargetType("positive_geo_target_type", positive)
		if err != nil {
			return nil, nil, err
		}
		setting["positiveGeoTargetType"] = v
		mask = append(mask, "geoTargetTypeSetting.positiveGeoTargetType")
	}
	if strings.TrimSpace(negative) != "" {
		v, err := parseGeoTargetType("negative_geo_target_type", negative)
		if err != nil {
			return nil, nil, err
		}
		setting["negativeGeoTargetType"] = v
		mask = append(mask, "geoTargetTypeSetting.negativeGeoTargetType")
	}
	if len(setting) == 0 {
		return nil, nil, nil
	}
	return setting, mask, nil
}

// adGroupTypeForChannel maps a channel type to its standard ad group type.
func adGroupTypeForChannel(channelType string) string {
	if channelType == "DISPLAY" {
		return "DISPLAY_STANDARD"
	}
	return "SEARCH_STANDARD"
}

// DraftCampaignArgs drafts a new campaign with budget, ad group, and optional
// keywords. Defaults to PAUSED for safety.
type DraftCampaignArgs struct {
	CustomerID      string                 `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that will own the campaign; omit to use the configured default customer"`
	CampaignName    string                 `json:"campaign_name" jsonschema:"the campaign name"`
	DailyBudget     float64                `json:"daily_budget" jsonschema:"daily budget in currency units (capped by the budget guard)"`
	BiddingStrategy string                 `json:"bidding_strategy" jsonschema:"e.g. MAXIMIZE_CONVERSIONS, TARGET_CPA, MANUAL_CPC"`
	TargetCPA       float64                `json:"target_cpa,omitempty" jsonschema:"target CPA in currency units (for TARGET_CPA / MAXIMIZE_CONVERSIONS)"`
	TargetROAS      float64                `json:"target_roas,omitempty" jsonschema:"target ROAS ratio (for TARGET_ROAS / MAXIMIZE_CONVERSION_VALUE)"`
	ChannelType     string                 `json:"channel_type" jsonschema:"advertising channel, e.g. SEARCH or DISPLAY"`
	AdGroupName     string                 `json:"ad_group_name" jsonschema:"the name of the ad group to create"`
	Keywords        []KeywordWithMatchType `json:"keywords,omitempty" jsonschema:"optional keywords to add to the ad group"`
	GeoTargetIDs    []string               `json:"geo_target_ids,omitempty" jsonschema:"optional geo target constant IDs to target"`
	// ExcludeGeoTargetIDs adds excluded locations, which negative_geo_target_type
	// configures the matching of.
	ExcludeGeoTargetIDs []string `json:"exclude_geo_target_ids,omitempty" jsonschema:"optional geo target constant IDs to EXCLUDE; how they match is set by negative_geo_target_type"`
	LanguageIDs         []string `json:"language_ids,omitempty" jsonschema:"optional language constant IDs"`
	// Location options — how targeted/excluded locations are matched.
	PositiveGeoTargetType string `json:"positive_geo_target_type,omitempty" jsonschema:"how targeted locations are matched: PRESENCE_OR_INTEREST (the Google default) or PRESENCE for people in the location only"`
	NegativeGeoTargetType string `json:"negative_geo_target_type,omitempty" jsonschema:"how excluded locations are matched: PRESENCE (recommended) or PRESENCE_OR_INTEREST, which most campaign types no longer accept"`
	Status                string `json:"status,omitempty" jsonschema:"ENABLED or PAUSED; defaults to PAUSED"`
	Confirm               string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runDraftCampaign(ctx context.Context, c *Client, args DraftCampaignArgs) (WriteResult, error) {
	const tool = "draft_campaign"
	cfg := loadSafetyConfig()
	if err := checkBlockedOperation(tool, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	// MCP callers bypass the CLI's required-flag checks, so the handler
	// validates required fields itself: empty values used to stage malformed
	// resource names that failed only at confirm time (issue #14).
	if args.Confirm == "" {
		resolved, err := c.resolveCustomerID(args.CustomerID)
		if err != nil {
			return WriteResult{}, err
		}
		args.CustomerID = resolved
		if args.CampaignName == "" {
			return WriteResult{}, fmt.Errorf("campaign_name is required")
		}
		if args.AdGroupName == "" {
			return WriteResult{}, fmt.Errorf("ad_group_name is required")
		}
		if args.DailyBudget <= 0 {
			return WriteResult{}, fmt.Errorf("daily_budget must be positive (currency units)")
		}
	}
	if err := checkBudgetCap(args.DailyBudget, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	for _, kw := range args.Keywords {
		if err := validateMatchType(kw.MatchType); err != nil {
			return WriteResult{}, err
		}
		// Guard: BROAD match keywords in a MANUAL_CPC campaign burn budget.
		if err := checkBroadManualCPC(kw.MatchType, args.BiddingStrategy); err != nil {
			return WriteResult{}, toolError(tool, err)
		}
	}
	status, err := parseCreateStatus(args.Status)
	if err != nil {
		return WriteResult{}, err
	}
	geoSetting, _, err := geoTargetTypeSetting(args.PositiveGeoTargetType, args.NegativeGeoTargetType)
	if err != nil {
		return WriteResult{}, err
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}

	cid := normalizeCustomerID(args.CustomerID)
	channelType := args.ChannelType
	if channelType == "" {
		channelType = "SEARCH"
	}
	budgetResource := fmt.Sprintf("customers/%s/campaignBudgets/-1", cid)
	campaignResource := fmt.Sprintf("customers/%s/campaigns/-2", cid)
	adGroupResource := fmt.Sprintf("customers/%s/adGroups/-3", cid)

	var ops []any
	// 1. Budget.
	ops = append(ops, map[string]any{"campaignBudgetOperation": map[string]any{"create": map[string]any{
		"name":           args.CampaignName + " Budget",
		"amountMicros":   microsString(dollarsToMicros(args.DailyBudget)),
		"deliveryMethod": "STANDARD",
		// The API defaults explicitlyShared to true when omitted, which
		// breaks non-portfolio Smart Bidding strategies (MAXIMIZE_CONVERSIONS,
		// TARGET_CPA, ...) with "Bidding strategy type is incompatible with
		// shared budget". New campaigns get a dedicated budget by default,
		// matching how every existing campaign in this account is set up.
		"explicitlyShared": false,
		"resourceName":     budgetResource,
	}}})

	// 2. Campaign.
	campaignCreate := map[string]any{
		"name":                   args.CampaignName,
		"status":                 string(status),
		"advertisingChannelType": channelType,
		"campaignBudget":         budgetResource,
		"resourceName":           campaignResource,
		"networkSettings": map[string]any{
			"targetGoogleSearch":         true,
			"targetSearchNetwork":        true,
			"targetContentNetwork":       false,
			"targetPartnerSearchNetwork": false,
		},
		// Required by EU TTPA regulation (Oct 2025+); defaults to "does not contain".
		"containsEuPoliticalAdvertising": "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING",
	}
	if geoSetting != nil {
		campaignCreate["geoTargetTypeSetting"] = geoSetting
	}
	if err := applyBiddingStrategyCreate(campaignCreate, args.BiddingStrategy, args.TargetCPA, args.TargetROAS); err != nil {
		return WriteResult{}, err
	}
	ops = append(ops, map[string]any{"campaignOperation": map[string]any{"create": campaignCreate}})

	// 3. Geo targets and exclusions, 4. Language targets.
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

	// 5. Ad group (inherits the campaign status).
	ops = append(ops, map[string]any{"adGroupOperation": map[string]any{"create": map[string]any{
		"name":         args.AdGroupName,
		"campaign":     campaignResource,
		"status":       string(status),
		"type":         adGroupTypeForChannel(channelType),
		"resourceName": adGroupResource,
	}}})

	// 6. Keywords (optional).
	for _, kw := range args.Keywords {
		ops = append(ops, map[string]any{"adGroupCriterionOperation": map[string]any{"create": map[string]any{
			"adGroup": adGroupResource,
			"keyword": map[string]any{"text": kw.Text, "matchType": kw.MatchType},
		}}})
	}

	summary := fmt.Sprintf("Draft %s campaign %q (budget %.2f/day, ad group %q, %d keyword(s), status %s)",
		channelType, args.CampaignName, args.DailyBudget, args.AdGroupName, len(args.Keywords), status)
	res, err := previewMutate(tool, cid, summary, ops)
	if err != nil {
		return WriteResult{}, err
	}
	return res.withCreateStatus(status, enableCampaignHint("<resolve campaign_id from apply response>")), nil
}

// campaignLocationCriterion builds a geo-target campaign criterion create op.
// negative marks the location as *excluded* rather than targeted — the same
// payload plus "negative": true, and the only thing that made
// negative_geo_target_type (which configures how exclusions match) apply to
// anything a campaign built here actually holds (issue #53).
func campaignLocationCriterion(campaignResource, geoID string, negative bool) map[string]any {
	criterion := map[string]any{
		"campaign": campaignResource,
		"location": map[string]any{"geoTargetConstant": fmt.Sprintf("geoTargetConstants/%s", geoID)},
	}
	if negative {
		criterion["negative"] = true
	}
	return map[string]any{"campaignCriterionOperation": map[string]any{"create": criterion}}
}

// validateGeoTargetSelection checks both sides of a campaign's location
// targeting and rejects an ID that appears on both. Google applies the
// exclusion, so targeting and excluding the same place is never what was
// meant — and it fails here rather than silently.
func validateGeoTargetSelection(include, exclude []string) error {
	if err := numericIDs("geo_target_id", include); err != nil {
		return err
	}
	if err := numericIDs("exclude_geo_target_id", exclude); err != nil {
		return err
	}
	targeted := make(map[string]bool, len(include))
	for _, id := range include {
		targeted[id] = true
	}
	for _, id := range exclude {
		if targeted[id] {
			return fmt.Errorf("geo target %s is both targeted and excluded — an exclusion wins, so listing it in geo_target_ids does nothing; drop it from one of the two", id)
		}
	}
	return nil
}

// campaignLanguageCriterion builds a language campaign criterion create op.
func campaignLanguageCriterion(campaignResource, langID string) map[string]any {
	return map[string]any{"campaignCriterionOperation": map[string]any{"create": map[string]any{
		"campaign": campaignResource,
		"language": map[string]any{"languageConstant": fmt.Sprintf("languageConstants/%s", langID)},
	}}}
}

// --- CLI front-end ---

var (
	draftCampaignArgs    DraftCampaignArgs
	draftCampaignKwFlags []string
)

var campaignCmd = &cobra.Command{
	Use:   "campaign",
	Short: "Create and update campaigns",
}

var campaignCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Draft a new campaign with budget and ad group (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for _, s := range draftCampaignKwFlags {
			draftCampaignArgs.Keywords = append(draftCampaignArgs.Keywords, parseKeywordFlag(s))
		}
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDraftCampaign(cmd.Context(), client, draftCampaignArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	f := campaignCreateCmd.Flags()
	f.StringVar(&draftCampaignArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&draftCampaignArgs.CampaignName, "name", "", "campaign name (required)")
	f.Float64Var(&draftCampaignArgs.DailyBudget, "daily-budget", 0, "daily budget in currency units (required)")
	f.StringVar(&draftCampaignArgs.BiddingStrategy, "bidding-strategy", "MAXIMIZE_CONVERSIONS", "bidding strategy")
	f.Float64Var(&draftCampaignArgs.TargetCPA, "target-cpa", 0, "target CPA in currency units")
	f.Float64Var(&draftCampaignArgs.TargetROAS, "target-roas", 0, "target ROAS ratio")
	f.StringVar(&draftCampaignArgs.ChannelType, "channel-type", "SEARCH", "advertising channel, e.g. SEARCH or DISPLAY")
	f.StringVar(&draftCampaignArgs.AdGroupName, "ad-group-name", "", "ad group name (required)")
	f.StringArrayVar(&draftCampaignKwFlags, "keyword", nil, "keyword as text|MATCHTYPE (repeatable)")
	f.StringArrayVar(&draftCampaignArgs.GeoTargetIDs, "geo-target-id", nil, "geo target constant ID to target (repeatable)")
	f.StringArrayVar(&draftCampaignArgs.ExcludeGeoTargetIDs, "exclude-geo-target-id", nil, "geo target constant ID to exclude (repeatable)")
	f.StringArrayVar(&draftCampaignArgs.LanguageIDs, "language-id", nil, "language constant ID (repeatable)")
	f.StringVar(&draftCampaignArgs.PositiveGeoTargetType, "positive-geo-target-type", "", "location option for targeted locations: PRESENCE_OR_INTEREST or PRESENCE")
	f.StringVar(&draftCampaignArgs.NegativeGeoTargetType, "negative-geo-target-type", "", "location option for excluded locations: PRESENCE (recommended) or PRESENCE_OR_INTEREST")
	f.StringVar(&draftCampaignArgs.Status, "status", "", "ENABLED, PAUSED (default), or REMOVED")
	f.StringVar(&draftCampaignArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = campaignCreateCmd.MarkFlagRequired("name")
	_ = campaignCreateCmd.MarkFlagRequired("daily-budget")
	_ = campaignCreateCmd.MarkFlagRequired("ad-group-name")

	campaignCmd.AddCommand(campaignCreateCmd)
}
