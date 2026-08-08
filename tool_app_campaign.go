package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// CreateAppCampaignArgs creates an Android App campaign for installs as one
// atomic batch. The campaign uses target CPA bidding because Google Ads models
// target install cost through the target_cpa campaign bidding strategy.
type CreateAppCampaignArgs struct {
	CustomerID         string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that will own the campaign; omit to use the configured default customer"`
	CampaignName       string   `json:"campaign_name" jsonschema:"the campaign name"`
	DailyBudget        float64  `json:"daily_budget" jsonschema:"daily budget in currency units (capped by the budget guard)"`
	AppID              string   `json:"app_id" jsonschema:"the Google Play package name, for example app.cloudmount"`
	TargetCPI          float64  `json:"target_cpi" jsonschema:"target cost per install in currency units"`
	Headlines          []string `json:"headlines" jsonschema:"1-5 headlines, each at most 30 characters"`
	Descriptions       []string `json:"descriptions" jsonschema:"1-5 descriptions, each at most 90 characters"`
	ImageAssets        []string `json:"image_assets,omitempty" jsonschema:"optional Google Ads image asset resource names"`
	YouTubeVideoAssets []string `json:"youtube_video_assets,omitempty" jsonschema:"optional Google Ads YouTube video asset resource names"`
	GeoTargetIDs       []string `json:"geo_target_ids,omitempty" jsonschema:"optional geo target constant IDs"`
	LanguageIDs        []string `json:"language_ids,omitempty" jsonschema:"optional language constant IDs"`
	AdGroupName        string   `json:"ad_group_name,omitempty" jsonschema:"the ad group name; defaults to Ad group 1"`
	Status             string   `json:"status,omitempty" jsonschema:"ENABLED or PAUSED; defaults to PAUSED"`
	Confirm            string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreateAppCampaign(ctx context.Context, c *Client, args CreateAppCampaignArgs) (WriteResult, error) {
	const tool = "create_app_campaign"
	cfg := loadSafetyConfig()
	if err := checkBlockedOperation(tool, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if err := checkBudgetCap(args.DailyBudget, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if strings.TrimSpace(args.CampaignName) == "" {
		return WriteResult{}, fmt.Errorf("campaign_name is required")
	}
	if args.DailyBudget <= 0 {
		return WriteResult{}, fmt.Errorf("daily_budget must be greater than zero")
	}
	if strings.TrimSpace(args.AppID) == "" {
		return WriteResult{}, fmt.Errorf("app_id is required")
	}
	if args.TargetCPI <= 0 {
		return WriteResult{}, fmt.Errorf("target_cpi must be greater than zero")
	}
	if len(args.Headlines) < 1 || len(args.Headlines) > 5 {
		return WriteResult{}, fmt.Errorf("app campaign requires 1-5 headlines, got %d", len(args.Headlines))
	}
	if len(args.Descriptions) < 1 || len(args.Descriptions) > 5 {
		return WriteResult{}, fmt.Errorf("app campaign requires 1-5 descriptions, got %d", len(args.Descriptions))
	}
	if len(args.ImageAssets) > 20 {
		return WriteResult{}, fmt.Errorf("app campaign accepts at most 20 image assets, got %d", len(args.ImageAssets))
	}
	if len(args.YouTubeVideoAssets) > 20 {
		return WriteResult{}, fmt.Errorf("app campaign accepts at most 20 YouTube video assets, got %d", len(args.YouTubeVideoAssets))
	}
	for _, headline := range args.Headlines {
		if strings.TrimSpace(headline) == "" {
			return WriteResult{}, fmt.Errorf("headlines cannot be empty")
		}
		if err := validateHeadline(headline); err != nil {
			return WriteResult{}, err
		}
	}
	for _, description := range args.Descriptions {
		if strings.TrimSpace(description) == "" {
			return WriteResult{}, fmt.Errorf("descriptions cannot be empty")
		}
		if err := validateDescription(description); err != nil {
			return WriteResult{}, err
		}
	}
	for _, resourceName := range append(append([]string{}, args.ImageAssets...), args.YouTubeVideoAssets...) {
		if resourceName == "" {
			return WriteResult{}, fmt.Errorf("asset resource names cannot be empty")
		}
	}
	status, err := parseCreateStatus(args.Status)
	if err != nil {
		return WriteResult{}, err
	}
	for _, geoID := range args.GeoTargetIDs {
		if strings.TrimSpace(geoID) == "" {
			return WriteResult{}, fmt.Errorf("geo target IDs cannot be empty")
		}
	}
	for _, languageID := range args.LanguageIDs {
		if strings.TrimSpace(languageID) == "" {
			return WriteResult{}, fmt.Errorf("language IDs cannot be empty")
		}
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}

	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if err := numericIDs("geo_target_id", args.GeoTargetIDs); err != nil {
		return WriteResult{}, err
	}
	if err := numericIDs("language_id", args.LanguageIDs); err != nil {
		return WriteResult{}, err
	}
	budgetResource := fmt.Sprintf("customers/%s/campaignBudgets/-1", cid)
	campaignResource := fmt.Sprintf("customers/%s/campaigns/-2", cid)
	adGroupResource := fmt.Sprintf("customers/%s/adGroups/-3", cid)
	adGroupName := strings.TrimSpace(args.AdGroupName)
	if adGroupName == "" {
		adGroupName = "Ad group 1"
	}
	// Keep the complete ad group ready while a default-paused campaign acts as
	// the single serving gate. The preview's enable-campaign hint can then make
	// the campaign serve without requiring two additional lifecycle mutations.
	childStatus := AdStatusEnabled

	var ops []any
	ops = append(ops, map[string]any{"campaignBudgetOperation": map[string]any{"create": map[string]any{
		"name":             args.CampaignName + " Budget",
		"amountMicros":     microsString(dollarsToMicros(args.DailyBudget)),
		"deliveryMethod":   "STANDARD",
		"explicitlyShared": false,
		"resourceName":     budgetResource,
	}}})

	ops = append(ops, map[string]any{"campaignOperation": map[string]any{"create": map[string]any{
		"name":                      args.CampaignName,
		"status":                    string(status),
		"advertisingChannelType":    "MULTI_CHANNEL",
		"advertisingChannelSubType": "APP_CAMPAIGN",
		"campaignBudget":            budgetResource,
		"resourceName":              campaignResource,
		"targetCpa": map[string]any{
			"targetCpaMicros": microsString(dollarsToMicros(args.TargetCPI)),
		},
		"appCampaignSetting": map[string]any{
			"appId":                   strings.TrimSpace(args.AppID),
			"appStore":                "GOOGLE_APP_STORE",
			"biddingStrategyGoalType": "OPTIMIZE_INSTALLS_TARGET_INSTALL_COST",
		},
		"containsEuPoliticalAdvertising": "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING",
	}}})

	for _, geoID := range args.GeoTargetIDs {
		ops = append(ops, campaignLocationCriterion(campaignResource, geoID))
	}
	for _, languageID := range args.LanguageIDs {
		ops = append(ops, campaignLanguageCriterion(campaignResource, languageID))
	}

	ops = append(ops, map[string]any{"adGroupOperation": map[string]any{"create": map[string]any{
		"name":         adGroupName,
		"campaign":     campaignResource,
		"status":       string(childStatus),
		"resourceName": adGroupResource,
	}}})

	appAd := map[string]any{
		"headlines":    textAssets(args.Headlines),
		"descriptions": textAssets(args.Descriptions),
	}
	if len(args.ImageAssets) > 0 {
		appAd["images"] = assetRefs(args.ImageAssets)
	}
	if len(args.YouTubeVideoAssets) > 0 {
		appAd["youtubeVideos"] = assetRefs(args.YouTubeVideoAssets)
	}
	ops = append(ops, map[string]any{"adGroupAdOperation": map[string]any{"create": map[string]any{
		"adGroup": adGroupResource,
		"ad":      map[string]any{"appAd": appAd},
		"status":  string(childStatus),
	}}})

	summary := fmt.Sprintf(
		"Create App campaign %q for %s (budget %.2f/day, target CPI %.2f, status %s, %d ops)",
		args.CampaignName, args.AppID, args.DailyBudget, args.TargetCPI, status, len(ops),
	)
	result, err := previewMutate(tool, cid, summary, ops)
	if err != nil {
		return WriteResult{}, err
	}
	return result.withCreateStatus(status, enableCampaignHint("<resolve campaign_id from apply response>")), nil
}

var createAppCampaignArgs CreateAppCampaignArgs

var campaignCreateAppCmd = &cobra.Command{
	Use:   "create-app",
	Short: "Create an Android App campaign for installs (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		result, err := runCreateAppCampaign(cmd.Context(), client, createAppCampaignArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), result)
	},
}

func init() {
	f := campaignCreateAppCmd.Flags()
	f.StringVar(&createAppCampaignArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	f.StringVar(&createAppCampaignArgs.CampaignName, "name", "", "campaign name (required)")
	f.Float64Var(&createAppCampaignArgs.DailyBudget, "daily-budget", 0, "daily budget in currency units (required)")
	f.StringVar(&createAppCampaignArgs.AppID, "app-id", "", "Google Play package name (required)")
	f.Float64Var(&createAppCampaignArgs.TargetCPI, "target-cpi", 0, "target cost per install in currency units (required)")
	f.StringArrayVar(&createAppCampaignArgs.Headlines, "headline", nil, "headline (repeatable, 1-5)")
	f.StringArrayVar(&createAppCampaignArgs.Descriptions, "description", nil, "description (repeatable, 1-5)")
	f.StringArrayVar(&createAppCampaignArgs.ImageAssets, "image-asset", nil, "Google Ads image asset resource name (repeatable)")
	f.StringArrayVar(&createAppCampaignArgs.YouTubeVideoAssets, "youtube-video-asset", nil, "Google Ads YouTube video asset resource name (repeatable)")
	f.StringArrayVar(&createAppCampaignArgs.GeoTargetIDs, "geo-target-id", nil, "geo target constant ID (repeatable)")
	f.StringArrayVar(&createAppCampaignArgs.LanguageIDs, "language-id", nil, "language constant ID (repeatable)")
	f.StringVar(&createAppCampaignArgs.AdGroupName, "ad-group-name", "", "ad group name (defaults to Ad group 1)")
	f.StringVar(&createAppCampaignArgs.Status, "status", "", "ENABLED or PAUSED (default)")
	f.StringVar(&createAppCampaignArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = campaignCreateAppCmd.MarkFlagRequired("name")
	_ = campaignCreateAppCmd.MarkFlagRequired("daily-budget")
	_ = campaignCreateAppCmd.MarkFlagRequired("app-id")
	_ = campaignCreateAppCmd.MarkFlagRequired("target-cpi")

	campaignCmd.AddCommand(campaignCreateAppCmd)
}
