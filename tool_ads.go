package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// AdsArgs is the input for the `ads` read tool, with an optional date window.
type AdsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	DateStart  string `json:"date_start,omitempty" jsonschema:"start date YYYY-MM-DD; pair with date_end to scope metrics; defaults to last 30 days"`
	DateEnd    string `json:"date_end,omitempty" jsonschema:"end date YYYY-MM-DD; pair with date_start to scope metrics; defaults to last 30 days"`
}

// AdsResult is the structured output: enriched ad rows + a count.
type AdsResult struct {
	Ads        []json.RawMessage `json:"ads"`
	TotalCount int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r AdsResult) tableRows() ([]json.RawMessage, []string) {
	return r.Ads, r.selectFields
}

// runAds returns ad-level performance for all non-removed ads, ordered by cost
// descending, with cost fields enriched.
func runAds(ctx context.Context, c *Client, args AdsArgs) (AdsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return AdsResult{}, err
	}
	args.CustomerID = cid
	dates, err := andDateClause(args.DateStart, args.DateEnd)
	if err != nil {
		return AdsResult{}, err
	}
	query := "SELECT " +
		"campaign.name, campaign.id, ad_group.name, ad_group.id, " +
		"ad_group_ad.ad.id, ad_group_ad.ad.type, " +
		"ad_group_ad.ad.responsive_search_ad.headlines, " +
		"ad_group_ad.ad.responsive_search_ad.descriptions, " +
		"ad_group_ad.ad.final_urls, ad_group_ad.status, " +
		"metrics.impressions, metrics.clicks, metrics.ctr, " +
		"metrics.conversions, metrics.cost_micros " +
		"FROM ad_group_ad WHERE ad_group_ad.status != 'REMOVED'" +
		dates +
		" ORDER BY metrics.cost_micros DESC"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return AdsResult{}, toolError("ads", err)
	}
	rows = enrichCostFields(rows)
	return AdsResult{Ads: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// --- CLI front-end ---

var (
	adsArgs   AdsArgs
	adsFormat string
)

var adsCmd = &cobra.Command{
	Use:   "ads",
	Short: "Show ad-level performance metrics",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runAds(cmd.Context(), client, adsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), adsFormat, res)
	},
}

func init() {
	adsCmd.Flags().StringVar(&adsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	adsCmd.Flags().StringVar(&adsArgs.DateStart, "date-start", "", "start date YYYY-MM-DD (defaults to last 30 days)")
	adsCmd.Flags().StringVar(&adsArgs.DateEnd, "date-end", "", "end date YYYY-MM-DD (defaults to last 30 days)")
	addFormatFlag(adsCmd, &adsFormat)
}
