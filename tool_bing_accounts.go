package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// BingAccountsArgs lists the ad accounts the signed-in user can reach.
type BingAccountsArgs struct {
	OnlyParentAccounts bool `json:"only_parent_accounts,omitempty" jsonschema:"list only accounts directly under the configured manager account, excluding linked accounts under other managers"`
}

// BingAccountsResult is the structured output of bing_list_accounts.
type BingAccountsResult struct {
	Accounts   []BingAccountInfo `json:"accounts"`
	TotalCount int               `json:"total_count"`
	// Message explains which manager account the list came from, since that is
	// what determines its contents.
	Message string `json:"message,omitempty"`
}

// runBingAccounts lists reachable ad accounts. With no manager account
// configured, Microsoft resolves the customer from the signed-in user's own
// credentials, so this works before `ads config bing` has been touched — which
// is exactly when someone needs it, to find the account ID to configure.
func runBingAccounts(ctx context.Context, c *BingClient, args BingAccountsArgs) (BingAccountsResult, error) {
	accounts, err := c.ListAccounts(ctx, args.OnlyParentAccounts)
	if err != nil {
		return BingAccountsResult{}, toolError("list_accounts", err)
	}
	res := BingAccountsResult{Accounts: accounts, TotalCount: len(accounts)}
	if c.cfg.CustomerID == "" {
		res.Message = "BING_ADS_CUSTOMER_ID is not set — listed the accounts this sign-in can reach. Set it to a manager (customer) ID to scope the list to that manager."
	}
	return res, nil
}

// BingAccountInfoArgs shows one ad account's details.
type BingAccountInfoArgs struct {
	AccountID string `json:"account_id,omitempty" jsonschema:"the Microsoft Advertising account ID; omit to use the configured default account"`
}

// BingAccountInfoResult is the structured output of bing_account_info. Only the
// operational fields are surfaced; the underlying payload also carries billing
// and tax data, which no tool here should be handing to an agent.
type BingAccountInfoResult struct {
	AccountID        string `json:"account_id"`
	Name             string `json:"name,omitempty"`
	Number           string `json:"number,omitempty"`
	CurrencyCode     string `json:"currency_code,omitempty"`
	TimeZone         string `json:"time_zone,omitempty"`
	Language         string `json:"language,omitempty"`
	Status           string `json:"status,omitempty"`
	PauseReason      string `json:"pause_reason,omitempty"`
	ParentCustomerID string `json:"parent_customer_id,omitempty"`
	AccountMode      string `json:"account_mode,omitempty"`
}

// runBingAccountInfo reports one account's details — most importantly the
// currency, which is what every Spend and bid figure this platform returns is
// denominated in.
func runBingAccountInfo(ctx context.Context, c *BingClient, args BingAccountInfoArgs) (BingAccountInfoResult, error) {
	accountID, err := c.resolveAccountID(args.AccountID)
	if err != nil {
		return BingAccountInfoResult{}, err
	}
	account, err := c.GetAccount(ctx, accountID)
	if err != nil {
		return BingAccountInfoResult{}, toolError("account_info", err)
	}
	return BingAccountInfoResult{
		AccountID:        accountID,
		Name:             account.Name,
		Number:           account.Number,
		CurrencyCode:     account.CurrencyCode,
		TimeZone:         account.TimeZone,
		Language:         account.Language,
		Status:           account.AccountLifeCycleStatus,
		PauseReason:      account.PauseReason,
		ParentCustomerID: account.ParentCustomerID,
		AccountMode:      account.AccountMode,
	}, nil
}

// --- CLI front-end ---

var (
	bingAccountsArgs    BingAccountsArgs
	bingAccountInfoArgs BingAccountInfoArgs
)

var bingAccountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List accessible Microsoft Advertising accounts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newBingClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBingAccounts(cmd.Context(), client, bingAccountsArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

var bingAccountInfoCmd = &cobra.Command{
	Use:   "account-info",
	Short: "Show account details (name, currency, time zone)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newBingClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBingAccountInfo(cmd.Context(), client, bingAccountInfoArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

// addBingAccountFlag registers the shared --account-id flag, which every Bing
// command accepts and every Bing command falls back to the same default for.
func addBingAccountFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "account-id", "", fmt.Sprintf("Microsoft Advertising account ID (falls back to the configured default; see `ads config %s set-account`)", bingPlatformName))
}

func init() {
	bingAccountsCmd.Flags().BoolVar(&bingAccountsArgs.OnlyParentAccounts, "only-parent-accounts", false, "list only accounts directly under the configured manager account")
	addBingAccountFlag(bingAccountInfoCmd, &bingAccountInfoArgs.AccountID)
}
