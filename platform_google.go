package main

import "github.com/spf13/cobra"

// googlePlatform is the Google Ads surface: `ads google …` on the CLI and
// `google_…` over MCP.
//
// Registration happens in a package-level variable initializer, which Go runs
// before any init() function — so main.go's init() sees this platform no matter
// how the files sort.
var googlePlatform = registerPlatform(&Platform{
	Name:  "google",
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
})
