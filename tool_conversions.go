package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ConversionsArgs lists the conversion actions configured in an account.
type ConversionsArgs struct {
	CustomerID string `json:"customer_id,omitempty" jsonschema:"the Google Ads customer ID to query (dashes optional); omit to use the configured default customer"`
}

type ConversionsResult struct {
	ConversionActions []json.RawMessage `json:"conversion_actions"`
	TotalCount        int               `json:"total_count"`
	// selectFields carries the SELECT column order for the CLI's --format
	// table/csv rendering; unexported so JSON/MCP output is unchanged.
	selectFields []string
}

func (r ConversionsResult) tableRows() ([]json.RawMessage, []string) {
	return r.ConversionActions, r.selectFields
}

func runConversions(ctx context.Context, c *Client, args ConversionsArgs) (ConversionsResult, error) {
	cid, err := c.resolveCustomerID(args.CustomerID)
	if err != nil {
		return ConversionsResult{}, err
	}
	args.CustomerID = cid
	// category and origin are listed together because that pair is the goal a
	// conversion action belongs to; primary_for_goal says whether it bids.
	query := "SELECT " +
		"conversion_action.id, conversion_action.name, conversion_action.type, " +
		"conversion_action.status, conversion_action.category, conversion_action.origin, " +
		"conversion_action.primary_for_goal, " +
		"conversion_action.value_settings.default_value, conversion_action.counting_type " +
		"FROM conversion_action WHERE conversion_action.status != 'REMOVED' " +
		"ORDER BY conversion_action.name LIMIT 200"

	rows, err := c.Search(ctx, args.CustomerID, query)
	if err != nil {
		return ConversionsResult{}, toolError("conversions", err)
	}
	return ConversionsResult{ConversionActions: rows, TotalCount: len(rows), selectFields: parseSelectFields(query)}, nil
}

// fetchConversionCustomerID returns the account that owns customerID's
// conversion actions and goals. It is customerID itself unless the account uses
// cross-account conversion tracking, in which case it is the tracking manager.
func fetchConversionCustomerID(ctx context.Context, c *Client, customerID string) (string, error) {
	rows, err := c.Search(ctx, customerID, "SELECT customer.conversion_tracking_setting.google_ads_conversion_customer FROM customer")
	if err != nil {
		return "", fmt.Errorf("look up the conversion tracking account of customer %s: %w", customerID, err)
	}
	if len(rows) == 0 {
		return customerID, nil
	}
	var row struct {
		Customer struct {
			ConversionTrackingSetting struct {
				GoogleAdsConversionCustomer string `json:"googleAdsConversionCustomer"`
			} `json:"conversionTrackingSetting"`
		} `json:"customer"`
	}
	if err := json.Unmarshal(rows[0], &row); err != nil {
		return "", fmt.Errorf("decode the conversion tracking account of customer %s: %w", customerID, err)
	}
	owner := strings.TrimPrefix(row.Customer.ConversionTrackingSetting.GoogleAdsConversionCustomer, "customers/")
	if owner == "" {
		return customerID, nil
	}
	return owner, nil
}

// requireConversionCustomer refuses a write that Google only accepts on the
// conversion tracking account, before anything is staged. what names the
// thing being written, for the message.
func requireConversionCustomer(ctx context.Context, c *Client, customerID, what string) error {
	owner, err := fetchConversionCustomerID(ctx, c, customerID)
	if err != nil {
		return err
	}
	if owner != customerID {
		return fmt.Errorf("customer %s uses cross-account conversion tracking managed by account %s — %s can only be changed on that account; re-run with customer_id %s", customerID, owner, what, owner)
	}
	return nil
}

var (
	conversionsArgs   ConversionsArgs
	conversionsFormat string
)

var conversionsCmd = &cobra.Command{
	Use:   "conversions",
	Short: "List conversion actions configured in an account",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := newGoogleClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runConversions(cmd.Context(), client, conversionsArgs)
		if err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), conversionsFormat, res)
	},
}

func init() {
	conversionsCmd.Flags().StringVar(&conversionsArgs.CustomerID, "customer-id", "", "Google Ads customer ID (falls back to the configured default)")
	addFormatFlag(conversionsCmd, &conversionsFormat)
}
