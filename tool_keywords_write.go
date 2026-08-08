package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file drafts keywords, adds negative keywords, and removes both (the
// removes are destructive). All writes preview first.

var validMatchTypes = map[string]bool{"EXACT": true, "PHRASE": true, "BROAD": true}

func validateMatchType(mt string) error {
	if !validMatchTypes[mt] {
		return fmt.Errorf("invalid match type %q: must be EXACT, PHRASE, or BROAD", mt)
	}
	return nil
}

// KeywordWithMatchType is one keyword plus its match type.
type KeywordWithMatchType struct {
	Text      string `json:"text" jsonschema:"the keyword text"`
	MatchType string `json:"match_type" jsonschema:"EXACT, PHRASE, or BROAD"`
}

// DraftKeywordsArgs adds keywords to an ad group.
type DraftKeywordsArgs struct {
	CustomerID string                 `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID  string                 `json:"ad_group_id" jsonschema:"the ad group ID to add keywords to"`
	Keywords   []KeywordWithMatchType `json:"keywords" jsonschema:"the keywords to add, each with a match type"`
	Confirm    string                 `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runDraftKeywords(ctx context.Context, c *Client, args DraftKeywordsArgs) (WriteResult, error) {
	const tool = "draft_keywords"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Keywords) == 0 {
		return WriteResult{}, fmt.Errorf("at least one keyword is required")
	}
	hasBroad := false
	for _, kw := range args.Keywords {
		if err := validateMatchType(kw.MatchType); err != nil {
			return WriteResult{}, err
		}
		hasBroad = hasBroad || kw.MatchType == "BROAD"
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	adGroupID, err := numericID("ad_group_id", args.AdGroupID)
	if err != nil {
		return WriteResult{}, err
	}
	// Guard: BROAD match keywords into a MANUAL_CPC campaign burn budget. The
	// campaign's strategy is looked up from the ad group; a lookup failure is
	// an error (fail closed), while an ad group with no resolvable strategy
	// does not block (issue #12).
	if hasBroad {
		strategy, err := campaignBiddingStrategyForAdGroup(ctx, c, cid, adGroupID)
		if err != nil {
			return WriteResult{}, toolError(tool, fmt.Errorf("could not verify the campaign's bidding strategy for the BROAD+MANUAL_CPC guard: %w", err))
		}
		if strategy != "" {
			if err := checkBroadManualCPC("BROAD", strategy); err != nil {
				return WriteResult{}, toolError(tool, err)
			}
		}
	}
	adGroupResource := fmt.Sprintf("customers/%s/adGroups/%s", cid, adGroupID)
	ops := make([]any, len(args.Keywords))
	for i, kw := range args.Keywords {
		ops[i] = map[string]any{
			"adGroupCriterionOperation": map[string]any{
				"create": map[string]any{
					"adGroup": adGroupResource,
					"keyword": map[string]any{"text": kw.Text, "matchType": kw.MatchType},
				},
			},
		}
	}
	summary := fmt.Sprintf("Add %d keyword(s) to ad group %s", len(args.Keywords), args.AdGroupID)
	return previewMutate(tool, cid, summary, ops)
}

// campaignBiddingStrategyForAdGroup looks up the bidding strategy type of the
// campaign that owns an ad group. A query failure is returned as an error so
// the guard can fail closed; "" with a nil error means the strategy could not
// be resolved from the response (no rows / unexpected shape).
func campaignBiddingStrategyForAdGroup(ctx context.Context, c *Client, customerID, adGroupID string) (string, error) {
	if c == nil {
		return "", nil
	}
	q := fmt.Sprintf("SELECT campaign.bidding_strategy_type FROM ad_group WHERE ad_group.id = %s", adGroupID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	var row struct {
		Campaign struct {
			BiddingStrategyType string `json:"biddingStrategyType"`
		} `json:"campaign"`
	}
	if json.Unmarshal(rows[0], &row) != nil {
		return "", nil
	}
	return row.Campaign.BiddingStrategyType, nil
}

// AddNegativeKeywordsArgs adds campaign-level negative keywords.
type AddNegativeKeywordsArgs struct {
	CustomerID string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID string   `json:"campaign_id" jsonschema:"the campaign ID to add negatives to"`
	Keywords   []string `json:"keywords" jsonschema:"the negative keyword texts"`
	MatchType  string   `json:"match_type" jsonschema:"EXACT, PHRASE, or BROAD"`
	Confirm    string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runAddNegativeKeywords(ctx context.Context, c *Client, args AddNegativeKeywordsArgs) (WriteResult, error) {
	const tool = "add_negative_keywords"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.Keywords) == 0 {
		return WriteResult{}, fmt.Errorf("at least one keyword is required")
	}
	if err := validateMatchType(args.MatchType); err != nil {
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
	campaignResource := fmt.Sprintf("customers/%s/campaigns/%s", cid, args.CampaignID)
	ops := make([]any, len(args.Keywords))
	for i, kw := range args.Keywords {
		ops[i] = map[string]any{
			"campaignCriterionOperation": map[string]any{
				"create": map[string]any{
					"campaign": campaignResource,
					"negative": true,
					"keyword":  map[string]any{"text": kw, "matchType": args.MatchType},
				},
			},
		}
	}
	summary := fmt.Sprintf("Add %d negative keyword(s) to campaign %s (%s)", len(args.Keywords), args.CampaignID, args.MatchType)
	return previewMutate(tool, cid, summary, ops)
}

// RemoveKeywordsArgs removes keywords from an ad group by criterion ID
// (destructive).
type RemoveKeywordsArgs struct {
	CustomerID   string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the ad group; omit to use the configured default customer"`
	AdGroupID    string   `json:"ad_group_id" jsonschema:"the ad group ID"`
	CriterionIDs []string `json:"criterion_ids" jsonschema:"the keyword criterion IDs to remove"`
	Confirm      string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runRemoveKeywords(ctx context.Context, c *Client, args RemoveKeywordsArgs) (WriteResult, error) {
	const tool = "remove_keywords"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.CriterionIDs) == 0 {
		return WriteResult{}, fmt.Errorf("at least one criterion ID is required")
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
	if err := numericIDs("criterion_id", args.CriterionIDs); err != nil {
		return WriteResult{}, err
	}
	ops := make([]any, len(args.CriterionIDs))
	for i, critID := range args.CriterionIDs {
		ops[i] = map[string]any{
			"adGroupCriterionOperation": map[string]any{
				"remove": fmt.Sprintf("customers/%s/adGroupCriteria/%s~%s", cid, args.AdGroupID, critID),
			},
		}
	}
	summary := fmt.Sprintf("REMOVE %d keyword(s) from ad group %s — destructive", len(args.CriterionIDs), args.AdGroupID)
	return previewMutate(tool, cid, summary, ops)
}

// RemoveNegativeKeywordsArgs removes campaign-level negative keywords by
// criterion ID (destructive).
type RemoveNegativeKeywordsArgs struct {
	CustomerID   string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the campaign; omit to use the configured default customer"`
	CampaignID   string   `json:"campaign_id" jsonschema:"the campaign ID"`
	CriterionIDs []string `json:"criterion_ids" jsonschema:"the negative keyword criterion IDs to remove"`
	Confirm      string   `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runRemoveNegativeKeywords(ctx context.Context, c *Client, args RemoveNegativeKeywordsArgs) (WriteResult, error) {
	const tool = "remove_negative_keywords"
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if len(args.CriterionIDs) == 0 {
		return WriteResult{}, fmt.Errorf("at least one criterion ID is required")
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
	if err := numericIDs("criterion_id", args.CriterionIDs); err != nil {
		return WriteResult{}, err
	}
	ops := make([]any, len(args.CriterionIDs))
	for i, critID := range args.CriterionIDs {
		ops[i] = map[string]any{
			"campaignCriterionOperation": map[string]any{
				"remove": fmt.Sprintf("customers/%s/campaignCriteria/%s~%s", cid, args.CampaignID, critID),
			},
		}
	}
	summary := fmt.Sprintf("REMOVE %d negative keyword(s) from campaign %s — destructive", len(args.CriterionIDs), args.CampaignID)
	return previewMutate(tool, cid, summary, ops)
}

// --- CLI front-end ---

var (
	draftKeywordsArgs   DraftKeywordsArgs
	draftKeywordStrings []string
	addNegArgs          AddNegativeKeywordsArgs
	removeKwArgs        RemoveKeywordsArgs
	removeNegArgs       RemoveNegativeKeywordsArgs
)

// parseKeywordFlag parses "text|MATCHTYPE" (match type defaults to BROAD).
func parseKeywordFlag(v string) KeywordWithMatchType {
	if i := strings.LastIndex(v, "|"); i >= 0 {
		return KeywordWithMatchType{Text: v[:i], MatchType: strings.ToUpper(strings.TrimSpace(v[i+1:]))}
	}
	return KeywordWithMatchType{Text: v, MatchType: "BROAD"}
}

var keywordWriteParent = keywordsCmd // attach write subcommands under `keywords`

var keywordsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add keywords to an ad group (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		for _, s := range draftKeywordStrings {
			draftKeywordsArgs.Keywords = append(draftKeywordsArgs.Keywords, parseKeywordFlag(s))
		}
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDraftKeywords(cmd.Context(), client, draftKeywordsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var keywordsAddNegativeCmd = &cobra.Command{
	Use:   "add-negative",
	Short: "Add negative keywords to a campaign (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runAddNegativeKeywords(cmd.Context(), client, addNegArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var keywordsRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove keywords from an ad group by criterion ID (destructive)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runRemoveKeywords(cmd.Context(), client, removeKwArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var keywordsRemoveNegativeCmd = &cobra.Command{
	Use:   "remove-negative",
	Short: "Remove negative keywords from a campaign by criterion ID (destructive)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runRemoveNegativeKeywords(cmd.Context(), client, removeNegArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	keywordsAddCmd.Flags().StringVar(&draftKeywordsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	keywordsAddCmd.Flags().StringVar(&draftKeywordsArgs.AdGroupID, "ad-group-id", "", "ad group ID (required)")
	keywordsAddCmd.Flags().StringArrayVar(&draftKeywordStrings, "keyword", nil, "keyword as text|MATCHTYPE (repeatable, match type defaults to BROAD)")
	keywordsAddCmd.Flags().StringVar(&draftKeywordsArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = keywordsAddCmd.MarkFlagRequired("ad-group-id")
	_ = keywordsAddCmd.MarkFlagRequired("keyword")

	keywordsAddNegativeCmd.Flags().StringVar(&addNegArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	keywordsAddNegativeCmd.Flags().StringVar(&addNegArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	keywordsAddNegativeCmd.Flags().StringArrayVar(&addNegArgs.Keywords, "keyword", nil, "negative keyword text (repeatable, required)")
	keywordsAddNegativeCmd.Flags().StringVar(&addNegArgs.MatchType, "match-type", "BROAD", "EXACT, PHRASE, or BROAD")
	keywordsAddNegativeCmd.Flags().StringVar(&addNegArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = keywordsAddNegativeCmd.MarkFlagRequired("campaign-id")
	_ = keywordsAddNegativeCmd.MarkFlagRequired("keyword")

	keywordsRemoveCmd.Flags().StringVar(&removeKwArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	keywordsRemoveCmd.Flags().StringVar(&removeKwArgs.AdGroupID, "ad-group-id", "", "ad group ID (required)")
	keywordsRemoveCmd.Flags().StringArrayVar(&removeKwArgs.CriterionIDs, "criterion-id", nil, "keyword criterion ID (repeatable, required)")
	keywordsRemoveCmd.Flags().StringVar(&removeKwArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = keywordsRemoveCmd.MarkFlagRequired("ad-group-id")
	_ = keywordsRemoveCmd.MarkFlagRequired("criterion-id")

	keywordsRemoveNegativeCmd.Flags().StringVar(&removeNegArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	keywordsRemoveNegativeCmd.Flags().StringVar(&removeNegArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	keywordsRemoveNegativeCmd.Flags().StringArrayVar(&removeNegArgs.CriterionIDs, "criterion-id", nil, "negative keyword criterion ID (repeatable, required)")
	keywordsRemoveNegativeCmd.Flags().StringVar(&removeNegArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = keywordsRemoveNegativeCmd.MarkFlagRequired("campaign-id")
	_ = keywordsRemoveNegativeCmd.MarkFlagRequired("criterion-id")

	keywordWriteParent.AddCommand(keywordsAddCmd, keywordsAddNegativeCmd, keywordsRemoveCmd, keywordsRemoveNegativeCmd)
}
