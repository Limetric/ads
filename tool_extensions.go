package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// ExtensionsArgs lists campaign-level extensions (sitelinks, callouts, and
// structured snippets).
type ExtensionsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
}

type ExtensionsResult struct {
	Extensions []json.RawMessage `json:"extensions"`
	TotalCount int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r ExtensionsResult) tableRows() ([]json.RawMessage, []string) {
	return r.Extensions, r.selectFields
}

func runExtensions(ctx context.Context, c *Client, args ExtensionsArgs) (ExtensionsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return ExtensionsResult{}, err
	}
	args.CustomerID = cid
	query := "SELECT " +
		"campaign_asset.campaign, campaign_asset.asset, campaign_asset.field_type, " +
		"asset.name, asset.type, " +
		"asset.sitelink_asset.link_text, asset.sitelink_asset.description1, asset.sitelink_asset.description2, " +
		"asset.callout_asset.callout_text, asset.structured_snippet_asset.header " +
		"FROM campaign_asset WHERE campaign_asset.status != 'REMOVED' LIMIT 500"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return ExtensionsResult{}, toolError("extensions", err)
	}
	return ExtensionsResult{Extensions: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

var (
	extensionsArgs   ExtensionsArgs
	extensionsFormat string
)

var extensionsCmd = &cobra.Command{
	Use:   "extensions",
	Short: "List campaign-level extensions (sitelinks, callouts, snippets)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runExtensions(cmd.Context(), client, extensionsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), extensionsFormat, res)
	},
}

func init() {
	extensionsCmd.Flags().StringVar(&extensionsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(extensionsCmd, &extensionsFormat)
}
