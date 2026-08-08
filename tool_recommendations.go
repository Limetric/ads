package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// This file lists active recommendations (read) and applies or dismisses them
// (writes). Apply and dismiss route through dedicated RPCs (not
// googleAds:mutate) via the confirm flow's dispatch field.

// RecommendationsArgs lists active (non-dismissed) recommendations.
type RecommendationsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
}

type RecommendationsResult struct {
	Recommendations []json.RawMessage `json:"recommendations"`
	TotalCount      int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r RecommendationsResult) tableRows() ([]json.RawMessage, []string) {
	return r.Recommendations, r.selectFields
}

func runListRecommendations(ctx context.Context, c *Client, args RecommendationsArgs) (RecommendationsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return RecommendationsResult{}, err
	}
	args.CustomerID = cid
	query := "SELECT " +
		"recommendation.type, recommendation.impact, recommendation.campaign, recommendation.resource_name " +
		"FROM recommendation WHERE recommendation.dismissed = FALSE LIMIT 50"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return RecommendationsResult{}, toolError("recommendations", err)
	}
	return RecommendationsResult{Recommendations: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// RecommendationActionArgs applies or dismisses a single recommendation. It is
// a write tool: omit Confirm to preview, pass the returned token to apply.
type RecommendationActionArgs struct {
	CustomerID       string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the recommendation; omit to use the configured default customer"`
	RecommendationID string `json:"recommendation_id" jsonschema:"the recommendation ID to act on"`
	Confirm          string `json:"confirm,omitempty" jsonschema:"a confirm token from a previous preview; omit to preview"`
}

func runApplyRecommendation(ctx context.Context, c *Client, args RecommendationActionArgs) (WriteResult, error) {
	return recommendationAction(ctx, c, args, "apply_recommendation", dispatchApplyRecommendation)
}

func runDismissRecommendation(ctx context.Context, c *Client, args RecommendationActionArgs) (WriteResult, error) {
	return recommendationAction(ctx, c, args, "dismiss_recommendation", dispatchDismissRecommendation)
}

// recommendationAction stages or applies an apply/dismiss recommendation write.
func recommendationAction(ctx context.Context, c *Client, args RecommendationActionArgs, tool, dispatch string) (WriteResult, error) {
	resolved, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	args.CustomerID = resolved
	if args.RecommendationID == "" {
		return WriteResult{}, fmt.Errorf("recommendation_id is required")
	}
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	cid := normalizeCustomerID(args.CustomerID)
	resourceName := fmt.Sprintf("customers/%s/recommendations/%s", cid, args.RecommendationID)
	verb := "Apply"
	if dispatch == dispatchDismissRecommendation {
		verb = "Dismiss"
	}
	summary := fmt.Sprintf("%s recommendation %s", verb, args.RecommendationID)
	p, err := stageDispatch(tool, cid, summary, dispatch, nil, []string{resourceName})
	if err != nil {
		return WriteResult{}, err
	}
	return previewResult(p), nil
}

// --- CLI front-end ---

var (
	recommendationsArgs   RecommendationsArgs
	recommendationsFormat string
	applyRecArgs          RecommendationActionArgs
	dismissRecArgs        RecommendationActionArgs
)

var recommendationsCmd = &cobra.Command{
	Use:   "recommendations",
	Short: "List, apply, or dismiss account recommendations",
}

var recommendationsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active (non-dismissed) recommendations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runListRecommendations(cmd.Context(), client, recommendationsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), recommendationsFormat, res)
	},
}

var recommendationsApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a recommendation (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runApplyRecommendation(cmd.Context(), client, applyRecArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var recommendationsDismissCmd = &cobra.Command{
	Use:   "dismiss",
	Short: "Dismiss a recommendation (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDismissRecommendation(cmd.Context(), client, dismissRecArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	recommendationsListCmd.Flags().StringVar(&recommendationsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(recommendationsListCmd, &recommendationsFormat)

	for _, b := range []struct {
		cmd  *cobra.Command
		args *RecommendationActionArgs
	}{{recommendationsApplyCmd, &applyRecArgs}, {recommendationsDismissCmd, &dismissRecArgs}} {
		b.cmd.Flags().StringVar(&b.args.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
		b.cmd.Flags().StringVar(&b.args.RecommendationID, "recommendation-id", "", "recommendation ID (required)")
		b.cmd.Flags().StringVar(&b.args.Confirm, "confirm", "", "confirm token from a previous preview")
		_ = b.cmd.MarkFlagRequired("recommendation-id")
	}

	recommendationsCmd.AddCommand(recommendationsListCmd, recommendationsApplyCmd, recommendationsDismissCmd)
}
