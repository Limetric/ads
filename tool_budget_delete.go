package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// DeleteBudgetArgs removes an unused campaign budget. The budget must not be
// referenced by an ENABLED or PAUSED campaign, and deletion requires two
// confirmations because it is destructive.
type DeleteBudgetArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the budget; omit to use the configured default customer"`
	BudgetID   string `json:"budget_id" jsonschema:"the unused campaign budget ID to delete"`
	Confirm    string `json:"confirm,omitempty" jsonschema:"a confirm token returned by a previous preview call; omit to preview"`
}

func (a DeleteBudgetArgs) operations() []any {
	resource := fmt.Sprintf("customers/%s/campaignBudgets/%s", normalizeCustomerID(a.CustomerID), a.BudgetID)
	return []any{map[string]any{
		"campaignBudgetOperation": map[string]any{"remove": resource},
	}}
}

// runDeleteBudget stages or applies removal of an unused campaign budget.
// The reference count is checked before staging and every confirmation so a
// campaign cannot start using the budget between preview and deletion.
func runDeleteBudget(ctx context.Context, c *Client, args DeleteBudgetArgs) (WriteResult, error) {
	const tool = "delete_campaign_budget"
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	args.CustomerID = cid
	if args.BudgetID == "" {
		return WriteResult{}, fmt.Errorf("budget_id is required")
	}
	if _, err := numericID("budget_id", args.BudgetID); err != nil {
		return WriteResult{}, err
	}
	if err := checkBlockedOperation(tool, loadSafetyConfig()); err != nil {
		return WriteResult{}, toolError(tool, err)
	}

	referenceCount, err := fetchBudgetReferenceCount(ctx, c, cid, args.BudgetID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if referenceCount > 0 {
		return WriteResult{}, fmt.Errorf("budget %s is still used by %d enabled or paused campaign(s) — pause, remove, or reassign those campaigns before deleting the budget", args.BudgetID, referenceCount)
	}

	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	return previewMutate(tool, cid, fmt.Sprintf("Delete unused campaign budget %s", args.BudgetID), args.operations())
}

// fetchBudgetReferenceCount returns the number of ENABLED or PAUSED campaigns
// using a budget. Unlike the best-effort amount lookup used by budget updates,
// failure to retrieve this value rejects deletion rather than failing open.
func fetchBudgetReferenceCount(ctx context.Context, c *Client, customerID, budgetID string) (int64, error) {
	q := fmt.Sprintf("SELECT campaign_budget.reference_count FROM campaign_budget WHERE campaign_budget.id = %s", budgetID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil {
		return 0, fmt.Errorf("look up budget %s reference count: %w", budgetID, err)
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("budget %s was not found or is not accessible", budgetID)
	}
	var row struct {
		CampaignBudget struct {
			ReferenceCount any `json:"referenceCount"`
		} `json:"campaignBudget"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return 0, fmt.Errorf("decode budget %s reference count: %w", budgetID, err)
	}
	switch value := row.CampaignBudget.ReferenceCount.(type) {
	case string:
		count, err := strconv.ParseInt(value, 10, 64)
		if err != nil || count < 0 {
			return 0, fmt.Errorf("invalid reference count for budget %s", budgetID)
		}
		return count, nil
	case float64:
		if value < 0 || value != float64(int64(value)) {
			return 0, fmt.Errorf("invalid reference count for budget %s", budgetID)
		}
		return int64(value), nil
	default:
		return 0, fmt.Errorf("budget %s did not return a reference count", budgetID)
	}
}

// --- CLI front-end ---

var deleteBudgetArgs DeleteBudgetArgs

var budgetDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete an unused campaign budget (previews first; two confirmations required)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runDeleteBudget(cmd.Context(), client, deleteBudgetArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	budgetDeleteCmd.Flags().StringVar(&deleteBudgetArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	budgetDeleteCmd.Flags().StringVar(&deleteBudgetArgs.BudgetID, "budget-id", "", "unused campaign budget ID (required)")
	budgetDeleteCmd.Flags().StringVar(&deleteBudgetArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = budgetDeleteCmd.MarkFlagRequired("budget-id")
	budgetCmd.AddCommand(budgetDeleteCmd)
}
