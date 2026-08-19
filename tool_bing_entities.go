package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// The entity reads: campaigns, ad groups, keywords, straight from Campaign
// Management. They are sub-second calls that return what an entity *is* — its
// name, status, budget, bid — with no performance data, because Microsoft keeps
// statistics in the Reporting service (see tool_bing_performance.go).
//
// Google answers both questions with one GAQL query. Bing cannot, and pretending
// otherwise would mean paying the reporting round-trip to answer "what are my
// campaigns called".

// BingCampaignsArgs lists the campaigns in an account.
type BingCampaignsArgs struct {
	AccountID string `json:"account_id,omitempty" jsonschema:"the Microsoft Advertising account ID; omit to use the configured default account"`
}

// BingCampaignRow is one campaign, flattened for output.
type BingCampaignRow struct {
	CampaignID   string   `json:"campaign_id"`
	Name         string   `json:"name"`
	Status       string   `json:"status,omitempty"`
	CampaignType string   `json:"campaign_type,omitempty"`
	SubType      string   `json:"sub_type,omitempty"`
	DailyBudget  *float64 `json:"daily_budget,omitempty"`
	BudgetType   string   `json:"budget_type,omitempty"`
	// SharedBudgetID is set when the campaign draws on a shared budget, in
	// which case daily_budget belongs to the budget, not this campaign.
	SharedBudgetID string `json:"shared_budget_id,omitempty"`
	TimeZone       string `json:"time_zone,omitempty"`
	BidStrategyID  string `json:"bid_strategy_id,omitempty"`
}

// BingCampaignsResult is the structured output of bing_campaigns.
type BingCampaignsResult struct {
	Campaigns  []BingCampaignRow `json:"campaigns"`
	TotalCount int               `json:"total_count"`
	// Message points at where the metrics live, because a tool called
	// "campaigns" that returns no spend is otherwise a surprise.
	Message string `json:"message,omitempty"`
}

// runBingCampaigns lists every campaign in an account, of every campaign type.
func runBingCampaigns(ctx context.Context, c *BingClient, args BingCampaignsArgs) (BingCampaignsResult, error) {
	accountID, err := c.resolveAccountID(args.AccountID)
	if err != nil {
		return BingCampaignsResult{}, err
	}
	campaigns, err := c.ListCampaigns(ctx, accountID)
	if err != nil {
		return BingCampaignsResult{}, toolError("campaigns", err)
	}
	rows := make([]BingCampaignRow, 0, len(campaigns))
	for _, campaign := range campaigns {
		row := BingCampaignRow{
			CampaignID:   campaign.ID,
			Name:         campaign.Name,
			Status:       campaign.Status,
			CampaignType: campaign.CampaignType,
			SubType:      campaign.SubType,
			DailyBudget:  campaign.DailyBudget,
			BudgetType:   campaign.BudgetType,
			TimeZone:     campaign.TimeZone,
		}
		row.SharedBudgetID = campaign.sharedBudgetID()
		row.BidStrategyID = campaign.portfolioBidStrategyID()
		rows = append(rows, row)
	}
	return BingCampaignsResult{
		Campaigns:  rows,
		TotalCount: len(rows),
		Message:    "Campaign settings only. For spend, clicks, and conversions use bing_campaign_performance.",
	}, nil
}

// BingAdGroupsArgs lists the ad groups in one campaign.
type BingAdGroupsArgs struct {
	AccountID  string `json:"account_id,omitempty" jsonschema:"the Microsoft Advertising account ID; omit to use the configured default account"`
	CampaignID string `json:"campaign_id" jsonschema:"the campaign whose ad groups to list (from bing_campaigns)"`
}

// BingAdGroupRow is one ad group, flattened for output.
type BingAdGroupRow struct {
	AdGroupID   string   `json:"ad_group_id"`
	Name        string   `json:"name"`
	Status      string   `json:"status,omitempty"`
	AdGroupType string   `json:"ad_group_type,omitempty"`
	CpcBid      *float64 `json:"cpc_bid,omitempty"`
	Language    string   `json:"language,omitempty"`
	Network     string   `json:"network,omitempty"`
	StartDate   string   `json:"start_date,omitempty"`
	EndDate     string   `json:"end_date,omitempty"`
}

// BingAdGroupsResult is the structured output of bing_ad_groups.
type BingAdGroupsResult struct {
	AdGroups   []BingAdGroupRow `json:"ad_groups"`
	TotalCount int              `json:"total_count"`
}

// runBingAdGroups lists the ad groups in a campaign.
func runBingAdGroups(ctx context.Context, c *BingClient, args BingAdGroupsArgs) (BingAdGroupsResult, error) {
	accountID, err := c.resolveAccountID(args.AccountID)
	if err != nil {
		return BingAdGroupsResult{}, err
	}
	campaignID, err := bingEntityID("campaign_id", args.CampaignID)
	if err != nil {
		return BingAdGroupsResult{}, err
	}
	adGroups, err := c.ListAdGroups(ctx, accountID, campaignID)
	if err != nil {
		return BingAdGroupsResult{}, toolError("ad_groups", err)
	}
	rows := make([]BingAdGroupRow, 0, len(adGroups))
	for _, adGroup := range adGroups {
		rows = append(rows, BingAdGroupRow{
			AdGroupID:   adGroup.ID,
			Name:        adGroup.Name,
			Status:      adGroup.Status,
			AdGroupType: adGroup.AdGroupType,
			CpcBid:      adGroup.CpcBid.value(),
			Language:    adGroup.Language,
			Network:     adGroup.Network,
			StartDate:   adGroup.StartDate.String(),
			EndDate:     adGroup.EndDate.String(),
		})
	}
	return BingAdGroupsResult{AdGroups: rows, TotalCount: len(rows)}, nil
}

// BingKeywordsArgs lists the keywords in one ad group.
type BingKeywordsArgs struct {
	AccountID string `json:"account_id,omitempty" jsonschema:"the Microsoft Advertising account ID; omit to use the configured default account"`
	AdGroupID string `json:"ad_group_id" jsonschema:"the ad group whose keywords to list (from bing_ad_groups)"`
}

// BingKeywordRow is one keyword, flattened for output.
type BingKeywordRow struct {
	KeywordID       string   `json:"keyword_id"`
	Text            string   `json:"text"`
	MatchType       string   `json:"match_type,omitempty"`
	Status          string   `json:"status,omitempty"`
	EditorialStatus string   `json:"editorial_status,omitempty"`
	Bid             *float64 `json:"bid,omitempty"`
}

// BingKeywordsResult is the structured output of bing_keywords.
type BingKeywordsResult struct {
	Keywords   []BingKeywordRow `json:"keywords"`
	TotalCount int              `json:"total_count"`
}

// runBingKeywords lists the keywords in an ad group. Microsoft has no
// account-wide keyword read: keywords are fetched per ad group, so a whole
// account means walking campaigns → ad groups → keywords. For account-wide
// keyword numbers, bing_keyword_performance is one report instead of hundreds
// of calls against a per-minute limit.
func runBingKeywords(ctx context.Context, c *BingClient, args BingKeywordsArgs) (BingKeywordsResult, error) {
	accountID, err := c.resolveAccountID(args.AccountID)
	if err != nil {
		return BingKeywordsResult{}, err
	}
	adGroupID, err := bingEntityID("ad_group_id", args.AdGroupID)
	if err != nil {
		return BingKeywordsResult{}, err
	}
	keywords, err := c.ListKeywords(ctx, accountID, adGroupID)
	if err != nil {
		return BingKeywordsResult{}, toolError("keywords", err)
	}
	rows := make([]BingKeywordRow, 0, len(keywords))
	for _, keyword := range keywords {
		rows = append(rows, BingKeywordRow{
			KeywordID:       keyword.ID,
			Text:            keyword.Text,
			MatchType:       keyword.MatchType,
			Status:          keyword.Status,
			EditorialStatus: keyword.EditorialStatus,
			Bid:             keyword.Bid.value(),
		})
	}
	return BingKeywordsResult{Keywords: rows, TotalCount: len(rows)}, nil
}

// bingEntityID validates a required entity identifier before it is sent.
func bingEntityID(field, id string) (string, error) {
	normalized := normalizeBingID(id)
	if normalized == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if !validBingID(normalized) {
		return "", fmt.Errorf("invalid %s %q — expected digits", field, id)
	}
	return normalized, nil
}

// --- CLI front-end ---

var (
	bingCampaignsArgs BingCampaignsArgs
	bingAdGroupsArgs  BingAdGroupsArgs
	bingKeywordsArgs  BingKeywordsArgs
)

var bingCampaignsCmd = &cobra.Command{
	Use:   "campaigns",
	Short: "List campaigns with their settings and budgets (metrics: `ads bing campaign-performance`)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newBingClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBingCampaigns(cmd.Context(), client, bingCampaignsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var bingAdGroupsCmd = &cobra.Command{
	Use:   "ad-groups",
	Short: "List the ad groups in a campaign",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newBingClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBingAdGroups(cmd.Context(), client, bingAdGroupsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var bingKeywordsCmd = &cobra.Command{
	Use:   "keywords",
	Short: "List the keywords in an ad group",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newBingClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBingKeywords(cmd.Context(), client, bingKeywordsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	addBingAccountFlag(bingCampaignsCmd, &bingCampaignsArgs.AccountID)

	addBingAccountFlag(bingAdGroupsCmd, &bingAdGroupsArgs.AccountID)
	bingAdGroupsCmd.Flags().StringVar(&bingAdGroupsArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	_ = bingAdGroupsCmd.MarkFlagRequired("campaign-id")

	addBingAccountFlag(bingKeywordsCmd, &bingKeywordsArgs.AccountID)
	bingKeywordsCmd.Flags().StringVar(&bingKeywordsArgs.AdGroupID, "ad-group-id", "", "ad group ID (required)")
	_ = bingKeywordsCmd.MarkFlagRequired("ad-group-id")
}
