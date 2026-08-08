package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file creates and updates ad groups. New ad groups default to PAUSED for
// safety, and the preview carries a next-action hint pointing at enable_entity.

// CreateAdGroupArgs drafts a new ad group in an existing campaign.
type CreateAdGroupArgs struct {
	CustomerID   string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID   string `json:"campaign_id" jsonschema:"the campaign ID the ad group belongs to"`
	Name         string `json:"name" jsonschema:"the ad group name"`
	CpcBidMicros int64  `json:"cpc_bid_micros,omitempty" jsonschema:"optional default CPC bid in micros"`
	OmitType     bool   `json:"omit_type,omitempty" jsonschema:"omit the ad group type; required for App campaigns"`
	Status       string `json:"status,omitempty" jsonschema:"ENABLED or PAUSED; defaults to PAUSED"`
	Confirm      string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreateAdGroup(ctx context.Context, c *Client, args CreateAdGroupArgs) (WriteResult, error) {
	const tool = "create_ad_group"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Name == "" {
		return WriteResult{}, fmt.Errorf("name must not be empty")
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
	if _, err := numericID("campaign_id", args.CampaignID); err != nil {
		return WriteResult{}, err
	}
	create := map[string]any{
		"campaign": fmt.Sprintf("customers/%s/campaigns/%s", cid, args.CampaignID),
		"name":     args.Name,
		"status":   string(status),
	}
	if !args.OmitType {
		create["type"] = "SEARCH_STANDARD"
	}
	if args.CpcBidMicros != 0 {
		create["cpcBidMicros"] = microsString(args.CpcBidMicros)
	}
	op := map[string]any{"adGroupOperation": map[string]any{"create": create}}
	summary := fmt.Sprintf("Create ad group %q in campaign %s (status %s)", args.Name, args.CampaignID, status)
	res, err := previewMutate(tool, cid, summary, []any{op})
	if err != nil {
		return WriteResult{}, err
	}
	return res.withCreateStatus(status, enableAdGroupHint("<resolve ad_group_id from apply response>")), nil
}

// UpdateAdGroupArgs updates an existing ad group's name, CPC bid, and/or ad
// rotation mode. At least one field must be provided.
type UpdateAdGroupArgs struct {
	CustomerID     string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID      string `json:"ad_group_id" jsonschema:"the ad group ID to update"`
	Name           string `json:"name,omitempty" jsonschema:"new ad group name"`
	CpcBidMicros   int64  `json:"cpc_bid_micros,omitempty" jsonschema:"new default CPC bid in micros"`
	AdRotationMode string `json:"ad_rotation_mode,omitempty" jsonschema:"OPTIMIZE or ROTATE_FOREVER"`
	Confirm        string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runUpdateAdGroup(ctx context.Context, c *Client, args UpdateAdGroupArgs) (WriteResult, error) {
	const tool = "update_ad_group"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Name == "" && args.CpcBidMicros == 0 && args.AdRotationMode == "" {
		return WriteResult{}, fmt.Errorf("at least one of name, cpc_bid_micros, or ad_rotation_mode must be provided")
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
	update := map[string]any{"resourceName": fmt.Sprintf("customers/%s/adGroups/%s", cid, args.AdGroupID)}
	var mask []string
	if args.Name != "" {
		update["name"] = args.Name
		mask = append(mask, "name")
	}
	if args.CpcBidMicros != 0 {
		update["cpcBidMicros"] = microsString(args.CpcBidMicros)
		mask = append(mask, "cpcBidMicros")
	}
	if args.AdRotationMode != "" {
		mode, err := parseAdRotationMode(args.AdRotationMode)
		if err != nil {
			return WriteResult{}, err
		}
		update["adRotationMode"] = string(mode)
		mask = append(mask, "adRotationMode")
	}
	op := map[string]any{"adGroupOperation": map[string]any{"update": update, "updateMask": strings.Join(mask, ",")}}
	summary := fmt.Sprintf("Update ad group %s (%s)", args.AdGroupID, strings.Join(mask, ", "))
	return previewMutate(tool, cid, summary, []any{op})
}

// --- CLI front-end ---

var (
	createAdGroupArgs CreateAdGroupArgs
	updateAdGroupArgs UpdateAdGroupArgs
)

var adGroupCmd = &cobra.Command{
	Use:   "adgroup",
	Short: "Create and update ad groups",
}

var adGroupCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an ad group (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCreateAdGroup(cmd.Context(), client, createAdGroupArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var adGroupUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update an ad group (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runUpdateAdGroup(cmd.Context(), client, updateAdGroupArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	adGroupCreateCmd.Flags().StringVar(&createAdGroupArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	adGroupCreateCmd.Flags().StringVar(&createAdGroupArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	adGroupCreateCmd.Flags().StringVar(&createAdGroupArgs.Name, "name", "", "ad group name (required)")
	adGroupCreateCmd.Flags().Int64Var(&createAdGroupArgs.CpcBidMicros, "cpc-bid-micros", 0, "default CPC bid in micros")
	adGroupCreateCmd.Flags().BoolVar(&createAdGroupArgs.OmitType, "omit-type", false, "omit ad group type (required for App campaigns)")
	adGroupCreateCmd.Flags().StringVar(&createAdGroupArgs.Status, "status", "", "ENABLED, PAUSED (default), or REMOVED")
	adGroupCreateCmd.Flags().StringVar(&createAdGroupArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = adGroupCreateCmd.MarkFlagRequired("campaign-id")
	_ = adGroupCreateCmd.MarkFlagRequired("name")

	adGroupUpdateCmd.Flags().StringVar(&updateAdGroupArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	adGroupUpdateCmd.Flags().StringVar(&updateAdGroupArgs.AdGroupID, "ad-group-id", "", "ad group ID (required)")
	adGroupUpdateCmd.Flags().StringVar(&updateAdGroupArgs.Name, "name", "", "new ad group name")
	adGroupUpdateCmd.Flags().Int64Var(&updateAdGroupArgs.CpcBidMicros, "cpc-bid-micros", 0, "new default CPC bid in micros")
	adGroupUpdateCmd.Flags().StringVar(&updateAdGroupArgs.AdRotationMode, "ad-rotation-mode", "", "OPTIMIZE or ROTATE_FOREVER")
	adGroupUpdateCmd.Flags().StringVar(&updateAdGroupArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = adGroupUpdateCmd.MarkFlagRequired("ad-group-id")

	adGroupCmd.AddCommand(adGroupCreateCmd, adGroupUpdateCmd)
}
