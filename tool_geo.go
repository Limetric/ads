package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file searches geo target constants by name (to find location IDs) and
// reports geographic performance.

// GeoTargetsArgs searches geo target constants whose name matches a substring.
type GeoTargetsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	Query      string `json:"query" jsonschema:"a location name substring to search for, e.g. 'California'"`
}

type GeoTargetsResult struct {
	GeoTargets []json.RawMessage `json:"geo_targets"`
	TotalCount int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r GeoTargetsResult) tableRows() ([]json.RawMessage, []string) {
	return r.GeoTargets, r.selectFields
}

func runGeoTargets(ctx context.Context, c *Client, args GeoTargetsArgs) (GeoTargetsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return GeoTargetsResult{}, err
	}
	args.CustomerID = cid
	if strings.TrimSpace(args.Query) == "" {
		return GeoTargetsResult{}, fmt.Errorf("query is required")
	}
	// LIKE pattern: escape backslashes and single quotes so the literal stays
	// well-formed — escaping only quotes let a trailing backslash break out of
	// the string (issue #13).
	pattern := escapeGAQLString(args.Query)
	query := "SELECT " +
		"geo_target_constant.id, geo_target_constant.name, " +
		"geo_target_constant.canonical_name, geo_target_constant.country_code, " +
		"geo_target_constant.target_type " +
		"FROM geo_target_constant WHERE geo_target_constant.name LIKE '%" + pattern + "%'"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return GeoTargetsResult{}, toolError("geo_targets", err)
	}
	return GeoTargetsResult{GeoTargets: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// GeoPerformanceArgs reports geographic performance, defaulting to the last 30
// days when no explicit date range is supplied.
type GeoPerformanceArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
	DateStart  string `json:"date_start,omitempty" jsonschema:"start date YYYY-MM-DD; defaults to last 30 days if omitted"`
	DateEnd    string `json:"date_end,omitempty" jsonschema:"end date YYYY-MM-DD; defaults to last 30 days if omitted"`
}

type GeoPerformanceResult struct {
	GeoPerformance []json.RawMessage `json:"geo_performance"`
	TotalCount     int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r GeoPerformanceResult) tableRows() ([]json.RawMessage, []string) {
	return r.GeoPerformance, r.selectFields
}

func runGeoPerformance(ctx context.Context, c *Client, args GeoPerformanceArgs) (GeoPerformanceResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return GeoPerformanceResult{}, err
	}
	args.CustomerID = cid
	where, err := dateRangeClause(args.DateStart, args.DateEnd)
	if err != nil {
		return GeoPerformanceResult{}, err
	}
	query := "SELECT " +
		"campaign.name, geographic_view.country_criterion_id, geographic_view.location_type, " +
		"metrics.impressions, metrics.clicks, metrics.cost_micros, metrics.conversions " +
		"FROM geographic_view WHERE " + where +
		" ORDER BY metrics.cost_micros DESC"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return GeoPerformanceResult{}, toolError("geo_performance", err)
	}
	rows = enrichCostFields(rows)
	return GeoPerformanceResult{GeoPerformance: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// --- CLI front-end ---

var (
	geoTargetsArgs   GeoTargetsArgs
	geoTargetsFormat string
	geoPerfArgs      GeoPerformanceArgs
	geoPerfFormat    string
)

var geoCmd = &cobra.Command{
	Use:   "geo",
	Short: "Search geo targets and view geographic performance",
}

var geoSearchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search geo target constants by name (find location IDs)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runGeoTargets(cmd.Context(), client, geoTargetsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), geoTargetsFormat, res)
	},
}

var geoPerfCmd = &cobra.Command{
	Use:   "performance",
	Short: "Show geographic performance for campaigns",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runGeoPerformance(cmd.Context(), client, geoPerfArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), geoPerfFormat, res)
	},
}

func init() {
	geoSearchCmd.Flags().StringVar(&geoTargetsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	geoSearchCmd.Flags().StringVar(&geoTargetsArgs.Query, "query", "", "location name substring (required)")
	addFormatFlag(geoSearchCmd, &geoTargetsFormat)
	_ = geoSearchCmd.MarkFlagRequired("query")

	geoPerfCmd.Flags().StringVar(&geoPerfArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	geoPerfCmd.Flags().StringVar(&geoPerfArgs.DateStart, "date-start", "", "start date YYYY-MM-DD")
	geoPerfCmd.Flags().StringVar(&geoPerfArgs.DateEnd, "date-end", "", "end date YYYY-MM-DD")
	addFormatFlag(geoPerfCmd, &geoPerfFormat)

	geoCmd.AddCommand(geoSearchCmd, geoPerfCmd)
}
