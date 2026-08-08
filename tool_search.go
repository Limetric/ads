package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// SearchArgs is the input for the `search` tool. Struct tags drive both the CLI
// flags and (via reflection) the MCP JSON schema, so descriptions live here.
type SearchArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	Query      string `json:"query" jsonschema:"the GAQL query to run, e.g. SELECT campaign.id, campaign.name FROM campaign"`
}

// SearchResult is the structured output for the `search` tool.
type SearchResult struct {
	CustomerID string            `json:"customer_id"`
	RowCount   int               `json:"row_count"`
	Rows       []json.RawMessage `json:"rows"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r SearchResult) tableRows() ([]json.RawMessage, []string) {
	return r.Rows, r.selectFields
}

// runSearch is the shared handler used by both the CLI and the MCP server.
func runSearch(ctx context.Context, c *Client, args SearchArgs) (SearchResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return SearchResult{}, err
	}
	args.CustomerID = cid
	if err := validateGAQL(args.Query); err != nil {
		return SearchResult{}, err
	}
	rows, err := c.Search(ctx, args.CustomerID, args.Query)
	if err != nil {
		return SearchResult{}, toolError("search", err)
	}
	return SearchResult{
		CustomerID:   normalizeCustomerID(args.CustomerID),
		RowCount:     len(rows),
		Rows:         rows,
		selectFields: parseSelectFields(args.Query),
	}, nil
}

// --- CLI front-end ---

var (
	searchArgs   SearchArgs
	searchFormat string
)

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Run a GAQL query and print the result rows as JSON",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runSearch(cmd.Context(), client, searchArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), searchFormat, res)
	},
}

func init() {
	searchCmd.Flags().StringVar(&searchArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	searchCmd.Flags().StringVar(&searchArgs.Query, "query", "", "GAQL query (required)")
	addFormatFlag(searchCmd, &searchFormat)
	_ = searchCmd.MarkFlagRequired("query")
}
