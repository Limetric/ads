package main

import "context"

// registerGoogleTools is the Google platform's Platform.RegisterMCP hook: it
// resolves credentials into a client and registers every Google tool.
func registerGoogleTools(ctx context.Context, reg *toolRegistrar) error {
	client, err := newGoogleClient(ctx)
	if err != nil {
		return err
	}
	addGoogleTools(reg, client)
	return nil
}

// addGoogleTools registers Google's tools against an existing client. Each
// tool's input schema is derived by reflection from its Args struct, so the
// struct tags in tool_*.go are the single source of truth for the schema.
//
// Names here are platform-local; the registrar prefixes them with `google_`.
// Keep this list in sync with the CLI subcommands in googlePlatform.Commands.
func addGoogleTools(reg *toolRegistrar, client *Client) {
	addTool(reg, client, "search",
		"Run a GAQL query against a Google Ads account and return the rows.",
		runSearch)

	addTool(reg, client, "list_accounts",
		"List the Google Ads accounts accessible to the authenticated user.",
		runAccounts)

	addTool(reg, client, "account_info",
		"Show account details: descriptive name, currency code (which all *_micros cost fields are denominated in), time zone, and account flags.",
		runAccountInfo)

	addTool(reg, client, "set_campaign_budget",
		"Update a campaign's daily budget. Returns a preview + confirm token; pass Confirm to apply.",
		runBudgetSet)

	addTool(reg, client, "delete_campaign_budget",
		"Delete an unused campaign budget. Returns a preview + confirm token; two confirmations are required to apply.",
		runDeleteBudget)

	addTool(reg, client, "campaigns",
		"Show campaign-level performance metrics (cost, clicks, conversions, CTR, CPA) for non-removed campaigns (defaults to the last 30 days).",
		runCampaigns)

	addTool(reg, client, "ads",
		"Show ad-level performance metrics for non-removed ads, ordered by cost (defaults to the last 30 days).",
		runAds)

	addTool(reg, client, "keyword_performance",
		"Show keyword-level performance metrics (impressions, clicks, CTR, CPC, cost, conversions, quality score; defaults to the last 30 days).",
		runKeywordPerformance)

	addTool(reg, client, "search_terms",
		"Show the actual search terms that triggered ads (defaults to the last 30 days).",
		runSearchTerms)

	addTool(reg, client, "negative_keywords",
		"List campaign-level negative keywords.",
		runNegativeKeywords)

	addTool(reg, client, "report",
		"Run an arbitrary GAQL query and return results as json (default), table, or csv.",
		runReport)

	addTool(reg, client, "geo_targets",
		"Search geo target constants by name to find location IDs for geo-targeting.",
		runGeoTargets)

	addTool(reg, client, "geo_performance",
		"Show geographic performance for campaigns (defaults to the last 30 days).",
		runGeoPerformance)

	addTool(reg, client, "conversions",
		"List all conversion actions configured in the account.",
		runConversions)

	addTool(reg, client, "policy",
		"List ads with policy issues (disapproved, limited, under review).",
		runPolicy)

	addTool(reg, client, "extensions",
		"List campaign-level extensions (sitelinks, callouts, structured snippets).",
		runExtensions)

	addTool(reg, client, "keyword_ideas",
		"Discover keyword ideas from seed keywords using the Keyword Planner.",
		runDiscoverKeywords)

	addTool(reg, client, "keyword_forecasts",
		"Get recent historical performance metrics for specific keywords.",
		runKeywordForecasts)

	addTool(reg, client, "list_recommendations",
		"List active (non-dismissed) recommendations for the account.",
		runListRecommendations)

	addTool(reg, client, "apply_recommendation",
		"Apply a recommendation. Returns a preview + confirm token; pass Confirm to apply.",
		runApplyRecommendation)

	addTool(reg, client, "dismiss_recommendation",
		"Dismiss a recommendation. Returns a preview + confirm token; pass Confirm to apply.",
		runDismissRecommendation)

	addTool(reg, client, "upload_image_asset",
		"Upload a base64-encoded image asset. Returns a preview + confirm token; pass Confirm to apply.",
		runUploadImageAsset)

	addTool(reg, client, "upload_youtube_video_asset",
		"Create an asset referencing an existing YouTube video. Returns a preview + confirm token; pass Confirm to apply.",
		runUploadYouTubeVideoAsset)

	addTool(reg, client, "upload_youtube_video",
		"Upload a local MP4 to a Google-managed unlisted YouTube channel. Returns a preview + confirm token; pass Confirm to apply.",
		runUploadYouTubeVideo)

	addTool(reg, client, "upload_text_asset",
		"Upload a reusable text asset. Returns a preview + confirm token; pass Confirm to apply.",
		runUploadTextAsset)

	addTool(reg, client, "draft_app_ad",
		"Draft an App campaign ad with text, image, and YouTube assets. Returns a preview + confirm token; pass Confirm to apply.",
		runDraftAppAd)

	addTool(reg, client, "pause_entity",
		"Pause a campaign, ad group, ad, or keyword. Returns a preview + confirm token; pass Confirm to apply.",
		runPauseEntity)

	addTool(reg, client, "enable_entity",
		"Enable a campaign, ad group, ad, or keyword. Returns a preview + confirm token; pass Confirm to apply.",
		runEnableEntity)

	addTool(reg, client, "remove_entity",
		"Remove a campaign, ad group, ad, or keyword (destructive). Returns a preview + confirm token; pass Confirm to apply.",
		runRemoveEntity)

	addTool(reg, client, "set_campaign_schedule",
		"Set campaign ad schedules (day-of-week time windows). Returns a preview + confirm token; pass Confirm to apply.",
		runSetCampaignSchedule)

	addTool(reg, client, "create_portfolio_bidding_strategy",
		"Create a portfolio bidding strategy (TARGET_CPA/ROAS/IMPRESSION_SHARE). Returns a preview + confirm token; pass Confirm to apply.",
		runCreatePortfolioBidding)

	addTool(reg, client, "update_portfolio_bidding_strategy",
		"Rename a portfolio (shared) bidding strategy or change the target its type carries (TargetCPA, TargetROAS, or ImpressionShareLocation/ImpressionSharePercent). This is where an attached campaign's target lives, so a change moves every attached campaign at once and takes two confirmations; a rename takes one. Returns a preview + confirm token; pass Confirm to apply.",
		runUpdatePortfolioBidding)

	addTool(reg, client, "update_keyword_bid",
		"Update a keyword's CPC bid (enforces the bid-increase guard). NewBid is required and must be positive; set ClearBid to remove the keyword's own bid so it falls back to the ad group default. Returns a preview + confirm token; pass Confirm to apply.",
		runUpdateKeywordBid)

	addTool(reg, client, "create_custom_audience",
		"NOT SUPPORTED YET: custom audiences need the dedicated customAudiences:mutate service (v23). This tool always errors with guidance; create the audience in the Google Ads UI and attach it with google_add_audience_targeting.",
		runCreateCustomAudience)

	addTool(reg, client, "add_audience_targeting",
		"Attach audience targeting to a campaign (TARGETING/OBSERVATION). Returns a preview + confirm token; pass Confirm to apply.",
		runAddAudienceTargeting)

	addTool(reg, client, "create_ad_group",
		"Create an ad group in a campaign (defaults to PAUSED). Returns a preview + confirm token; pass Confirm to apply.",
		runCreateAdGroup)

	addTool(reg, client, "update_ad_group",
		"Update an ad group's name, CPC bid, and/or ad rotation mode. Returns a preview + confirm token; pass Confirm to apply.",
		runUpdateAdGroup)

	addTool(reg, client, "draft_responsive_search_ad",
		"Draft a Responsive Search Ad (3-15 headlines, 2-4 descriptions; defaults to PAUSED). Returns a preview + confirm token; pass Confirm to apply.",
		runDraftResponsiveSearchAd)

	addTool(reg, client, "draft_keywords",
		"Add keywords (with match types) to an ad group. Returns a preview + confirm token; pass Confirm to apply.",
		runDraftKeywords)

	addTool(reg, client, "add_negative_keywords",
		"Add campaign-level negative keywords. Returns a preview + confirm token; pass Confirm to apply.",
		runAddNegativeKeywords)

	addTool(reg, client, "remove_keywords",
		"Remove keywords from an ad group by criterion ID (destructive). Returns a preview + confirm token; pass Confirm to apply.",
		runRemoveKeywords)

	addTool(reg, client, "remove_negative_keywords",
		"Remove campaign-level negative keywords by criterion ID (destructive). Returns a preview + confirm token; pass Confirm to apply.",
		runRemoveNegativeKeywords)

	addTool(reg, client, "draft_sitelinks",
		"Draft sitelink extensions for a campaign. Returns a preview + confirm token; pass Confirm to apply.",
		runDraftSitelinks)

	addTool(reg, client, "create_callouts",
		"Draft callout extensions for a campaign. Returns a preview + confirm token; pass Confirm to apply.",
		runCreateCallouts)

	addTool(reg, client, "create_structured_snippets",
		"Draft structured snippet extensions for a campaign. Returns a preview + confirm token; pass Confirm to apply.",
		runCreateStructuredSnippets)

	addTool(reg, client, "remove_extension",
		"Remove a campaign extension (destructive). Returns a preview + confirm token; pass Confirm to apply.",
		runRemoveExtension)

	addTool(reg, client, "create_pmax_campaign",
		"Create a Performance Max campaign as one atomic batch (defaults to PAUSED). Returns a preview + confirm token; pass Confirm to apply.",
		runCreatePmaxCampaign)

	addTool(reg, client, "create_app_campaign",
		"Create a Google Play App campaign for installs as one atomic batch (defaults to PAUSED). Returns a preview + confirm token; pass Confirm to apply.",
		runCreateAppCampaign)

	addTool(reg, client, "draft_campaign",
		"Draft a new campaign with budget, ad group, and optional keywords (defaults to PAUSED). GeoTargetIDs targets locations and ExcludeGeoTargetIDs excludes them. Returns a preview + confirm token; pass Confirm to apply.",
		runDraftCampaign)

	addTool(reg, client, "update_campaign",
		"Update a campaign's budget, bidding strategy, geo/language targeting (GeoTargetIDs to target locations, ExcludeGeoTargetIDs to exclude them), and/or location options (positive/negative geo target type). Set BiddingStrategy for a standard campaign-level strategy, or PortfolioStrategyID to attach the campaign to a shared strategy from create_portfolio_bidding_strategy so several campaigns pool their conversion volume. Set ClearTargetCPA (or ClearTargetROAS) to strip an optional target off a MAXIMIZE_CONVERSIONS (or MAXIMIZE_CONVERSION_VALUE) campaign — omitting TargetCPA/TargetROAS leaves the existing target in place. Returns a preview + confirm token; pass Confirm to apply.",
		runUpdateCampaign)
}
