package main

import (
	"context"

	"github.com/spf13/cobra"
)

// bingPlatformName is Bing's namespace. Like Google's it is a constant so the
// write path can name the platform without the package-level initializers
// forming a cycle.
const bingPlatformName = "bing"

// bingPlatform is the Microsoft Advertising surface: `ads bing …` on the CLI
// and `bing_…` over MCP.
//
// Bing exposes what Bing has. There is no `bing_search`, because Microsoft has
// no query language, and no `bing_keyword_ideas`, because the Ad Insight
// equivalent is not part of v1 — a tool that answered "unsupported" would be
// worse than its absence, since an agent has to call it to find out.
var bingPlatform = registerPlatform(&Platform{
	Name:  bingPlatformName,
	Title: "Microsoft Advertising",
	Short: "Microsoft Advertising (Bing Ads) campaign management",

	// Keep this list in sync with addBingTools in mcp_bing.go — every tool is
	// exposed both ways, backed by the same handler.
	Commands: []*cobra.Command{
		bingAccountsCmd,
		bingAccountInfoCmd,
		bingCampaignsCmd,
		bingAdGroupsCmd,
		bingKeywordsCmd,
		bingCampaignPerformanceCmd,
		bingKeywordPerformanceCmd,
		bingAdPerformanceCmd,
		bingReportCmd,
		bingBudgetCmd,
	},

	Login:          bingLoginCmd,
	ConfigCommands: []*cobra.Command{bingSetAccountCmd},

	RegisterMCP: registerBingTools,
	ShowConfig:  bingShowConfig,
	Doctor:      bingDoctor,
	NewApplier:  func(ctx context.Context) (mutationApplier, error) { return newBingClient(ctx) },
	Configured:  bingConfigured,
})

// bingConfigured reports whether anything Bing-specific has been set up, so a
// plain `ads doctor` skips Bing for someone who only uses Google.
func bingConfigured() bool {
	cfg, err := loadBingConfig(configPath)
	if err != nil {
		// A config file that cannot be read is a problem doctor should report,
		// not one it should skip over.
		return true
	}
	return cfg.configured()
}

// newBingClient builds a Microsoft Advertising client from the resolved
// configuration (the global --config flag plus the environment). Shared by
// every `ads bing` subcommand and by the MCP server's bing_* tools.
func newBingClient(ctx context.Context) (*BingClient, error) {
	cfg, err := loadBingConfig(configPath)
	if err != nil {
		return nil, err
	}
	return NewBingClient(ctx, cfg)
}
