package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// ConversionsArgs lists the conversion actions configured in an account.
type ConversionsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
}

type ConversionsResult struct {
	ConversionActions []json.RawMessage `json:"conversion_actions"`
	TotalCount        int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r ConversionsResult) tableRows() ([]json.RawMessage, []string) {
	return r.ConversionActions, r.selectFields
}

func runConversions(ctx context.Context, c *Client, args ConversionsArgs) (ConversionsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return ConversionsResult{}, err
	}
	args.CustomerID = cid
	query := "SELECT " +
		"conversion_action.id, conversion_action.name, conversion_action.type, " +
		"conversion_action.status, conversion_action.category, " +
		"conversion_action.value_settings.default_value, conversion_action.counting_type " +
		"FROM conversion_action WHERE conversion_action.status != 'REMOVED' " +
		"ORDER BY conversion_action.name LIMIT 200"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return ConversionsResult{}, toolError("conversions", err)
	}
	return ConversionsResult{ConversionActions: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

var (
	conversionsArgs   ConversionsArgs
	conversionsFormat string
)

var conversionsCmd = &cobra.Command{
	Use:   "conversions",
	Short: "List conversion actions configured in an account",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runConversions(cmd.Context(), client, conversionsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), conversionsFormat, res)
	},
}

func init() {
	conversionsCmd.Flags().StringVar(&conversionsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(conversionsCmd, &conversionsFormat)
}
