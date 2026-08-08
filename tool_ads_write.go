package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// This file drafts Responsive Search Ads (RSAs). New ads default to PAUSED with
// a next-action hint to enable them.

// DraftRsaArgs drafts a Responsive Search Ad in an ad group. RSAs need 3-15
// headlines (<=30 chars) and 2-4 descriptions (<=90 chars).
type DraftRsaArgs struct {
	CustomerID   string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID    string   `json:"ad_group_id" jsonschema:"the ad group ID to create the ad in"`
	Headlines    []string `json:"headlines" jsonschema:"3-15 headlines, each at most 30 characters"`
	Descriptions []string `json:"descriptions" jsonschema:"2-4 descriptions, each at most 90 characters"`
	FinalURL     string   `json:"final_url" jsonschema:"the landing page URL"`
	Path1        string   `json:"path1,omitempty" jsonschema:"optional display URL path segment 1"`
	Path2        string   `json:"path2,omitempty" jsonschema:"optional display URL path segment 2"`
	Status       string   `json:"status,omitempty" jsonschema:"ENABLED or PAUSED; defaults to PAUSED"`
	Confirm      string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runDraftResponsiveSearchAd(ctx context.Context, c *Client, args DraftRsaArgs) (WriteResult, error) {
	const tool = "draft_responsive_search_ad"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Headlines) < 3 || len(args.Headlines) > 15 {
		return WriteResult{}, fmt.Errorf("RSA requires 3-15 headlines, got %d", len(args.Headlines))
	}
	if len(args.Descriptions) < 2 || len(args.Descriptions) > 4 {
		return WriteResult{}, fmt.Errorf("RSA requires 2-4 descriptions, got %d", len(args.Descriptions))
	}
	for _, h := range args.Headlines {
		if err := validateHeadline(h); err != nil {
			return WriteResult{}, err
		}
	}
	for _, d := range args.Descriptions {
		if err := validateDescription(d); err != nil {
			return WriteResult{}, err
		}
	}
	if args.FinalURL == "" {
		return WriteResult{}, fmt.Errorf("final_url is required")
	}
	status, err := parseCreateStatus(args.Status)
	if err != nil {
		return WriteResult{}, err
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}

	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := numericID("ad_group_id", args.AdGroupID); err != nil {
		return WriteResult{}, err
	}
	rsa := map[string]any{
		"headlines":    textAssets(args.Headlines),
		"descriptions": textAssets(args.Descriptions),
	}
	if args.Path1 != "" {
		rsa["path1"] = args.Path1
	}
	if args.Path2 != "" {
		rsa["path2"] = args.Path2
	}
	op := map[string]any{
		"adGroupAdOperation": map[string]any{
			"create": map[string]any{
				"adGroup": fmt.Sprintf("customers/%s/adGroups/%s", cid, args.AdGroupID),
				"ad":      map[string]any{"responsiveSearchAd": rsa, "finalUrls": []string{args.FinalURL}},
				"status":  string(status),
			},
		},
	}
	summary := fmt.Sprintf("Draft RSA in ad group %s (%d headlines, %d descriptions, status %s)",
		args.AdGroupID, len(args.Headlines), len(args.Descriptions), status)
	res, err := previewMutate(tool, cid, summary, []any{op})
	if err != nil {
		return WriteResult{}, err
	}
	return res.withCreateStatus(status, enableAdHint(args.AdGroupID, "<resolve ad_id from apply response>")), nil
}

// DraftAppAdArgs drafts an App campaign ad. App ads are immutable after
// creation, so copy or asset changes are made by creating a replacement ad.
type DraftAppAdArgs struct {
	CustomerID         string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID          string   `json:"ad_group_id" jsonschema:"the App campaign ad group ID to create the replacement ad in"`
	Headlines          []string `json:"headlines" jsonschema:"1-5 headlines, each at most 30 characters"`
	Descriptions       []string `json:"descriptions" jsonschema:"1-5 descriptions, each at most 90 characters"`
	ImageAssets        []string `json:"image_assets,omitempty" jsonschema:"Google Ads image asset resource names"`
	YouTubeVideoAssets []string `json:"youtube_video_assets,omitempty" jsonschema:"Google Ads YouTube video asset resource names"`
	Status             string   `json:"status,omitempty" jsonschema:"ENABLED or PAUSED; defaults to PAUSED"`
	Confirm            string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runDraftAppAd(ctx context.Context, c *Client, args DraftAppAdArgs) (WriteResult, error) {
	const tool = "draft_app_ad"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.AdGroupID == "" {
		return WriteResult{}, fmt.Errorf("ad_group_id is required")
	}
	if len(args.Headlines) < 1 || len(args.Headlines) > 5 {
		return WriteResult{}, fmt.Errorf("app ad requires 1-5 headlines, got %d", len(args.Headlines))
	}
	if len(args.Descriptions) < 1 || len(args.Descriptions) > 5 {
		return WriteResult{}, fmt.Errorf("app ad requires 1-5 descriptions, got %d", len(args.Descriptions))
	}
	if len(args.ImageAssets) > 20 {
		return WriteResult{}, fmt.Errorf("app ad accepts at most 20 image assets, got %d", len(args.ImageAssets))
	}
	if len(args.YouTubeVideoAssets) > 20 {
		return WriteResult{}, fmt.Errorf("app ad accepts at most 20 YouTube video assets, got %d", len(args.YouTubeVideoAssets))
	}
	for _, headline := range args.Headlines {
		if err := validateHeadline(headline); err != nil {
			return WriteResult{}, err
		}
	}
	for _, description := range args.Descriptions {
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
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}

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
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := numericID("ad_group_id", args.AdGroupID); err != nil {
		return WriteResult{}, err
	}
	op := map[string]any{
		"adGroupAdOperation": map[string]any{
			"create": map[string]any{
				"adGroup": fmt.Sprintf("customers/%s/adGroups/%s", cid, args.AdGroupID),
				"ad":      map[string]any{"appAd": appAd},
				"status":  string(status),
			},
		},
	}
	summary := fmt.Sprintf(
		"Draft App ad in ad group %s (%d headlines, %d descriptions, %d images, %d videos, status %s)",
		args.AdGroupID, len(args.Headlines), len(args.Descriptions), len(args.ImageAssets), len(args.YouTubeVideoAssets), status,
	)
	result, err := previewMutate(tool, cid, summary, []any{op})
	if err != nil {
		return WriteResult{}, err
	}
	return result.withCreateStatus(status, enableAdHint(args.AdGroupID, "<resolve ad_id from apply response>")), nil
}

// textAssets wraps each string as a {"text": …} asset object.
func textAssets(texts []string) []any {
	out := make([]any, len(texts))
	for i, t := range texts {
		out[i] = map[string]any{"text": t}
	}
	return out
}

func assetRefs(resourceNames []string) []any {
	out := make([]any, len(resourceNames))
	for i, resourceName := range resourceNames {
		out[i] = map[string]any{"asset": resourceName}
	}
	return out
}

// --- CLI front-end ---

var (
	draftRsaArgs   DraftRsaArgs
	draftAppAdArgs DraftAppAdArgs
)

var adCmd = &cobra.Command{
	Use:   "ad",
	Short: "Create ads",
}

var adDraftRsaCmd = &cobra.Command{
	Use:   "draft-rsa",
	Short: "Draft a Responsive Search Ad (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDraftResponsiveSearchAd(cmd.Context(), client, draftRsaArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var adDraftAppCmd = &cobra.Command{
	Use:   "draft-app",
	Short: "Draft an App campaign ad (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		result, err := runDraftAppAd(cmd.Context(), client, draftAppAdArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), result)
	},
}

func init() {
	adDraftRsaCmd.Flags().StringVar(&draftRsaArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	adDraftRsaCmd.Flags().StringVar(&draftRsaArgs.AdGroupID, "ad-group-id", "", "ad group ID (required)")
	adDraftRsaCmd.Flags().StringArrayVar(&draftRsaArgs.Headlines, "headline", nil, "headline (repeatable, 3-15)")
	adDraftRsaCmd.Flags().StringArrayVar(&draftRsaArgs.Descriptions, "description", nil, "description (repeatable, 2-4)")
	adDraftRsaCmd.Flags().StringVar(&draftRsaArgs.FinalURL, "final-url", "", "landing page URL (required)")
	adDraftRsaCmd.Flags().StringVar(&draftRsaArgs.Path1, "path1", "", "display URL path segment 1")
	adDraftRsaCmd.Flags().StringVar(&draftRsaArgs.Path2, "path2", "", "display URL path segment 2")
	adDraftRsaCmd.Flags().StringVar(&draftRsaArgs.Status, "status", "", "ENABLED, PAUSED (default), or REMOVED")
	adDraftRsaCmd.Flags().StringVar(&draftRsaArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = adDraftRsaCmd.MarkFlagRequired("ad-group-id")
	_ = adDraftRsaCmd.MarkFlagRequired("final-url")

	adDraftAppCmd.Flags().StringVar(&draftAppAdArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	adDraftAppCmd.Flags().StringVar(&draftAppAdArgs.AdGroupID, "ad-group-id", "", "App campaign ad group ID (required)")
	adDraftAppCmd.Flags().StringArrayVar(&draftAppAdArgs.Headlines, "headline", nil, "headline (repeatable, 1-5)")
	adDraftAppCmd.Flags().StringArrayVar(&draftAppAdArgs.Descriptions, "description", nil, "description (repeatable, 1-5)")
	adDraftAppCmd.Flags().StringArrayVar(&draftAppAdArgs.ImageAssets, "image-asset", nil, "Google Ads image asset resource name (repeatable)")
	adDraftAppCmd.Flags().StringArrayVar(&draftAppAdArgs.YouTubeVideoAssets, "youtube-video-asset", nil, "Google Ads YouTube video asset resource name (repeatable)")
	adDraftAppCmd.Flags().StringVar(&draftAppAdArgs.Status, "status", "", "ENABLED, PAUSED (default), or REMOVED")
	adDraftAppCmd.Flags().StringVar(&draftAppAdArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = adDraftAppCmd.MarkFlagRequired("ad-group-id")

	adCmd.AddCommand(adDraftRsaCmd, adDraftAppCmd)
}
