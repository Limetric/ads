package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// This file creates a Performance Max campaign as one atomic mutate batch using
// negative temp resource IDs (-1 budget,
// -2 campaign, -3 asset group, -100.. text assets). Image assets are uploaded
// separately via upload_image_asset.

// CreatePmaxArgs creates a Performance Max campaign. start_paused defaults to
// true (the campaign and asset group ship PAUSED unless explicitly enabled).
type CreatePmaxArgs struct {
	CustomerID      string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that will own the campaign; omit to use the configured default customer"`
	CampaignName    string   `json:"campaign_name" jsonschema:"the campaign name"`
	DailyBudget     float64  `json:"daily_budget" jsonschema:"daily budget in currency units (capped by the budget guard)"`
	BiddingStrategy string   `json:"bidding_strategy" jsonschema:"MAXIMIZE_CONVERSIONS, MAXIMIZE_CONVERSION_VALUE, or TARGET_CPA"`
	TargetCPA       float64  `json:"target_cpa,omitempty" jsonschema:"target CPA in currency units (required for TARGET_CPA)"`
	TargetROAS      float64  `json:"target_roas,omitempty" jsonschema:"optional target ROAS ratio (for MAXIMIZE_CONVERSION_VALUE)"`
	FinalURLs       []string `json:"final_urls" jsonschema:"final URLs for the asset group"`
	Headlines       []string `json:"headlines" jsonschema:"3-15 headlines, each at most 30 characters"`
	LongHeadlines   []string `json:"long_headlines" jsonschema:"1-5 long headlines, each at most 90 characters"`
	Descriptions    []string `json:"descriptions" jsonschema:"2-5 descriptions, each at most 90 characters"`
	BusinessName    string   `json:"business_name" jsonschema:"business name, at most 25 characters"`
	GeoTargetIDs    []string `json:"geo_target_ids,omitempty" jsonschema:"optional geo target constant IDs"`
	StartPaused     *bool    `json:"start_paused,omitempty" jsonschema:"if true (default) the campaign starts PAUSED"`
	Confirm         string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreatePmaxCampaign(ctx context.Context, c *Client, args CreatePmaxArgs) (WriteResult, error) {
	const tool = "create_pmax_campaign"
	cfg := loadSafetyConfig()
	if err := checkBlockedOperation(tool, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if err := checkBudgetCap(args.DailyBudget, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Headlines) < 3 || len(args.Headlines) > 15 {
		return WriteResult{}, fmt.Errorf("PMax requires 3-15 headlines, got %d", len(args.Headlines))
	}
	if len(args.LongHeadlines) < 1 || len(args.LongHeadlines) > 5 {
		return WriteResult{}, fmt.Errorf("PMax requires 1-5 long headlines, got %d", len(args.LongHeadlines))
	}
	if len(args.Descriptions) < 2 || len(args.Descriptions) > 5 {
		return WriteResult{}, fmt.Errorf("PMax requires 2-5 descriptions, got %d", len(args.Descriptions))
	}
	for _, h := range args.Headlines {
		if err := validateHeadline(h); err != nil {
			return WriteResult{}, err
		}
	}
	for _, lh := range args.LongHeadlines {
		if err := validateDescription(lh); err != nil {
			return WriteResult{}, err
		}
	}
	for _, d := range args.Descriptions {
		if err := validateDescription(d); err != nil {
			return WriteResult{}, err
		}
	}
	if err := charLimit("business name", args.BusinessName, 25); err != nil {
		return WriteResult{}, err
	}
	if len(args.FinalURLs) == 0 {
		return WriteResult{}, fmt.Errorf("at least one final URL is required")
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}

	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if args.CampaignName == "" {
		return WriteResult{}, fmt.Errorf("campaign_name is required")
	}
	if args.DailyBudget <= 0 {
		return WriteResult{}, fmt.Errorf("daily_budget must be positive (currency units)")
	}
	if err := numericIDs("geo_target_id", args.GeoTargetIDs); err != nil {
		return WriteResult{}, err
	}
	paused := true
	if args.StartPaused != nil {
		paused = *args.StartPaused
	}
	status := AdStatusEnabled
	if paused {
		status = AdStatusPaused
	}

	budgetResource := fmt.Sprintf("customers/%s/campaignBudgets/-1", cid)
	campaignResource := fmt.Sprintf("customers/%s/campaigns/-2", cid)
	assetGroupResource := fmt.Sprintf("customers/%s/assetGroups/-3", cid)

	var ops []any
	// 1. Budget.
	ops = append(ops, map[string]any{"campaignBudgetOperation": map[string]any{"create": map[string]any{
		"name":           args.CampaignName + " Budget",
		"amountMicros":   microsString(dollarsToMicros(args.DailyBudget)),
		"deliveryMethod": "STANDARD",
		"resourceName":   budgetResource,
	}}})

	// 2. Campaign with bidding strategy.
	campaignCreate := map[string]any{
		"name":                   args.CampaignName,
		"status":                 string(status),
		"advertisingChannelType": "PERFORMANCE_MAX",
		"campaignBudget":         budgetResource,
		"resourceName":           campaignResource,
	}
	switch args.BiddingStrategy {
	case "MAXIMIZE_CONVERSION_VALUE":
		if args.TargetROAS < 0 {
			return WriteResult{}, fmt.Errorf("target_roas must be positive (a ratio, e.g. 3.5), got %v", args.TargetROAS)
		}
		mcv := map[string]any{}
		if args.TargetROAS > 0 {
			mcv["targetRoas"] = args.TargetROAS
		}
		campaignCreate["maximizeConversionValue"] = mcv
	case "MAXIMIZE_CONVERSIONS", "":
		campaignCreate["maximizeConversions"] = map[string]any{}
	case "TARGET_CPA":
		// PMax expresses a CPA target as maximizeConversions.targetCpaMicros;
		// it used to be silently dropped, staging materially different bidding
		// than requested (issue #14).
		if args.TargetCPA <= 0 {
			return WriteResult{}, fmt.Errorf("TARGET_CPA requires target_cpa (currency units)")
		}
		campaignCreate["maximizeConversions"] = map[string]any{"targetCpaMicros": microsString(dollarsToMicros(args.TargetCPA))}
	default:
		return WriteResult{}, fmt.Errorf("unsupported PMax bidding strategy %q — use MAXIMIZE_CONVERSIONS, MAXIMIZE_CONVERSION_VALUE, or TARGET_CPA", args.BiddingStrategy)
	}
	ops = append(ops, map[string]any{"campaignOperation": map[string]any{"create": campaignCreate}})

	// 3. Geo targets.
	for _, geoID := range args.GeoTargetIDs {
		ops = append(ops, map[string]any{"campaignCriterionOperation": map[string]any{"create": map[string]any{
			"campaign": campaignResource,
			"location": map[string]any{"geoTargetConstant": fmt.Sprintf("geoTargetConstants/%s", geoID)},
		}}})
	}

	// 4. Asset group (inherits the campaign status).
	ops = append(ops, map[string]any{"assetGroupOperation": map[string]any{"create": map[string]any{
		"name":         args.CampaignName + " Asset Group",
		"campaign":     campaignResource,
		"finalUrls":    args.FinalURLs,
		"status":       string(status),
		"resourceName": assetGroupResource,
	}}})

	// 5-8. Text assets linked to the asset group, each with a unique temp ID.
	tempAssetID := -100
	addTextAsset := func(text, fieldType string) {
		assetResource := fmt.Sprintf("customers/%s/assets/%d", cid, tempAssetID)
		ops = append(ops, map[string]any{"assetOperation": map[string]any{"create": map[string]any{
			"resourceName": assetResource,
			"textAsset":    map[string]any{"text": text},
		}}})
		ops = append(ops, map[string]any{"assetGroupAssetOperation": map[string]any{"create": map[string]any{
			"assetGroup": assetGroupResource,
			"asset":      assetResource,
			"fieldType":  fieldType,
		}}})
		tempAssetID--
	}
	for _, h := range args.Headlines {
		addTextAsset(h, "HEADLINE")
	}
	for _, lh := range args.LongHeadlines {
		addTextAsset(lh, "LONG_HEADLINE")
	}
	for _, d := range args.Descriptions {
		addTextAsset(d, "DESCRIPTION")
	}
	addTextAsset(args.BusinessName, "BUSINESS_NAME")

	summary := fmt.Sprintf("Create Performance Max campaign %q (budget %.2f/day, status %s, %d ops)",
		args.CampaignName, args.DailyBudget, status, len(ops))
	res, err := previewMutate(tool, cid, summary, ops)
	if err != nil {
		return WriteResult{}, err
	}
	return res.withCreateStatus(status, enableCampaignHint("<resolve campaign_id from apply response>")), nil
}

// --- CLI front-end ---

var (
	pmaxArgs        CreatePmaxArgs
	pmaxStartPaused bool
)

var pmaxCmd = &cobra.Command{
	Use:   "pmax",
	Short: "Create Performance Max campaigns",
}

var pmaxCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Performance Max campaign (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("start-paused") {
			pmaxArgs.StartPaused = &pmaxStartPaused
		}
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCreatePmaxCampaign(cmd.Context(), client, pmaxArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	f := pmaxCreateCmd.Flags()
	f.StringVar(&pmaxArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&pmaxArgs.CampaignName, "name", "", "campaign name (required)")
	f.Float64Var(&pmaxArgs.DailyBudget, "daily-budget", 0, "daily budget in currency units (required)")
	f.StringVar(&pmaxArgs.BiddingStrategy, "bidding-strategy", "MAXIMIZE_CONVERSIONS", "MAXIMIZE_CONVERSIONS, MAXIMIZE_CONVERSION_VALUE, or TARGET_CPA")
	f.StringArrayVar(&pmaxArgs.FinalURLs, "final-url", nil, "final URL (repeatable, required)")
	f.StringArrayVar(&pmaxArgs.Headlines, "headline", nil, "headline (repeatable, 3-15)")
	f.StringArrayVar(&pmaxArgs.LongHeadlines, "long-headline", nil, "long headline (repeatable, 1-5)")
	f.StringArrayVar(&pmaxArgs.Descriptions, "description", nil, "description (repeatable, 2-5)")
	f.StringVar(&pmaxArgs.BusinessName, "business-name", "", "business name (required)")
	f.StringArrayVar(&pmaxArgs.GeoTargetIDs, "geo-target-id", nil, "geo target constant ID (repeatable)")
	f.BoolVar(&pmaxStartPaused, "start-paused", true, "start the campaign PAUSED (default true)")
	f.StringVar(&pmaxArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = pmaxCreateCmd.MarkFlagRequired("name")
	_ = pmaxCreateCmd.MarkFlagRequired("daily-budget")
	_ = pmaxCreateCmd.MarkFlagRequired("business-name")

	pmaxCmd.AddCommand(pmaxCreateCmd)
}
