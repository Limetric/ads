package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// googleShowConfig is the Google platform's Platform.ShowConfig hook: the
// resolved settings for `ads config show`, with credentials redacted.
func googleShowConfig(out io.Writer) error {
	cfg, err := loadGoogleConfig(configPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "base url:             %s\n", cfg.BaseURL)
	fmt.Fprintf(out, "developer token:      %s\n", redactSecret(cfg.DeveloperToken))
	fmt.Fprintf(out, "client id:            %s\n", orNone(cfg.ClientID))
	fmt.Fprintf(out, "client secret:        %s\n", redactSecret(cfg.ClientSecret))
	fmt.Fprintf(out, "refresh token:        %s\n", redactSecret(cfg.RefreshToken))
	fmt.Fprintf(out, "login customer id:    %s\n", orNone(cfg.LoginCustomerID))
	fmt.Fprintf(out, "default customer id:  %s\n", orNone(cfg.DefaultCustomerID))
	return nil
}

// googleSetCustomerCmd persists default_customer_id so every Google command can
// omit --customer-id. Note GOOGLE_ADS_CUSTOMER_ID still overrides the file value.
var googleSetCustomerCmd = &cobra.Command{
	Use:   "set-customer <customer-id>",
	Short: "Persist a default Google Ads customer ID so --customer-id can be omitted",
	Long:  "Write default_customer_id to the ads config file (the --config path if given,\notherwise the default location — see `ads config path`).\n\nOther keys in the file are preserved, but comments are not: the file is\nre-encoded from its parsed form. GOOGLE_ADS_CUSTOMER_ID overrides the file value.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := normalizeCustomerID(args[0])
		if !validCustomerIDFormat(id) {
			return fmt.Errorf("invalid customer ID %q — expected 10 digits, e.g. 123-456-7890", args[0])
		}
		path, err := writableConfigPath(configPath)
		if err != nil {
			return err
		}
		if err := upsertConfigKey(path, "default_customer_id", id); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "default customer ID set to %s in %s\n", id, path)
		return nil
	},
}

// validCustomerIDFormat reports whether id (already normalized) looks like a
// Google Ads customer ID: exactly 10 digits.
func validCustomerIDFormat(id string) bool {
	if len(id) != 10 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
