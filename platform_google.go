package main

import (
	"context"

	"github.com/spf13/cobra"
)

// googlePlatformName is Google's namespace. It is a constant rather than a
// read of googlePlatform.Name so the write path can stamp it on a staged
// mutation without the package-level initializers forming a cycle (the platform
// value refers to the commands, which refer back to the write path).
const googlePlatformName = "google"

// googlePlatform is the Google Ads surface: `ads google …` on the CLI and
// `google_…` over MCP.
//
// Registration happens in a package-level variable initializer, which Go runs
// before any init() function — so main.go's init() sees this platform no matter
// how the files sort.
var googlePlatform = registerPlatform(&Platform{
	Name:  googlePlatformName,
	Title: "Google Ads",
	Short: "Google Ads campaign management",

	// Keep this list in sync with addGoogleTools in mcp_google.go — every tool
	// is exposed both ways, backed by the same handler.
	Commands: []*cobra.Command{
		searchCmd,
		accountsCmd,
		budgetCmd,
		campaignsCmd,
		adsCmd,
		keywordsCmd,
		reportCmd,
		geoCmd,
		conversionsCmd,
		policyCmd,
		extensionsCmd,
		keywordIdeasCmd,
		keywordForecastsCmd,
		recommendationsCmd,
		assetCmd,
		pauseCmd,
		enableCmd,
		removeCmd,
		scheduleCmd,
		biddingCmd,
		audienceCmd,
		adGroupCmd,
		adCmd,
		extensionCmd,
		pmaxCmd,
		campaignCmd,
	},

	Login:          googleLoginCmd,
	ConfigCommands: []*cobra.Command{googleSetCustomerCmd},

	RegisterMCP: registerGoogleTools,
	ShowConfig:  googleShowConfig,
	Doctor:      googleDoctor,
	NewApplier:  func(ctx context.Context) (mutationApplier, error) { return newGoogleClient(ctx) },
	Configured:  googleConfigured,
})

// googleConfigured reports whether anything Google-specific has been set up:
// a developer token, an OAuth client, or a saved sign-in. Any one of them means
// the user intends to use Google and wants `ads doctor` to say what is missing;
// none of them means they haven't started, and a plain `ads doctor` should not
// report that as a broken setup.
func googleConfigured() bool {
	cfg, err := loadGoogleConfig(configPath)
	if err != nil {
		// A config file that cannot be read is a problem doctor should report,
		// not one it should skip over.
		return true
	}
	if cfg.DeveloperToken != "" || cfg.ClientID != "" || cfg.ClientSecret != "" {
		return true
	}
	if tok, err := readStoredToken(googleTokenPolicy.Platform); err == nil && tok != nil {
		return true
	}
	return false
}
