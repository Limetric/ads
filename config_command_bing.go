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
	st := newStyles(out)
	field := func(label, value string) { fmt.Fprintf(out, "%s%s\n", st.field(label, configFieldWidth), value) }
	field("environment", cfg.Environment)
	field("api host", bingCampaignService.url(cfg, ""))
	field("developer token", st.secret(cfg.DeveloperToken))
	field("client id", st.optional(cfg.ClientID))
	field("client secret", st.secret(cfg.ClientSecret))
	field("refresh token", bingRefreshTokenSummary(st, store))
	field("token store", store.location())
	field("manager (customer)", st.optional(cfg.CustomerID))
	field("default account id", st.optional(cfg.DefaultAccountID))
	return nil
}

// bingRefreshTokenSummary renders which sign-in is in effect without revealing
// it. There is no deprecated fallback to describe: Bing's refresh token has
// only ever lived in the token store.
func bingRefreshTokenSummary(st styles, store tokenStoreStatus) string {
	if store.Token == nil {
		return store.describe(bingTokenPolicy)
	}
	return withOrigin(st.secret(store.Token.RefreshToken), store.describe(bingTokenPolicy))
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
		out := cmd.OutOrStdout()
		st := newStyles(out)
		fmt.Fprintf(out, "%s default account ID set to %s %s\n", st.success("✓"), id, st.muted("in "+path))
		return nil
	},
}
