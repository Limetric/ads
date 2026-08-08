package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// PolicyArgs lists ads with policy issues (disapproved, limited, under review).
type PolicyArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
}

type PolicyResult struct {
	PolicyIssues []json.RawMessage `json:"policy_issues"`
	TotalCount   int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r PolicyResult) tableRows() ([]json.RawMessage, []string) {
	return r.PolicyIssues, r.selectFields
}

func runPolicy(ctx context.Context, c *Client, args PolicyArgs) (PolicyResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return PolicyResult{}, err
	}
	args.CustomerID = cid
	query := "SELECT " +
		"ad_group_ad.ad.id, ad_group_ad.ad.name, " +
		"ad_group_ad.policy_summary.approval_status, ad_group_ad.policy_summary.review_status, " +
		"ad_group_ad.policy_summary.policy_topic_entries, campaign.name, ad_group.name " +
		"FROM ad_group_ad WHERE ad_group_ad.policy_summary.approval_status != 'APPROVED' LIMIT 200"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return PolicyResult{}, toolError("policy", err)
	}
	return PolicyResult{PolicyIssues: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

var (
	policyArgs   PolicyArgs
	policyFormat string
)

var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "List ads with policy issues (disapproved, limited, under review)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runPolicy(cmd.Context(), client, policyArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), policyFormat, res)
	},
}

func init() {
	policyCmd.Flags().StringVar(&policyArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(policyCmd, &policyFormat)
}
