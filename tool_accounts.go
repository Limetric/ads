package main

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"
)

// AccountsArgs has no inputs today, but the struct is the schema anchor and the
// place to add filters (e.g. by manager account) later.
type AccountsArgs struct{}

// AccountsResult lists customer accounts reachable under the login customer.
type AccountsResult struct {
	CustomerIDs []string `json:"customer_ids"`
	// Names maps a customer ID to its descriptive name, when known (the
	// listAccessibleCustomers fallback returns bare IDs only).
	Names map[string]string `json:"names,omitempty"`
	// Message notes when the fallback path was used.
	Message string `json:"message,omitempty"`
}

// runAccounts lists accessible customers. With a login-customer-id it queries
// customer_client under that root (IDs + names); without one — where the old
// query built a malformed customers//googleAds:search path and 404ed (issue
// #14) — it falls back to listAccessibleCustomers, which needs no root.
func runAccounts(ctx context.Context, c *Client, _ AccountsArgs) (AccountsResult, error) {
	root := c.cfg.LoginCustomerID
	if root == "" {
		ids, err := c.ListAccessibleCustomers(ctx)
		if err != nil {
			return AccountsResult{}, toolError("list_accounts", err)
		}
		return AccountsResult{
			CustomerIDs: ids,
			Message:     "GOOGLE_ADS_LOGIN_CUSTOMER_ID is not set — listed the accounts this login can access directly. Set it to an MCC ID to list its client accounts with names.",
		}, nil
	}
	q, err := buildSelect(
		[]string{"customer_client.id", "customer_client.descriptive_name"},
		"customer_client", "customer_client.status = 'ENABLED'", 0,
	)
	if err != nil {
		return AccountsResult{}, err
	}
	rows, err := c.Search(ctx, root, q)
	if err != nil {
		return AccountsResult{}, toolError("list_accounts", err)
	}
	out := AccountsResult{Names: map[string]string{}}
	for _, r := range rows {
		var row struct {
			CustomerClient struct {
				ID              string `json:"id"`
				DescriptiveName string `json:"descriptiveName"`
			} `json:"customerClient"`
		}
		if json.Unmarshal(r, &row) == nil && row.CustomerClient.ID != "" {
			out.CustomerIDs = append(out.CustomerIDs, row.CustomerClient.ID)
			if row.CustomerClient.DescriptiveName != "" {
				out.Names[row.CustomerClient.ID] = row.CustomerClient.DescriptiveName
			}
		}
	}
	if len(out.Names) == 0 {
		out.Names = nil
	}
	return out, nil
}

// --- CLI front-end ---

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List accessible Google Ads accounts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runAccounts(cmd.Context(), client, AccountsArgs{})
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), res)
	},
}
