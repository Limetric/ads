package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// This file provides three read tools over keyword data: performance
// (keyword_view), search terms (search_term_view), and
// campaign-level negative keywords (campaign_criterion).

// KeywordPerformanceArgs scopes keyword performance to an optional date window.
type KeywordPerformanceArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	DateStart  string `json:"date_start,omitempty" jsonschema:"start date YYYY-MM-DD; pair with date_end to scope metrics; defaults to last 30 days"`
	DateEnd    string `json:"date_end,omitempty" jsonschema:"end date YYYY-MM-DD; pair with date_start to scope metrics; defaults to last 30 days"`
}

type KeywordPerformanceResult struct {
	Keywords   []json.RawMessage `json:"keywords"`
	TotalCount int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r KeywordPerformanceResult) tableRows() ([]json.RawMessage, []string) {
	return r.Keywords, r.selectFields
}

func runKeywordPerformance(ctx context.Context, c *Client, args KeywordPerformanceArgs) (KeywordPerformanceResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return KeywordPerformanceResult{}, err
	}
	args.CustomerID = cid
	dates, err := andDateClause(args.DateStart, args.DateEnd)
	if err != nil {
		return KeywordPerformanceResult{}, err
	}
	query := "SELECT " +
		"campaign.name, ad_group.name, ad_group_criterion.keyword.text, " +
		"ad_group_criterion.keyword.match_type, ad_group_criterion.quality_info.quality_score, " +
		"metrics.impressions, metrics.clicks, metrics.ctr, metrics.average_cpc, " +
		"metrics.cost_micros, metrics.conversions " +
		"FROM keyword_view WHERE ad_group_criterion.status != 'REMOVED'" +
		dates +
		" ORDER BY metrics.cost_micros DESC"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return KeywordPerformanceResult{}, toolError("keywords", err)
	}
	rows = enrichCostFields(rows)
	return KeywordPerformanceResult{Keywords: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// SearchTermsArgs scopes the search-terms report to a date window (defaults to
// the last 30 days when no explicit range is given).
type SearchTermsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	DateStart  string `json:"date_start,omitempty" jsonschema:"start date YYYY-MM-DD; defaults to last 30 days if omitted"`
	DateEnd    string `json:"date_end,omitempty" jsonschema:"end date YYYY-MM-DD; defaults to last 30 days if omitted"`
}

type SearchTermsResult struct {
	SearchTerms []json.RawMessage `json:"search_terms"`
	TotalCount  int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r SearchTermsResult) tableRows() ([]json.RawMessage, []string) {
	return r.SearchTerms, r.selectFields
}

func runSearchTerms(ctx context.Context, c *Client, args SearchTermsArgs) (SearchTermsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return SearchTermsResult{}, err
	}
	args.CustomerID = cid
	where, err := dateRangeClause(args.DateStart, args.DateEnd)
	if err != nil {
		return SearchTermsResult{}, err
	}
	query := "SELECT " +
		"search_term_view.search_term, campaign.name, ad_group.name, " +
		"metrics.impressions, metrics.clicks, metrics.cost_micros, metrics.conversions " +
		"FROM search_term_view WHERE " + where +
		" ORDER BY metrics.clicks DESC LIMIT 200"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return SearchTermsResult{}, toolError("search_terms", err)
	}
	rows = enrichCostFields(rows)
	return SearchTermsResult{SearchTerms: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// NegativeKeywordsArgs lists campaign-level negative keywords.
type NegativeKeywordsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
}

type NegativeKeywordsResult struct {
	NegativeKeywords []json.RawMessage `json:"negative_keywords"`
	TotalCount       int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r NegativeKeywordsResult) tableRows() ([]json.RawMessage, []string) {
	return r.NegativeKeywords, r.selectFields
}

func runNegativeKeywords(ctx context.Context, c *Client, args NegativeKeywordsArgs) (NegativeKeywordsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return NegativeKeywordsResult{}, err
	}
	args.CustomerID = cid
	query := "SELECT " +
		"campaign.id, campaign.name, campaign_criterion.keyword.text, " +
		"campaign_criterion.keyword.match_type, campaign_criterion.negative, " +
		"campaign_criterion.criterion_id " +
		"FROM campaign_criterion WHERE campaign_criterion.negative = TRUE " +
		"AND campaign_criterion.status != 'REMOVED'"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return NegativeKeywordsResult{}, toolError("negative_keywords", err)
	}
	return NegativeKeywordsResult{NegativeKeywords: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// --- CLI front-end ---

var (
	keywordPerfArgs   KeywordPerformanceArgs
	keywordPerfFormat string
	searchTermsArgs   SearchTermsArgs
	searchTermsFormat string
	negKeywordsArgs   NegativeKeywordsArgs
	negKeywordsFormat string
)

var keywordsCmd = &cobra.Command{
	Use:   "keywords",
	Short: "Inspect keyword performance, search terms, and negatives",
}

var keywordsPerfCmd = &cobra.Command{
	Use:   "performance",
	Short: "Show keyword-level performance metrics",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runKeywordPerformance(cmd.Context(), client, keywordPerfArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), keywordPerfFormat, res)
	},
}

var searchTermsCmd = &cobra.Command{
	Use:   "search-terms",
	Short: "Show the search terms that triggered ads",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runSearchTerms(cmd.Context(), client, searchTermsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), searchTermsFormat, res)
	},
}

var negativeKeywordsCmd = &cobra.Command{
	Use:   "negative",
	Short: "List campaign-level negative keywords",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runNegativeKeywords(cmd.Context(), client, negKeywordsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), negKeywordsFormat, res)
	},
}

func init() {
	keywordsPerfCmd.Flags().StringVar(&keywordPerfArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	keywordsPerfCmd.Flags().StringVar(&keywordPerfArgs.DateStart, "date-start", "", "start date YYYY-MM-DD (defaults to last 30 days)")
	keywordsPerfCmd.Flags().StringVar(&keywordPerfArgs.DateEnd, "date-end", "", "end date YYYY-MM-DD (defaults to last 30 days)")
	addFormatFlag(keywordsPerfCmd, &keywordPerfFormat)

	searchTermsCmd.Flags().StringVar(&searchTermsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	searchTermsCmd.Flags().StringVar(&searchTermsArgs.DateStart, "date-start", "", "start date YYYY-MM-DD")
	searchTermsCmd.Flags().StringVar(&searchTermsArgs.DateEnd, "date-end", "", "end date YYYY-MM-DD")
	addFormatFlag(searchTermsCmd, &searchTermsFormat)

	negativeKeywordsCmd.Flags().StringVar(&negKeywordsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(negativeKeywordsCmd, &negKeywordsFormat)

	keywordsCmd.AddCommand(keywordsPerfCmd, searchTermsCmd, negativeKeywordsCmd)
}
