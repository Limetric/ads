package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// This file lists conversion goals. Google groups conversion actions into
// goals by category and origin (PURCHASE:WEBSITE): account goals are the
// defaults every campaign bids toward, campaign goals override them for one
// campaign, and custom goals name an explicit set of conversion actions. The
// writes live in tool_conversion_goals_write.go.

// AccountConversionGoalsArgs lists an account's default conversion goals.
type AccountConversionGoalsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query; omit to use the configured default customer"`
}

// ConversionGoalsResult is the row result shared by the goal reads.
type ConversionGoalsResult struct {
	Goals      []json.RawMessage `json:"goals"`
	TotalCount int               `json:"total_count"`
	// GoalConfigs is set by the campaign read: whether each campaign follows the
	// account goals (CUSTOMER) or its own (CAMPAIGN), and its custom goal.
	GoalConfigs  []json.RawMessage `json:"goal_configs,omitempty"`
	selectFields []string
}

func (r ConversionGoalsResult) tableRows() ([]json.RawMessage, []string) {
	return r.Goals, r.selectFields
}

func runAccountConversionGoals(ctx context.Context, c *Client, args AccountConversionGoalsArgs) (ConversionGoalsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return ConversionGoalsResult{}, err
	}
	query := "SELECT customer_conversion_goal.category, customer_conversion_goal.origin, " +
		"customer_conversion_goal.biddable FROM customer_conversion_goal"
	rows, err := c.Search(ctx, cid, query)
	if err != nil {
		return ConversionGoalsResult{}, toolError("account_conversion_goals", err)
	}
	return ConversionGoalsResult{Goals: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// CampaignConversionGoalsArgs lists campaign-level conversion goals.
type CampaignConversionGoalsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query; omit to use the configured default customer"`
	CampaignID string `json:"campaign_id,omitempty" jsonschema:"limit to one campaign; omit to list every non-removed campaign"`
}

func runCampaignConversionGoals(ctx context.Context, c *Client, args CampaignConversionGoalsArgs) (ConversionGoalsResult, error) {
	const tool = "campaign_conversion_goals"
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return ConversionGoalsResult{}, err
	}
	where := "campaign.status != 'REMOVED'"
	if args.CampaignID != "" {
		campaignID, err := numericID("campaign_id", args.CampaignID)
		if err != nil {
			return ConversionGoalsResult{}, err
		}
		where += " AND campaign.id = " + campaignID
	}
	query := "SELECT campaign.id, campaign.name, campaign_conversion_goal.category, " +
		"campaign_conversion_goal.origin, campaign_conversion_goal.biddable " +
		"FROM campaign_conversion_goal WHERE " + where
	rows, err := c.Search(ctx, cid, query)
	if err != nil {
		return ConversionGoalsResult{}, toolError(tool, err)
	}
	configs, err := c.Search(ctx, cid, "SELECT campaign.id, campaign.name, "+
		"conversion_goal_campaign_config.goal_config_level, conversion_goal_campaign_config.custom_conversion_goal "+
		"FROM conversion_goal_campaign_config WHERE "+where)
	if err != nil {
		return ConversionGoalsResult{}, toolError(tool, err)
	}
	return ConversionGoalsResult{Goals: rows, TotalCount: len(rows), GoalConfigs: configs, selectFields: parseSelectFields(query)}, nil
}

// CustomConversionGoalsArgs lists custom conversion goals.
type CustomConversionGoalsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query; omit to use the configured default customer"`
}

func runCustomConversionGoals(ctx context.Context, c *Client, args CustomConversionGoalsArgs) (ConversionGoalsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return ConversionGoalsResult{}, err
	}
	query := "SELECT custom_conversion_goal.id, custom_conversion_goal.name, custom_conversion_goal.status, " +
		"custom_conversion_goal.conversion_actions FROM custom_conversion_goal " +
		"WHERE custom_conversion_goal.status != 'REMOVED' ORDER BY custom_conversion_goal.name"
	rows, err := c.Search(ctx, cid, query)
	if err != nil {
		return ConversionGoalsResult{}, toolError("custom_conversion_goals", err)
	}
	return ConversionGoalsResult{Goals: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// --- CLI front-end ---

var (
	accountGoalsArgs    AccountConversionGoalsArgs
	accountGoalsFormat  string
	campaignGoalsArgs   CampaignConversionGoalsArgs
	campaignGoalsFormat string
	customGoalsArgs     CustomConversionGoalsArgs
	customGoalsFormat   string
)

var goalsCmd = &cobra.Command{
	Use:   "goals",
	Short: "List conversion goals (account, campaign, and custom)",
}

var goalsAccountCmd = &cobra.Command{
	Use:   "account",
	Short: "List the account's default conversion goals and whether each is biddable",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runAccountConversionGoals(cmd.Context(), client, accountGoalsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), accountGoalsFormat, res)
	},
}

var goalsCampaignCmd = &cobra.Command{
	Use:   "campaign",
	Short: "List campaign conversion goals and each campaign's goal source (json output includes goal_configs)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCampaignConversionGoals(cmd.Context(), client, campaignGoalsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), campaignGoalsFormat, res)
	},
}

var goalsCustomCmd = &cobra.Command{
	Use:   "custom",
	Short: "List custom conversion goals",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCustomConversionGoals(cmd.Context(), client, customGoalsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), customGoalsFormat, res)
	},
}

func init() {
	goalsAccountCmd.Flags().StringVar(&accountGoalsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(goalsAccountCmd, &accountGoalsFormat)

	goalsCampaignCmd.Flags().StringVar(&campaignGoalsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	goalsCampaignCmd.Flags().StringVar(&campaignGoalsArgs.CampaignID, "campaign-id", "", "limit to one campaign")
	addFormatFlag(goalsCampaignCmd, &campaignGoalsFormat)

	goalsCustomCmd.Flags().StringVar(&customGoalsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(goalsCustomCmd, &customGoalsFormat)

	goalsCmd.AddCommand(goalsAccountCmd, goalsCampaignCmd, goalsCustomCmd)
}
