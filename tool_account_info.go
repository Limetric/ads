package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// account_info reads the customer resource: the account's name, currency, and
// time zone. Cost fields across every read tool are micros of the account
// currency, so this is where an agent (or human) learns which currency that is
// (issue #17).

// AccountInfoArgs identifies the account to describe.
type AccountInfoArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to describe (dashes optional); omit to use the configured default customer"`
}

// AccountInfoResult is the account's descriptive metadata.
type AccountInfoResult struct {
	CustomerID      string `json:"customer_id"`
	DescriptiveName string `json:"descriptive_name,omitempty"`
	// CurrencyCode is the ISO 4217 code (e.g. EUR) that all *_micros cost
	// fields are denominated in.
	CurrencyCode       string `json:"currency_code,omitempty"`
	TimeZone           string `json:"time_zone,omitempty"`
	IsManager          bool   `json:"is_manager"`
	IsTestAccount      bool   `json:"is_test_account"`
	AutoTaggingEnabled bool   `json:"auto_tagging_enabled"`
}

func runAccountInfo(ctx context.Context, c *Client, args AccountInfoArgs) (AccountInfoResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return AccountInfoResult{}, err
	}
	query := "SELECT " +
		"customer.descriptive_name, customer.currency_code, customer.time_zone, " +
		"customer.manager, customer.test_account, customer.auto_tagging_enabled " +
		"FROM customer LIMIT 1"

	rows, err := c.Search(ctx, cid, query)
	if err != nil {
		return AccountInfoResult{}, toolError("account_info", err)
	}
	if len(rows) == 0 {
		return AccountInfoResult{}, fmt.Errorf("account_info: customer %s returned no data — check the customer ID", cid)
	}
	var row struct {
		Customer struct {
			DescriptiveName    string `json:"descriptiveName"`
			CurrencyCode       string `json:"currencyCode"`
			TimeZone           string `json:"timeZone"`
			Manager            bool   `json:"manager"`
			TestAccount        bool   `json:"testAccount"`
			AutoTaggingEnabled bool   `json:"autoTaggingEnabled"`
		} `json:"customer"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return AccountInfoResult{}, fmt.Errorf("account_info: decode customer row: %w", err)
	}
	return AccountInfoResult{
		CustomerID:         cid,
		DescriptiveName:    row.Customer.DescriptiveName,
		CurrencyCode:       row.Customer.CurrencyCode,
		TimeZone:           row.Customer.TimeZone,
		IsManager:          row.Customer.Manager,
		IsTestAccount:      row.Customer.TestAccount,
		AutoTaggingEnabled: row.Customer.AutoTaggingEnabled,
	}, nil
}

// --- CLI front-end ---

var accountInfoArgs AccountInfoArgs

var accountsInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show account details (name, currency, time zone)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runAccountInfo(cmd.Context(), client, accountInfoArgs)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}

func init() {
	accountsInfoCmd.Flags().StringVar(&accountInfoArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	accountsCmd.AddCommand(accountsInfoCmd)
}
