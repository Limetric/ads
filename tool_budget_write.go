package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

// BudgetSetArgs updates a campaign budget's daily amount. This is a *write*
// tool, so it follows the confirm-token flow: the first call (Confirm == "")
// stages a preview; the second call (Confirm == token) applies it.
type BudgetSetArgs struct {
	CustomerID   string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID that owns the budget; omit to use the configured default customer"`
	BudgetID     string `json:"budget_id" jsonschema:"the campaign budget ID to update"`
	AmountMicros int64  `json:"amount_micros" jsonschema:"the new daily budget in micros (1 unit of currency = 1,000,000 micros)"`
	Confirm      string `json:"confirm,omitempty" jsonschema:"a confirm token returned by a previous preview call; omit to preview"`
}

func (a BudgetSetArgs) operations() []any {
	resource := fmt.Sprintf("customers/%s/campaignBudgets/%s", normalizeCustomerID(a.CustomerID), a.BudgetID)
	return []any{
		map[string]any{
			"campaignBudgetOperation": map[string]any{
				"update": map[string]any{
					"resourceName": resource,
					"amountMicros": a.AmountMicros,
				},
				"updateMask": "amountMicros",
			},
		},
	}
}

// runBudgetSet stages or applies a campaign-budget update via the shared
// preview/confirm helpers, so partial failures fail the apply (issue #7).
func runBudgetSet(ctx context.Context, c *Client, args BudgetSetArgs) (WriteResult, error) {
	const tool = "set_campaign_budget"
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return WriteResult{}, err
	}
	// operations() builds resource names from args.CustomerID, so the resolved
	// default has to land back in args.
	args.CustomerID = cid
	if args.BudgetID == "" {
		return WriteResult{}, fmt.Errorf("budget_id is required")
	}
	// Blocked-op check runs before the confirm branch so an operation blocked
	// between preview and confirm cannot still be applied with its token.
	cfg := loadSafetyConfig()
	if err := checkBlockedOperation(tool, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	if args.AmountMicros <= 0 {
		return WriteResult{}, fmt.Errorf("amount_micros must be positive (1 unit of currency = 1,000,000 micros)")
	}
	// Guard: reject daily budgets above the configured cap (default $50/day).
	if err := checkBudgetCap(float64(args.AmountMicros)/1_000_000.0, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if _, err := numericID("budget_id", args.BudgetID); err != nil {
		return WriteResult{}, err
	}
	summary := fmt.Sprintf("Set budget %s to %d micros", args.BudgetID, args.AmountMicros)
	// Budget increases over 50% take a second confirmation (issue #12). The
	// current amount is fetched best-effort; when it can't be determined the
	// write stays single-confirm.
	if cur := fetchBudgetAmountMicros(ctx, c, cid, args.BudgetID); cur > 0 {
		curUnits, propUnits := float64(cur)/1_000_000.0, float64(args.AmountMicros)/1_000_000.0
		if requiresDoubleConfirmation(tool, &curUnits, &propUnits) {
			return previewMutateDouble(tool, cid, summary, args.operations())
		}
	}
	return previewMutate(tool, cid, summary, args.operations())
}

// fetchBudgetAmountMicros looks up a campaign budget's current daily amount.
// Best-effort: 0 when it cannot be determined.
func fetchBudgetAmountMicros(ctx context.Context, c *Client, customerID, budgetID string) int64 {
	if c == nil {
		return 0
	}
	q := fmt.Sprintf("SELECT campaign_budget.amount_micros FROM campaign_budget WHERE campaign_budget.id = %s", budgetID)
	rows, err := c.Search(ctx, customerID, q)
	if err != nil || len(rows) == 0 {
		return 0
	}
	var row struct {
		CampaignBudget struct {
			AmountMicros any `json:"amountMicros"`
		} `json:"campaignBudget"`
	}
	if json.Unmarshal(rows[0], &row) != nil {
		return 0
	}
	switch v := row.CampaignBudget.AmountMicros.(type) {
	case string:
		micros, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0
		}
		return micros
	case float64:
		return int64(v)
	default:
		return 0
	}
}

// --- CLI front-end ---

var budgetArgs BudgetSetArgs

var budgetCmd = &cobra.Command{
	Use:   "budget",
	Short: "Manage campaign budgets",
}

var budgetSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a campaign budget's daily amount (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBudgetSet(cmd.Context(), client, budgetArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	budgetSetCmd.Flags().StringVar(&budgetArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	budgetSetCmd.Flags().StringVar(&budgetArgs.BudgetID, "budget-id", "", "campaign budget ID (required)")
	budgetSetCmd.Flags().Int64Var(&budgetArgs.AmountMicros, "amount-micros", 0, "new daily budget in micros")
	budgetSetCmd.Flags().StringVar(&budgetArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = budgetSetCmd.MarkFlagRequired("budget-id")
	budgetCmd.AddCommand(budgetSetCmd)
}
