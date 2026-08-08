package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ReportArgs runs an arbitrary GAQL query and renders the result in json (the
// default), table, or csv form.
type ReportArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	Query      string `json:"query" jsonschema:"the GAQL query to run, e.g. SELECT campaign.id, metrics.clicks FROM campaign"`
	Format     string `json:"format,omitempty" jsonschema:"output format: json (default), table, or csv"`
}

// ReportResult holds either structured rows (json) or rendered text (table/csv).
type ReportResult struct {
	Format     string            `json:"format"`
	TotalCount int               `json:"total_count"`
	Fields     []string          `json:"fields,omitempty"`
	Results    []json.RawMessage `json:"results,omitempty"`
	Formatted  string            `json:"formatted,omitempty"`
}

// runReport executes the query and shapes the output per Format.
func runReport(ctx context.Context, c *Client, args ReportArgs) (ReportResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return ReportResult{}, err
	}
	args.CustomerID = cid
	if err := validateGAQL(args.Query); err != nil {
		return ReportResult{}, err
	}
	rows, err := c.Search(ctx, args.CustomerID, args.Query)
	if err != nil {
		return ReportResult{}, toolError("report", err)
	}
	rows = enrichCostFields(rows)
	fields := parseSelectFields(args.Query)

	format := strings.ToLower(strings.TrimSpace(args.Format))
	switch format {
	case "table":
		return ReportResult{Format: "table", TotalCount: len(rows), Fields: fields, Formatted: formatTable(rows, fields)}, nil
	case "csv":
		return ReportResult{Format: "csv", TotalCount: len(rows), Fields: fields, Formatted: formatCSV(rows, fields)}, nil
	default:
		return ReportResult{Format: "json", TotalCount: len(rows), Fields: fields, Results: rows}, nil
	}
}

// --- CLI front-end ---

var reportArgs ReportArgs

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Run a GAQL query and render results as json, table, or csv",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runReport(cmd.Context(), client, reportArgs)
		if err != nil {
			return err
		}
		// For table/csv, print the rendered text directly; json prints structured.
		if res.Formatted != "" {
			fmt.Fprint(cmd.OutOrStdout(), res.Formatted)
			return nil
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	reportCmd.Flags().StringVar(&reportArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	reportCmd.Flags().StringVar(&reportArgs.Query, "query", "", "GAQL query (required)")
	reportCmd.Flags().StringVar(&reportArgs.Format, "format", "json", "output format: json, table, or csv")
	_ = reportCmd.MarkFlagRequired("query")
}
