package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file discovers keyword ideas from seed terms via the Keyword Planner
// generateKeywordIdeas endpoint and pulls historical performance for specific
// keywords as a rough forecast.

// DiscoverKeywordsArgs seeds keyword-idea discovery.
type DiscoverKeywordsArgs struct {
	CustomerID   string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	SeedKeywords []string `json:"seed_keywords" jsonschema:"seed keywords to expand into ideas, e.g. ['running shoes','trainers']"`
}

type DiscoverKeywordsResult struct {
	KeywordIdeas []json.RawMessage `json:"keyword_ideas"`
	TotalCount   int               `json:"total_count"`
	// selectFields carries the column order for the CLI's --format table/csv
	// rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r DiscoverKeywordsResult) tableRows() ([]json.RawMessage, []string) {
	return r.KeywordIdeas, r.selectFields
}

// keywordIdeaFields is the column set for keyword-idea rows. Ideas come from
// generateKeywordIdeas (not GAQL), so the columns are fixed here rather than
// parsed from a SELECT clause.
var keywordIdeaFields = []string{
	"text",
	"keyword_idea_metrics.avg_monthly_searches",
	"keyword_idea_metrics.competition",
	"keyword_idea_metrics.low_top_of_page_bid_micros",
	"keyword_idea_metrics.high_top_of_page_bid_micros",
}

func runDiscoverKeywords(ctx context.Context, c *Client, args DiscoverKeywordsArgs) (DiscoverKeywordsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return DiscoverKeywordsResult{}, err
	}
	args.CustomerID = cid
	if len(args.SeedKeywords) == 0 {
		return DiscoverKeywordsResult{}, fmt.Errorf("at least one seed keyword is required")
	}
	rows, err := c.GenerateKeywordIdeas(ctx, args.CustomerID, args.SeedKeywords, 50)
	if err != nil {
		return DiscoverKeywordsResult{}, toolError("keyword_ideas", err)
	}
	return DiscoverKeywordsResult{KeywordIdeas: rows, TotalCount: len(rows), selectFields: keywordIdeaFields}, nil
}

// KeywordForecastsArgs pulls recent historical metrics for specific keywords.
type KeywordForecastsArgs struct {
	CustomerID   string   `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	KeywordTexts []string `json:"keyword_texts" jsonschema:"the keyword texts to look up, e.g. ['running shoes']"`
}

type KeywordForecastsResult struct {
	KeywordForecasts []json.RawMessage `json:"keyword_forecasts"`
	TotalCount       int               `json:"total_count"`
	Message          string            `json:"message,omitempty"`
	selectFields     []string
}

func (r KeywordForecastsResult) tableRows() ([]json.RawMessage, []string) {
	return r.KeywordForecasts, r.selectFields
}

// keywordForecastSelect is the SELECT clause for keyword forecasts, shared so
// empty results still carry the column set for table/csv rendering.
const keywordForecastSelect = "SELECT " +
	"ad_group_criterion.keyword.text, metrics.average_cpc, metrics.impressions, " +
	"metrics.clicks, metrics.cost_micros, metrics.average_cpm "

func runKeywordForecasts(ctx context.Context, c *Client, args KeywordForecastsArgs) (KeywordForecastsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return KeywordForecastsResult{}, err
	}
	args.CustomerID = cid
	fields := parseSelectFields(keywordForecastSelect)
	if len(args.KeywordTexts) == 0 {
		return KeywordForecastsResult{KeywordForecasts: []json.RawMessage{}, TotalCount: 0, Message: "No keywords provided.", selectFields: fields}, nil
	}

	quoted := make([]string, len(args.KeywordTexts))
	for i, kw := range args.KeywordTexts {
		quoted[i] = quoteGAQLString(kw)
	}
	query := keywordForecastSelect +
		"FROM keyword_view WHERE ad_group_criterion.keyword.text IN (" + strings.Join(quoted, ", ") + ") " +
		"AND segments.date DURING LAST_30_DAYS"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return KeywordForecastsResult{}, toolError("keyword_forecasts", err)
	}
	if len(rows) == 0 {
		return KeywordForecastsResult{
			KeywordForecasts: []json.RawMessage{},
			TotalCount:       0,
			Message:          "No matching keywords found in the account. These keywords may not exist in any active ad group.",
			selectFields:     fields,
		}, nil
	}
	rows = enrichCostFields(rows)
	return KeywordForecastsResult{KeywordForecasts: rows, TotalCount: len(rows), selectFields: fields}, nil
}

// --- CLI front-end ---

var (
	discoverArgs    DiscoverKeywordsArgs
	discoverFormat  string
	forecastsArgs   KeywordForecastsArgs
	forecastsFormat string
)

var keywordIdeasCmd = &cobra.Command{
	Use:   "keyword-ideas",
	Short: "Discover keyword ideas from seed keywords (Keyword Planner)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDiscoverKeywords(cmd.Context(), client, discoverArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), discoverFormat, res)
	},
}

var keywordForecastsCmd = &cobra.Command{
	Use:   "keyword-forecasts",
	Short: "Get recent historical metrics for specific keywords",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runKeywordForecasts(cmd.Context(), client, forecastsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), forecastsFormat, res)
	},
}

func init() {
	keywordIdeasCmd.Flags().StringVar(&discoverArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	keywordIdeasCmd.Flags().StringSliceVar(&discoverArgs.SeedKeywords, "seed", nil, "seed keyword (repeatable)")
	addFormatFlag(keywordIdeasCmd, &discoverFormat)
	_ = keywordIdeasCmd.MarkFlagRequired("seed")

	keywordForecastsCmd.Flags().StringVar(&forecastsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	keywordForecastsCmd.Flags().StringSliceVar(&forecastsArgs.KeywordTexts, "keyword", nil, "keyword text (repeatable)")
	addFormatFlag(keywordForecastsCmd, &forecastsFormat)
}
