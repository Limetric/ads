package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// bingShowConfig is the Bing platform's Platform.ShowConfig hook: the resolved
// settings for `ads config show`, with credentials redacted.
func bingShowConfig(out io.Writer) error {
	cfg, err := loadBingConfig(configPath)
	if err != nil {
		return err
	}
	// `config show` describes the setup; it must not alter it, so the store is
	// read directly rather than through resolution.
	store := describeTokenStore(bingTokenPolicy.Platform)
	fmt.Fprintf(out, "environment:          %s\n", cfg.Environment)
	fmt.Fprintf(out, "api host:             %s\n", bingCampaignService.url(cfg, ""))
	fmt.Fprintf(out, "developer token:      %s\n", redactSecret(cfg.DeveloperToken))
	fmt.Fprintf(out, "client id:            %s\n", orNone(cfg.ClientID))
	fmt.Fprintf(out, "client secret:        %s\n", redactSecret(cfg.ClientSecret))
	fmt.Fprintf(out, "refresh token:        %s\n", bingRefreshTokenSummary(store))
	fmt.Fprintf(out, "token store:          %s\n", store.location())
	fmt.Fprintf(out, "manager (customer):   %s\n", orNone(cfg.CustomerID))
	fmt.Fprintf(out, "default account id:   %s\n", orNone(cfg.DefaultAccountID))
	return nil
}

// bingRefreshTokenSummary renders which sign-in is in effect without revealing
// it. There is no deprecated fallback to describe: Bing's refresh token has
// only ever lived in the token store.
func bingRefreshTokenSummary(store tokenStoreStatus) string {
	if store.Token == nil {
		return store.describe(bingTokenPolicy)
	}
	return withOrigin(redactSecret(store.Token.RefreshToken), store.describe(bingTokenPolicy))
}

// bingSetAccountCmd persists default_account_id so every Bing command can omit
// --account-id. BING_ADS_ACCOUNT_ID still overrides the file value.
var bingSetAccountCmd = &cobra.Command{
	Use:   "set-account <account-id>",
	Short: "Persist a default Microsoft Advertising account ID so --account-id can be omitted",
	Long:  "Write default_account_id to the [bing] table of the ads config file (the --config\npath if given, otherwise the default location — see `ads config path`).\n\nOther keys in the file are preserved, but comments are not: the file is\nre-encoded from its parsed form. BING_ADS_ACCOUNT_ID overrides the file value.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := normalizeBingID(args[0])
		if !validBingID(id) {
			return fmt.Errorf("invalid account ID %q — expected digits, e.g. 123456789 (find yours with `ads %s accounts`)", args[0], bingPlatformName)
		}
		path, err := writableConfigPath(configPath)
		if err != nil {
			return err
		}
		if err := mergeBingConfigValues(path, map[string]string{"default_account_id": id}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "default account ID set to %s in %s\n", id, path)
		return nil
	},
}
