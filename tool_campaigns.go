package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// CampaignsArgs is the input for the `campaigns` read tool. An optional date
// range narrows the performance metrics to a window; omit it for the last 30 days.
type CampaignsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	DateStart  string `json:"date_start,omitempty" jsonschema:"start date YYYY-MM-DD; pair with date_end to scope metrics; defaults to last 30 days"`
	DateEnd    string `json:"date_end,omitempty" jsonschema:"end date YYYY-MM-DD; pair with date_start to scope metrics; defaults to last 30 days"`
}

// CampaignsResult is the structured output: enriched campaign rows + a count.
type CampaignsResult struct {
	Campaigns  []json.RawMessage `json:"campaigns"`
	TotalCount int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r CampaignsResult) tableRows() ([]json.RawMessage, []string) {
	return r.Campaigns, r.selectFields
}

// runCampaigns returns campaign-level performance for all non-removed campaigns,
// ordered by cost descending, with cost and CPA fields enriched.
func runCampaigns(ctx context.Context, c *Client, args CampaignsArgs) (CampaignsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return CampaignsResult{}, err
	}
	args.CustomerID = cid
	dates, err := andDateClause(args.DateStart, args.DateEnd)
	if err != nil {
		return CampaignsResult{}, err
	}
	query := "SELECT " +
		"campaign.id, campaign.name, campaign.status, " +
		"campaign.advertising_channel_type, campaign.bidding_strategy_type, " +
		"metrics.impressions, metrics.clicks, metrics.cost_micros, " +
		"metrics.conversions, metrics.conversions_value, metrics.ctr, metrics.average_cpc " +
		"FROM campaign WHERE campaign.status != 'REMOVED'" +
		dates +
		" ORDER BY metrics.cost_micros DESC"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return CampaignsResult{}, toolError("campaigns", err)
	}
	rows = enrichCPA(enrichCostFields(rows))
	return CampaignsResult{Campaigns: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// andDateClause returns an explicit date range when both dates are set and
// otherwise defaults to the last 30 days. Shared by the metrics read tools.
func andDateClause(start, end string) (string, error) {
	clause, err := dateRangeClause(start, end)
	if err != nil {
		return "", err
	}
	return " AND " + clause, nil
}

// enrichCPA inserts metrics.cpa = cost / conversions (currency units) for rows
// that have positive conversions.
func enrichCPA(rows []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		v, ok := decodeRow(r)
		if !ok {
			out[i] = r
			continue
		}
		if m, ok := v.(map[string]any); ok {
			if metrics, ok := m["metrics"].(map[string]any); ok {
				costRaw := metrics["costMicros"]
				if costRaw == nil {
					costRaw = metrics["cost_micros"]
				}
				cost, okCost := asFloat(costRaw)
				conv, okConv := asFloat(metrics["conversions"])
				if okCost && okConv && conv > 0 {
					metrics["cpa"] = fmt.Sprintf("%.2f", (cost/1_000_000.0)/conv)
				}
			}
		}
		nb, err := json.Marshal(v)
		if err != nil {
			out[i] = r
			continue
		}
		out[i] = nb
	}
	return out
}

// --- CLI front-end ---

var (
	campaignsArgs   CampaignsArgs
	campaignsFormat string
)

var campaignsCmd = &cobra.Command{
	Use:   "campaigns",
	Short: "Show campaign-level performance metrics",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runCampaigns(cmd.Context(), client, campaignsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), campaignsFormat, res)
	},
}

func init() {
	campaignsCmd.Flags().StringVar(&campaignsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	campaignsCmd.Flags().StringVar(&campaignsArgs.DateStart, "date-start", "", "start date YYYY-MM-DD (defaults to last 30 days)")
	campaignsCmd.Flags().StringVar(&campaignsArgs.DateEnd, "date-end", "", "end date YYYY-MM-DD (defaults to last 30 days)")
	addFormatFlag(campaignsCmd, &campaignsFormat)
}
