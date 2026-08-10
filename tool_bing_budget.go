package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// BingBudgetSetArgs updates a campaign's daily budget. This is a *write* tool,
// so it follows the confirm-token flow: the first call (Confirm == "") stages a
// preview; the second call (Confirm == token) applies it.
//
// Microsoft holds the daily budget on the campaign itself, not on a separate
// budget entity as Google does — so this takes a campaign ID, and there is no
// bing_delete_campaign_budget to match Google's.
type BingBudgetSetArgs struct {
	AccountID  string `json:"account_id,omitempty" jsonschema:"the Microsoft Advertising account that owns the campaign; omit to use the configured default account"`
	CampaignID string `json:"campaign_id" jsonschema:"the campaign whose daily budget to set"`
	// DailyBudget is a plain currency amount. Microsoft has no micros: a
	// $25.50 budget is 25.50, in the account's currency (bing_account_info
	// reports which).
	DailyBudget float64 `json:"daily_budget" jsonschema:"the new daily budget in the account's currency (not micros), e.g. 25.50"`
	Confirm     string  `json:"confirm,omitempty" jsonschema:"a confirm token returned by a previous preview call; omit to preview"`
}

// runBingBudgetSet stages or applies a campaign daily-budget update.
func runBingBudgetSet(ctx context.Context, c *BingClient, args BingBudgetSetArgs) (WriteResult, error) {
	const tool = "set_campaign_budget"
	accountID, err := c.resolveAccountID(args.AccountID)
	if err != nil {
		return WriteResult{}, err
	}
	// The blocked-op check runs before the confirm branch so an operation
	// blocked between preview and confirm cannot still be applied with its
	// token.
	cfg := loadSafetyConfig()
	if err := checkBlockedOperation(tool, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if args.Confirm != "" {
		return applyConfirmed(ctx, c, tool, args.Confirm)
	}
	campaignID, err := bingEntityID("campaign_id", args.CampaignID)
	if err != nil {
		return WriteResult{}, err
	}
	if args.DailyBudget <= 0 {
		return WriteResult{}, fmt.Errorf("daily_budget must be positive (a plain currency amount, e.g. 25.50 — Microsoft does not use micros)")
	}
	// Guard: reject daily budgets above the configured cap (default $50/day).
	if err := checkBudgetCap(args.DailyBudget, cfg); err != nil {
		return WriteResult{}, toolError(tool, err)
	}

	// The current campaign is read before staging, for three reasons: to fail
	// now rather than at confirm time if it does not exist, to show the user
	// what the budget is changing *from*, and to catch a shared budget, whose
	// amount this call cannot change.
	campaign, err := c.GetCampaign(ctx, accountID, campaignID)
	if err != nil {
		return WriteResult{}, toolError(tool, err)
	}
	if campaign == nil {
		return WriteResult{}, toolError(tool, fmt.Errorf("campaign %s was not found in account %s — check the ID with `ads %s campaigns`", campaignID, accountID, bingPlatformName))
	}
	if campaign.BudgetID != nil && *campaign.BudgetID != "" {
		return WriteResult{}, toolError(tool, fmt.Errorf("campaign %s draws on shared budget %s — its daily amount belongs to the budget, not the campaign, and ads cannot change shared budgets yet. Change it in the Microsoft Advertising UI, or move the campaign to its own budget",
			campaignID, *campaign.BudgetID))
	}

	operation := map[string]any{"Id": campaignID, "DailyBudget": args.DailyBudget}
	summary := bingBudgetSummary(campaign, args.DailyBudget)
	write := pendingWrite{
		Tool:          tool,
		CustomerID:    accountID,
		Summary:       summary,
		Dispatch:      dispatchBingUpdateCampaign,
		Operations:    []any{operation},
		BudgetAmounts: []float64{args.DailyBudget},
	}
	// Budget increases over 50% take a second confirmation (issue #12).
	if campaign.DailyBudget != nil && *campaign.DailyBudget > 0 {
		current := *campaign.DailyBudget
		if requiresDoubleConfirmation(tool, &current, &args.DailyBudget) {
			write.RequiresDouble = true
		}
	}
	return previewBingWrite(write)
}

// bingBudgetSummary describes the change for the preview.
//
// It spells out both what changes and what does not. Campaign updates are
// partial — an unsent field keeps its value — but that is emphatically not true
// across this API: ad extensions and the customer management entities are full
// replacements, where omitting a field deletes it. A preview that only ever
// said "here is the new value" would read identically in both cases, so this
// one names the fields being written.
func bingBudgetSummary(campaign *BingCampaign, proposed float64) string {
	var b strings.Builder
	name := campaign.Name
	if name == "" {
		name = "campaign " + campaign.ID
	}
	fmt.Fprintf(&b, "Set the daily budget of %q (campaign %s) to %.2f", name, campaign.ID, proposed)
	if campaign.DailyBudget != nil {
		fmt.Fprintf(&b, ", from %.2f", *campaign.DailyBudget)
	} else {
		b.WriteString(", which currently has no campaign-level daily budget")
	}
	b.WriteString(" (account currency — see bing_account_info).\n")
	fmt.Fprintf(&b, "Fields written: Id, DailyBudget. Everything else on the campaign — name, status, bid strategy, targeting — is left untouched: Microsoft applies a partial update for campaigns.")
	if campaign.BudgetType != "" {
		fmt.Fprintf(&b, "\nBudget type stays %s.", campaign.BudgetType)
	}
	return b.String()
}

// --- CLI front-end ---

var bingBudgetArgs BingBudgetSetArgs

var bingBudgetCmd = &cobra.Command{
	Use:   "budget",
	Short: "Manage campaign daily budgets",
}

var bingBudgetSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a campaign's daily budget (previews first; --confirm to apply)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newBingClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBingBudgetSet(cmd.Context(), client, bingBudgetArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	addBingAccountFlag(bingBudgetSetCmd, &bingBudgetArgs.AccountID)
	bingBudgetSetCmd.Flags().StringVar(&bingBudgetArgs.CampaignID, "campaign-id", "", "campaign ID (required)")
	bingBudgetSetCmd.Flags().Float64Var(&bingBudgetArgs.DailyBudget, "daily-budget", 0, "new daily budget in the account's currency")
	bingBudgetSetCmd.Flags().StringVar(&bingBudgetArgs.Confirm, "confirm", "", "confirm token from a previous preview")
	_ = bingBudgetSetCmd.MarkFlagRequired("campaign-id")
	bingBudgetCmd.AddCommand(bingBudgetSetCmd)
}
