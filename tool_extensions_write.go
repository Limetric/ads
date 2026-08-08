package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// This file drafts sitelink, callout, and structured-snippet extensions and
// removes campaign assets.
// Asset-create + campaign-link ops share a negative temp resource ID so the
// link references the just-created asset within the same mutate batch.

// tempAssetResource builds a negative-temp-ID asset resource name.
func tempAssetResource(cid string, tempID int) string {
	return fmt.Sprintf("customers/%s/assets/%d", cid, tempID)
}

// SitelinkInput is one sitelink extension.
type SitelinkInput struct {
	LinkText     string `json:"link_text" jsonschema:"the visible link text (<=25 chars)"`
	FinalURL     string `json:"final_url" jsonschema:"the landing page URL"`
	Description1 string `json:"description1" jsonschema:"first description line (<=35 chars)"`
	Description2 string `json:"description2" jsonschema:"second description line (<=35 chars)"`
}

// DraftSitelinksArgs drafts sitelink extensions for a campaign.
type DraftSitelinksArgs struct {
	CustomerID string          `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string          `json:"campaign_id" jsonschema:"the campaign ID to attach sitelinks to"`
	Sitelinks  []SitelinkInput `json:"sitelinks" jsonschema:"the sitelinks to create"`
	Confirm    string          `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runDraftSitelinks(ctx context.Context, c *Client, args DraftSitelinksArgs) (WriteResult, error) {
	const tool = "draft_sitelinks"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Sitelinks) == 0 {
		return WriteResult{}, fmt.Errorf("at least one sitelink is required")
	}
	for _, sl := range args.Sitelinks {
		if err := validateSitelinkText(sl.LinkText); err != nil {
			return WriteResult{}, err
		}
		if err := validateSitelinkDescription(sl.Description1); err != nil {
			return WriteResult{}, err
		}
		if err := validateSitelinkDescription(sl.Description2); err != nil {
			return WriteResult{}, err
		}
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
	campaignResource := fmt.Sprintf("customers/%s/campaigns/%s", cid, args.CampaignID)
	var ops []any
	for i, sl := range args.Sitelinks {
		assetResource := tempAssetResource(cid, -(100 + i))
		ops = append(ops, map[string]any{
			"assetOperation": map[string]any{
				"create": map[string]any{
					"resourceName": assetResource,
					"sitelinkAsset": map[string]any{
						"linkText":     sl.LinkText,
						"description1": sl.Description1,
						"description2": sl.Description2,
					},
					"finalUrls": []string{sl.FinalURL},
				},
			},
		})
		ops = append(ops, campaignAssetCreate(campaignResource, assetResource, "SITELINK"))
	}
	summary := fmt.Sprintf("Draft %d sitelink(s) on campaign %s", len(args.Sitelinks), args.CampaignID)
	if len(args.Sitelinks) < 2 {
		summary += " (warning: Google recommends at least 2 sitelinks)"
	}
	return previewMutate(tool, cid, summary, ops)
}

// CreateCalloutsArgs drafts callout extensions for a campaign.
type CreateCalloutsArgs struct {
	CustomerID string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string   `json:"campaign_id" jsonschema:"the campaign ID to attach callouts to"`
	Callouts   []string `json:"callouts" jsonschema:"callout texts, each at most 25 characters"`
	Confirm    string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreateCallouts(ctx context.Context, c *Client, args CreateCalloutsArgs) (WriteResult, error) {
	const tool = "create_callouts"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Callouts) == 0 {
		return WriteResult{}, fmt.Errorf("at least one callout is required")
	}
	for _, co := range args.Callouts {
		if err := validateSitelinkText(co); err != nil { // same 25-char limit
			return WriteResult{}, err
		}
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
	campaignResource := fmt.Sprintf("customers/%s/campaigns/%s", cid, args.CampaignID)
	var ops []any
	for i, co := range args.Callouts {
		assetResource := tempAssetResource(cid, -(200 + i))
		ops = append(ops, map[string]any{
			"assetOperation": map[string]any{
				"create": map[string]any{
					"resourceName": assetResource,
					"calloutAsset": map[string]any{"calloutText": co},
				},
			},
		})
		ops = append(ops, campaignAssetCreate(campaignResource, assetResource, "CALLOUT"))
	}
	summary := fmt.Sprintf("Draft %d callout(s) on campaign %s", len(args.Callouts), args.CampaignID)
	return previewMutate(tool, cid, summary, ops)
}

var validSnippetHeaders = map[string]bool{
	"Amenities": true, "Brands": true, "Courses": true, "Degree programs": true,
	"Destinations": true, "Featured hotels": true, "Insurance coverage": true,
	"Models": true, "Neighborhoods": true, "Service catalog": true, "Shows": true,
	"Styles": true, "Types": true,
}

// sortedSnippetHeaders lists the valid headers for error messages — the old
// hint suggested "Services", which the set rejects (issue #14).
func sortedSnippetHeaders() []string {
	headers := make([]string, 0, len(validSnippetHeaders))
	for h := range validSnippetHeaders {
		headers = append(headers, h)
	}
	sort.Strings(headers)
	return headers
}

// CreateSnippetsArgs drafts a structured-snippet extension for a campaign.
type CreateSnippetsArgs struct {
	CustomerID string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string   `json:"campaign_id" jsonschema:"the campaign ID to attach the snippet to"`
	Header     string   `json:"header" jsonschema:"a predefined snippet header, e.g. Brands, Service catalog, Types"`
	Values     []string `json:"values" jsonschema:"the snippet values"`
	Confirm    string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runCreateStructuredSnippets(ctx context.Context, c *Client, args CreateSnippetsArgs) (WriteResult, error) {
	const tool = "create_structured_snippets"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if !validSnippetHeaders[args.Header] {
		return WriteResult{}, fmt.Errorf("invalid structured snippet header %q — valid headers: %s", args.Header, strings.Join(sortedSnippetHeaders(), ", "))
	}
	if len(args.Values) == 0 {
		return WriteResult{}, fmt.Errorf("at least one value is required for structured snippets")
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
	campaignResource := fmt.Sprintf("customers/%s/campaigns/%s", cid, args.CampaignID)
	assetResource := tempAssetResource(cid, -300)
	ops := []any{
		map[string]any{
			"assetOperation": map[string]any{
				"create": map[string]any{
					"resourceName":           assetResource,
					"structuredSnippetAsset": map[string]any{"header": args.Header, "values": args.Values},
				},
			},
		},
		campaignAssetCreate(campaignResource, assetResource, "STRUCTURED_SNIPPET"),
	}
	summary := fmt.Sprintf("Draft structured snippet %q (%d values) on campaign %s", args.Header, len(args.Values), args.CampaignID)
	return previewMutate(tool, cid, summary, ops)
}

// RemoveExtensionArgs removes a campaign asset (extension) — destructive.
type RemoveExtensionArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string `json:"campaign_id" jsonschema:"the campaign ID"`
	AssetID    string `json:"asset_id" jsonschema:"the asset ID to unlink"`
	FieldType  string `json:"field_type" jsonschema:"the extension field type, e.g. SITELINK, CALLOUT, STRUCTURED_SNIPPET"`
	Confirm    string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runRemoveExtension(ctx context.Context, c *Client, args RemoveExtensionArgs) (WriteResult, error) {
	const tool = "remove_extension"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.CampaignID == "" || args.AssetID == "" || args.FieldType == "" {
		return WriteResult{}, fmt.Errorf("campaign_id, asset_id, and field_type are required")
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
	if _, err := numericID("asset_id", args.AssetID); err != nil {
		return WriteResult{}, err
	}
	resource := fmt.Sprintf("customers/%s/campaignAssets/%s~%s~%s", cid, args.CampaignID, args.AssetID, args.FieldType)
	op := map[string]any{"campaignAssetOperation": map[string]any{"remove": resource}}
	summary := fmt.Sprintf("REMOVE %s extension (asset %s) from campaign %s — destructive", args.FieldType, args.AssetID, args.CampaignID)
	return previewMutate(tool, cid, summary, []any{op})
}

// campaignAssetCreate builds a campaignAssetOperation linking an asset to a
// campaign with the given field type.
func campaignAssetCreate(campaignResource, assetResource, fieldType string) map[string]any {
	return map[string]any{
		"campaignAssetOperation": map[string]any{
			"create": map[string]any{
				"campaign":  campaignResource,
				"asset":     assetResource,
				"fieldType": fieldType,
			},
		},
	}
}

// --- CLI front-end ---

var (
	draftSitelinksArgs DraftSitelinksArgs
	sitelinkStrings    []string
	calloutsArgs       CreateCalloutsArgs
	snippetsArgs       CreateSnippetsArgs
	removeExtArgs      RemoveExtensionArgs
)

// parseSitelinkFlag parses "linkText|finalURL|desc1|desc2".
func parseSitelinkFlag(v string) (SitelinkInput, error) {
	parts := strings.Split(v, "|")
	if len(parts) != 4 {
		return SitelinkInput{}, fmt.Errorf("sitelink %q must be linkText|finalURL|desc1|desc2", v)
	}
	return SitelinkInput{LinkText: parts[0], FinalURL: parts[1], Description1: parts[2], Description2: parts[3]}, nil
}

var extensionCmd = &cobra.Command{
	Use:   "extension",
	Short: "Create and remove campaign extensions",
}

var extSitelinkCmd = &cobra.Command{
	Use:   "sitelinks",
	Short: "Draft sitelink extensions (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for _, s := range sitelinkStrings {
			sl, err := parseSitelinkFlag(s)
			if err != nil {
				return err
			}
			draftSitelinksArgs.Sitelinks = append(draftSitelinksArgs.Sitelinks, sl)
		}
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDraftSitelinks(cmd.Context(), client, draftSitelinksArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var extCalloutCmd = &cobra.Command{
	Use:   "callouts",
	Short: "Draft callout extensions (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCreateCallouts(cmd.Context(), client, calloutsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var extSnippetCmd = &cobra.Command{
	Use:   "snippets",
	Short: "Draft structured snippet extensions (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCreateStructuredSnippets(cmd.Context(), client, snippetsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var extRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove a campaign extension (destructive; previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runRemoveExtension(cmd.Context(), client, removeExtArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	extSitelinkCmd.Flags().StringVar(&draftSitelinksArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	extSitelinkCmd.Flags().StringVar(&draftSitelinksArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	extSitelinkCmd.Flags().StringArrayVar(&sitelinkStrings, "sitelink", nil, "sitelink as linkText|finalURL|desc1|desc2 (repeatable, required)")
	extSitelinkCmd.Flags().StringVar(&draftSitelinksArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = extSitelinkCmd.MarkFlagRequired("campaign-id")
	_ = extSitelinkCmd.MarkFlagRequired("sitelink")

	extCalloutCmd.Flags().StringVar(&calloutsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	extCalloutCmd.Flags().StringVar(&calloutsArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	extCalloutCmd.Flags().StringArrayVar(&calloutsArgs.Callouts, "callout", nil, "callout text (repeatable, required)")
	extCalloutCmd.Flags().StringVar(&calloutsArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = extCalloutCmd.MarkFlagRequired("campaign-id")
	_ = extCalloutCmd.MarkFlagRequired("callout")

	extSnippetCmd.Flags().StringVar(&snippetsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	extSnippetCmd.Flags().StringVar(&snippetsArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	extSnippetCmd.Flags().StringVar(&snippetsArgs.Header, "header", "", "snippet header, e.g. Brands (required)")
	extSnippetCmd.Flags().StringArrayVar(&snippetsArgs.Values, "value", nil, "snippet value (repeatable, required)")
	extSnippetCmd.Flags().StringVar(&snippetsArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = extSnippetCmd.MarkFlagRequired("campaign-id")
	_ = extSnippetCmd.MarkFlagRequired("header")
	_ = extSnippetCmd.MarkFlagRequired("value")

	extRemoveCmd.Flags().StringVar(&removeExtArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	extRemoveCmd.Flags().StringVar(&removeExtArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	extRemoveCmd.Flags().StringVar(&removeExtArgs.AssetID, "asset-id", "", "asset ID (required)")
	extRemoveCmd.Flags().StringVar(&removeExtArgs.FieldType, "field-type", "", "field type, e.g. SITELINK (required)")
	extRemoveCmd.Flags().StringVar(&removeExtArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = extRemoveCmd.MarkFlagRequired("campaign-id")
	_ = extRemoveCmd.MarkFlagRequired("asset-id")
	_ = extRemoveCmd.MarkFlagRequired("field-type")

	extensionCmd.AddCommand(extSitelinkCmd, extCalloutCmd, extSnippetCmd, extRemoveCmd)
}
