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

// UpdateAdGroupArgs updates an existing ad group's name, bids, targets, and/or
// ad rotation mode. At least one change must be provided.
type UpdateAdGroupArgs struct {
	CustomerID     string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID      string `json:"ad_group_id" jsonschema:"the ad group ID to update"`
	Name           string `json:"name,omitempty" jsonschema:"new ad group name"`
	CpcBidMicros   int64  `json:"cpc_bid_micros,omitempty" jsonschema:"new default CPC bid in micros"`
	AdRotationMode string `json:"ad_rotation_mode,omitempty" jsonschema:"OPTIMIZE or ROTATE_FOREVER"`
	// An ad group's target overrides its campaign's for that ad group alone —
	// the standard way to bid a segment differently without splitting the
	// campaign (issue #54).
	TargetCpaMicros int64   `json:"target_cpa_micros,omitempty" jsonschema:"ad-group-level target CPA in micros, overriding the campaign target for this ad group"`
	TargetROAS      float64 `json:"target_roas,omitempty" jsonschema:"ad-group-level target ROAS ratio, overriding the campaign target for this ad group"`
	// The clear flags remove a value so the ad group inherits again. Omitting
	// the value cannot express this: an omitted number means "leave it alone".
	ClearCpcBid     bool   `json:"clear_cpc_bid,omitempty" jsonschema:"remove the ad group's own CPC bid; cannot be combined with cpc_bid_micros"`
	ClearTargetCPA  bool   `json:"clear_target_cpa,omitempty" jsonschema:"remove the ad group's target CPA so the campaign target applies; cannot be combined with target_cpa_micros"`
	ClearTargetROAS bool   `json:"clear_target_roas,omitempty" jsonschema:"remove the ad group's target ROAS so the campaign target applies; cannot be combined with target_roas"`
	Confirm         string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

// adGroupNumericField is one of the ad group's optional numeric leaves: a value
// to set, a flag that removes it, and the leaf both mask.
type adGroupNumericField struct {
	arg      string // argument name carrying the value, for error messages
	clearArg string // argument name that removes it
	leaf     string // the update-mask leaf
	set      bool   // a value was supplied
	clear    bool   // removal was asked for
	value    any    // the staged value, when set
}

// adGroupNumericFields describes every optional number update_ad_group can set
// or clear. Each is an independent scalar on the ad group — unlike a campaign's
// bidding targets, which are members of one oneof, so clearing more than one
// here is coherent.
func adGroupNumericFields(args UpdateAdGroupArgs) []adGroupNumericField {
	return []adGroupNumericField{
		{
			arg: "cpc_bid_micros", clearArg: "clear_cpc_bid", leaf: "cpcBidMicros",
			set: args.CpcBidMicros != 0, clear: args.ClearCpcBid, value: microsString(args.CpcBidMicros),
		},
		{
			arg: "target_cpa_micros", clearArg: "clear_target_cpa", leaf: "targetCpaMicros",
			set: args.TargetCpaMicros != 0, clear: args.ClearTargetCPA, value: microsString(args.TargetCpaMicros),
		},
		{
			arg: "target_roas", clearArg: "clear_target_roas", leaf: "targetRoas",
			set: args.TargetROAS != 0, clear: args.ClearTargetROAS, value: args.TargetROAS,
		},
	}
}

// validateUpdateAdGroup rejects an update that names no change, contradicts
// itself, or carries a negative number, before any API round trip.
func validateUpdateAdGroup(args UpdateAdGroupArgs) error {
	fields := adGroupNumericFields(args)
	changes := args.Name != "" || args.AdRotationMode != ""
	for _, f := range fields {
		if f.set && f.clear {
			return fmt.Errorf("%s cannot be combined with %s — pass %s alone to remove the value, or %s alone to change it", f.clearArg, f.arg, f.clearArg, f.arg)
		}
		changes = changes || f.set || f.clear
	}
	if !changes {
		return fmt.Errorf("at least one of name, cpc_bid_micros, target_cpa_micros, target_roas, ad_rotation_mode, or a clear_* flag must be provided")
	}
	if args.CpcBidMicros < 0 {
		return fmt.Errorf("cpc_bid_micros must be positive (micros), got %d", args.CpcBidMicros)
	}
	if args.TargetCpaMicros < 0 {
		return fmt.Errorf("target_cpa_micros must be positive (micros), got %d", args.TargetCpaMicros)
	}
	if args.TargetROAS < 0 {
		return fmt.Errorf("target_roas must be positive (a ratio, e.g. 3.5), got %v", args.TargetROAS)
	}
	if args.TargetCpaMicros != 0 && args.TargetROAS != 0 {
		return fmt.Errorf("target_cpa_micros and target_roas cannot be set together — an ad group overrides the target its campaign's bidding strategy actually uses")
	}
	return nil
}

func runUpdateAdGroup(ctx context.Context, c *Client, args UpdateAdGroupArgs) (WriteResult, error) {
	const tool = "update_ad_group"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if err := validateUpdateAdGroup(args); err != nil {
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
	update := map[string]any{"resourceName": fmt.Sprintf("customers/%s/adGroups/%s", cid, args.AdGroupID)}
	var mask []string
	var changes []string
	if args.Name != "" {
		update["name"] = args.Name
		mask = append(mask, "name")
		changes = append(changes, "name")
	}
	for _, f := range adGroupNumericFields(args) {
		switch {
		case f.set:
			update[f.leaf] = f.value
			mask = append(mask, f.leaf)
			changes = append(changes, f.leaf)
		case f.clear:
			// Masked but absent is what Google reads as unset, so the ad group
			// falls back to what it inherits rather than bidding a literal zero.
			mask = append(mask, f.leaf)
			changes = append(changes, "clear "+f.leaf)
		}
	}
	if args.AdRotationMode != "" {
		mode, err := parseAdRotationMode(args.AdRotationMode)
		if err != nil {
			return WriteResult{}, err
		}
		update["adRotationMode"] = string(mode)
		mask = append(mask, "adRotationMode")
		changes = append(changes, "adRotationMode")
	}
	op := map[string]any{"adGroupOperation": map[string]any{"update": update, "updateMask": strings.Join(mask, ",")}}
	summary := fmt.Sprintf("Update ad group %s (%s)", args.AdGroupID, strings.Join(changes, ", "))
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
	adGroupUpdateCmd.Flags().BoolVar(&updateAdGroupArgs.ClearCpcBid, "clear-cpc-bid", false, "remove the ad group's own CPC bid")
	adGroupUpdateCmd.Flags().Int64Var(&updateAdGroupArgs.TargetCpaMicros, "target-cpa-micros", 0, "ad-group-level target CPA in micros")
	adGroupUpdateCmd.Flags().BoolVar(&updateAdGroupArgs.ClearTargetCPA, "clear-target-cpa", false, "remove the ad group's target CPA so the campaign target applies")
	adGroupUpdateCmd.Flags().Float64Var(&updateAdGroupArgs.TargetROAS, "target-roas", 0, "ad-group-level target ROAS ratio")
	adGroupUpdateCmd.Flags().BoolVar(&updateAdGroupArgs.ClearTargetROAS, "clear-target-roas", false, "remove the ad group's target ROAS so the campaign target applies")
	adGroupUpdateCmd.Flags().StringVar(&updateAdGroupArgs.AdRotationMode, "ad-rotation-mode", "", "OPTIMIZE or ROTATE_FOREVER")
	adGroupUpdateCmd.Flags().StringVar(&updateAdGroupArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = adGroupUpdateCmd.MarkFlagRequired("ad-group-id")

	adGroupCmd.AddCommand(adGroupCreateCmd, adGroupUpdateCmd)
}
