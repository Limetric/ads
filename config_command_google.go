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
	// `config show` describes the setup; it must not alter it. So it reads the
	// store directly instead of calling resolveRefreshToken, which would
	// migrate a deprecated seed and rewrite the very file it is describing.
	store := describeTokenStore(googleTokenPolicy.Platform)
	fmt.Fprintf(out, "base url:             %s\n", cfg.BaseURL)
	fmt.Fprintf(out, "developer token:      %s\n", redactSecret(cfg.DeveloperToken))
	fmt.Fprintf(out, "client id:            %s\n", orNone(cfg.ClientID))
	fmt.Fprintf(out, "client secret:        %s\n", redactSecret(cfg.ClientSecret))
	fmt.Fprintf(out, "refresh token:        %s\n", googleRefreshTokenSummary(cfg, store))
	fmt.Fprintf(out, "token store:          %s\n", store.location())
	fmt.Fprintf(out, "login customer id:    %s\n", orNone(cfg.LoginCustomerID))
	fmt.Fprintf(out, "default customer id:  %s\n", orNone(cfg.DefaultCustomerID))
	return nil
}

// googleRefreshTokenSummary renders which sign-in is actually in effect, and
// where it comes from, without revealing it.
func googleRefreshTokenSummary(cfg *GoogleConfig, store tokenStoreStatus) string {
	switch {
	case store.ReadErr != nil:
		// A store that cannot be read makes every real command fail on it.
		// Falling back to a deprecated seed here would describe a setup that
		// does not exist, in the one command run to find out what is wrong.
		return store.describe(googleTokenPolicy)
	case store.Token != nil:
		return withOrigin(redactSecret(store.Token.RefreshToken), store.describe(googleTokenPolicy))
	}
	seeds := presentSeeds(cfg.refreshTokenSeeds())
	if len(seeds) == 0 {
		return redactSecret("")
	}
	return withOrigin(redactSecret(seeds[0].Value),
		"from "+seeds[0].Origin+" (deprecated — it will be saved to the token store on first use)")
}

// withOrigin appends where a credential came from, when that is known.
func withOrigin(value, origin string) string {
	if origin == "" {
		return value
	}
	return value + " — " + origin
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
