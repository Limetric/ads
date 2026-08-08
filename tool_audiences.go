package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// This file creates custom audiences and attaches audience targeting to
// campaigns. Both operations preview first.

var (
	validAudienceTypes  = map[string]bool{"WEBSITE_VISITORS": true, "CUSTOMER_MATCH": true}
	validTargetingModes = map[string]bool{"TARGETING": true, "OBSERVATION": true}
)

// CreateAudienceArgs creates a custom audience from URL patterns or rules.
//
// Note: custom audiences are not a googleAds:mutate operation in v23 — they
// live on the dedicated customAudiences:mutate service, which this tool does
// not call yet. It errors at preview time instead of issuing a confirm token
// that could never be applied (issue #9).
type CreateAudienceArgs struct {
	CustomerID   string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that will own the audience; omit to use the configured default customer"`
	AudienceName string   `json:"audience_name" jsonschema:"a name for the custom audience"`
	AudienceType string   `json:"audience_type" jsonschema:"one of: WEBSITE_VISITORS, CUSTOMER_MATCH"`
	URLsOrRules  []string `json:"urls_or_rules" jsonschema:"URL-contains patterns or matching rules for the audience"`
	Confirm      string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreateCustomAudience(ctx context.Context, c *Client, args CreateAudienceArgs) (WriteResult, error) {
	const tool = "create_custom_audience"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if !validAudienceTypes[args.AudienceType] {
		return WriteResult{}, fmt.Errorf("invalid audience type %q: must be WEBSITE_VISITORS or CUSTOMER_MATCH", args.AudienceType)
	}
	if args.AudienceName == "" {
		return WriteResult{}, fmt.Errorf("audience name cannot be empty")
	}
	if len(args.URLsOrRules) == 0 {
		return WriteResult{}, fmt.Errorf("at least one URL pattern or rule is required")
	}
	// No preview token is issued: custom audiences need the dedicated
	// customAudiences:mutate service (not googleAds:mutate), so a staged
	// operation could never pass the mutate allow-list at confirm time.
	// Failing here is honest; handing out a dead token is not (issue #9).
	return WriteResult{}, fmt.Errorf("create_custom_audience is not supported yet: custom audiences use the dedicated customAudiences:mutate service in v23, which this client does not call. Create the audience in the Google Ads UI, then attach it with `ads google audience target` (MCP: google_add_audience_targeting)")
}

// AddAudienceTargetingArgs attaches an audience to a campaign in TARGETING or
// OBSERVATION mode.
type AddAudienceTargetingArgs struct {
	CustomerID    string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID    string `json:"campaign_id" jsonschema:"the campaign ID to target"`
	AudienceID    string `json:"audience_id" jsonschema:"the user list / audience ID to attach"`
	TargetingMode string `json:"targeting_mode" jsonschema:"TARGETING (limit to audience) or OBSERVATION (monitor only)"`
	Confirm       string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runAddAudienceTargeting(ctx context.Context, c *Client, args AddAudienceTargetingArgs) (WriteResult, error) {
	const tool = "add_audience_targeting"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if !validTargetingModes[args.TargetingMode] {
		return WriteResult{}, fmt.Errorf("invalid targeting mode %q: must be TARGETING or OBSERVATION", args.TargetingMode)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := numericID("campaign_id", args.CampaignID); err != nil {
		return WriteResult{}, err
	}
	if _, err := numericID("audience_id", args.AudienceID); err != nil {
		return WriteResult{}, err
	}
	criterion := map[string]any{
		"campaign": fmt.Sprintf("customers/%s/campaigns/%s", cid, args.CampaignID),
		"userList": map[string]any{"userList": fmt.Sprintf("customers/%s/userLists/%s", cid, args.AudienceID)},
	}
	if args.TargetingMode == "OBSERVATION" {
		criterion["bidModifier"] = 1.0
	}
	op := map[string]any{"campaignCriterionOperation": map[string]any{"create": criterion}}
	summary := fmt.Sprintf("Attach audience %s to campaign %s (%s)", args.AudienceID, args.CampaignID, args.TargetingMode)
	return previewMutate(tool, cid, summary, []any{op})
}

// --- CLI front-end ---

var (
	createAudienceArgs CreateAudienceArgs
	addTargetingArgs   AddAudienceTargetingArgs
)

var audienceCmd = &cobra.Command{
	Use:   "audience",
	Short: "Create custom audiences and attach audience targeting",
}

var audienceCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a custom audience (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCreateCustomAudience(cmd.Context(), client, createAudienceArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var audienceTargetCmd = &cobra.Command{
	Use:   "target",
	Short: "Attach audience targeting to a campaign (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runAddAudienceTargeting(cmd.Context(), client, addTargetingArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	audienceCreateCmd.Flags().StringVar(&createAudienceArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	audienceCreateCmd.Flags().StringVar(&createAudienceArgs.AudienceName, "name", "", "audience name (required)")
	audienceCreateCmd.Flags().StringVar(&createAudienceArgs.AudienceType, "type", "", "WEBSITE_VISITORS or CUSTOMER_MATCH (required)")
	audienceCreateCmd.Flags().StringArrayVar(&createAudienceArgs.URLsOrRules, "rule", nil, "URL pattern or rule (repeatable, required)")
	audienceCreateCmd.Flags().StringVar(&createAudienceArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = audienceCreateCmd.MarkFlagRequired("name")
	_ = audienceCreateCmd.MarkFlagRequired("type")
	_ = audienceCreateCmd.MarkFlagRequired("rule")

	audienceTargetCmd.Flags().StringVar(&addTargetingArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	audienceTargetCmd.Flags().StringVar(&addTargetingArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	audienceTargetCmd.Flags().StringVar(&addTargetingArgs.AudienceID, "audience-id", "", "audience/user list ID (required)")
	audienceTargetCmd.Flags().StringVar(&addTargetingArgs.TargetingMode, "mode", "", "TARGETING or OBSERVATION (required)")
	audienceTargetCmd.Flags().StringVar(&addTargetingArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = audienceTargetCmd.MarkFlagRequired("campaign-id")
	_ = audienceTargetCmd.MarkFlagRequired("audience-id")
	_ = audienceTargetCmd.MarkFlagRequired("mode")

	audienceCmd.AddCommand(audienceCreateCmd, audienceTargetCmd)
}
