package main

import "context"

// registerBingTools is the Bing platform's Platform.RegisterMCP hook: it
// resolves credentials into a client and registers every Bing tool.
func registerBingTools(ctx context.Context, reg *toolRegistrar) error {
	client, err := newBingClient(ctx)
	if err != nil {
		return err
	}
	addBingTools(reg, client)
	return nil
}

// addBingTools registers Bing's tools against an existing client. Each tool's
// input schema is derived by reflection from its Args struct, so the struct
// tags in tool_bing_*.go are the single source of truth for the schema.
//
// Names here are platform-local; the registrar prefixes them with `bing_`.
// Keep this list in sync with the CLI subcommands in bingPlatform.Commands.
//
// The descriptions carry more than a summary on purpose. An agent picking
// between platforms has to know that Bing's metrics arrive asynchronously and
// that its budgets are plain currency, not micros — those are the two places
// where assuming Google's behaviour produces a wrong answer rather than an
// error.
func addBingTools(reg *toolRegistrar, client *BingClient) {
	addTool(reg, client, "list_accounts",
		"List the Microsoft Advertising accounts this sign-in can reach.",
		runBingAccounts)

	addTool(reg, client, "account_info",
		"Show account details: name, currency code (which every Spend and bid figure for this account is denominated in), time zone, and status.",
		runBingAccountInfo)

	addTool(reg, client, "campaigns",
		"List campaigns with their settings: name, status, type, and daily budget. Settings only — for spend and clicks use bing_campaign_performance.",
		runBingCampaigns)

	addTool(reg, client, "ad_groups",
		"List the ad groups in a campaign, with their status and default CPC bid.",
		runBingAdGroups)

	addTool(reg, client, "keywords",
		"List the keywords in an ad group, with their match type, status, and bid.",
		runBingKeywords)

	addTool(reg, client, "campaign_performance",
		"Campaign spend, clicks, CTR, and conversions (defaults to the last 30 complete days). Microsoft generates reports asynchronously: this waits up to 45 seconds and then returns either rows or a job handle to fetch with bing_report_fetch.",
		runBingCampaignPerformance)

	addTool(reg, client, "keyword_performance",
		"Keyword spend, clicks, CTR, conversions, and quality score (defaults to the last 30 complete days). Returns rows, or a job handle to fetch with bing_report_fetch when the report takes longer than 45 seconds.",
		runBingKeywordPerformance)

	addTool(reg, client, "ad_performance",
		"Ad-level spend, clicks, CTR, and conversions (defaults to the last 30 complete days). Returns rows, or a job handle to fetch with bing_report_fetch when the report takes longer than 45 seconds.",
		runBingAdPerformance)

	addTool(reg, client, "report_fetch",
		"Collect the rows of a report queued earlier, by its job handle. Waits up to 45 seconds; if the report is still running the same handle comes back and can be fetched again.",
		runBingReportFetch)

	addTool(reg, client, "set_campaign_budget",
		"Set a campaign's daily budget, in the account's currency (NOT micros — Microsoft uses plain amounts). Returns a preview + confirm token; pass Confirm to apply.",
		runBingBudgetSet)
}

// The three metric tools share one handler, parameterized by preset. MCP tool
// registration needs a func value per tool, so each preset gets a named wrapper
// rather than a closure built inline — the name is what shows up in a stack
// trace when a report fails.

func runBingCampaignPerformance(ctx context.Context, c *BingClient, args BingPerformanceArgs) (BingReportResult, error) {
	return runBingReportTool(ctx, c, bingCampaignPerformancePreset, args)
}

func runBingKeywordPerformance(ctx context.Context, c *BingClient, args BingPerformanceArgs) (BingReportResult, error) {
	return runBingReportTool(ctx, c, bingKeywordPerformancePreset, args)
}

func runBingAdPerformance(ctx context.Context, c *BingClient, args BingPerformanceArgs) (BingReportResult, error) {
	return runBingReportTool(ctx, c, bingAdPerformancePreset, args)
}
