# Name map: namespacing Google's tool surface

Reference for [#36](https://github.com/Limetric/goads/issues/36). Every public
name that changes when Google's surface moves into its own namespace, so the
platform issues under #31 (#21–#30) can be written against the shape that
actually landed.

## Rules

1. **Platform tools are namespaced, symmetrically.** Google is not the implicit
   default; it is one platform among the ones we will add.
   - CLI: `ads google <command>` (was `goads <command>`)
   - MCP: `google_<tool>` (was `<tool>`)
2. **Shared infrastructure stays unnamespaced** — `login`, `doctor`, `config`,
   `confirm`, `audit`, `mcp`, `version`, `completion`. Each grows a *platform
   dimension* instead: `ads login google`, `ads doctor google`,
   `ads config google set-customer`.
3. **The binary is `ads`, not `goads`.** `goads` reads as "Google Ads"; the name
   had to move with the namespacing or not at all. The Go module path and the
   GitHub repo stay `github.com/Limetric/goads`.
4. **Clean break.** The old unprefixed names are gone — no aliases, no
   deprecation window. At v0.2.1 the entire public surface is still cheap to
   rename, and keeping aliases would re-take the exact names the next platform
   wants.

> #36 says 52 MCP tools; the tree actually registers **49**. The count in the
> issue is stale, not a missing group.

## MCP tools (49)

Uniform: every tool gains a `google_` prefix, nothing else changes. Descriptions,
`Args` structs, and handlers are untouched.

| Old | New |
| --- | --- |
| `search` | `google_search` |
| `list_accounts` | `google_list_accounts` |
| `account_info` | `google_account_info` |
| `set_campaign_budget` | `google_set_campaign_budget` |
| `delete_campaign_budget` | `google_delete_campaign_budget` |
| `campaigns` | `google_campaigns` |
| `ads` | `google_ads` |
| `keyword_performance` | `google_keyword_performance` |
| `search_terms` | `google_search_terms` |
| `negative_keywords` | `google_negative_keywords` |
| `report` | `google_report` |
| `geo_targets` | `google_geo_targets` |
| `geo_performance` | `google_geo_performance` |
| `conversions` | `google_conversions` |
| `policy` | `google_policy` |
| `extensions` | `google_extensions` |
| `keyword_ideas` | `google_keyword_ideas` |
| `keyword_forecasts` | `google_keyword_forecasts` |
| `list_recommendations` | `google_list_recommendations` |
| `apply_recommendation` | `google_apply_recommendation` |
| `dismiss_recommendation` | `google_dismiss_recommendation` |
| `upload_image_asset` | `google_upload_image_asset` |
| `upload_youtube_video_asset` | `google_upload_youtube_video_asset` |
| `upload_youtube_video` | `google_upload_youtube_video` |
| `upload_text_asset` | `google_upload_text_asset` |
| `draft_app_ad` | `google_draft_app_ad` |
| `pause_entity` | `google_pause_entity` |
| `enable_entity` | `google_enable_entity` |
| `remove_entity` | `google_remove_entity` |
| `set_campaign_schedule` | `google_set_campaign_schedule` |
| `create_portfolio_bidding_strategy` | `google_create_portfolio_bidding_strategy` |
| `update_keyword_bid` | `google_update_keyword_bid` |
| `create_custom_audience` | `google_create_custom_audience` |
| `add_audience_targeting` | `google_add_audience_targeting` |
| `create_ad_group` | `google_create_ad_group` |
| `update_ad_group` | `google_update_ad_group` |
| `draft_responsive_search_ad` | `google_draft_responsive_search_ad` |
| `draft_keywords` | `google_draft_keywords` |
| `add_negative_keywords` | `google_add_negative_keywords` |
| `remove_keywords` | `google_remove_keywords` |
| `remove_negative_keywords` | `google_remove_negative_keywords` |
| `draft_sitelinks` | `google_draft_sitelinks` |
| `create_callouts` | `google_create_callouts` |
| `create_structured_snippets` | `google_create_structured_snippets` |
| `remove_extension` | `google_remove_extension` |
| `create_pmax_campaign` | `google_create_pmax_campaign` |
| `create_app_campaign` | `google_create_app_campaign` |
| `draft_campaign` | `google_draft_campaign` |
| `update_campaign` | `google_update_campaign` |

The prefix is applied by the registration helper, not typed per tool, so a
platform cannot register an unnamespaced tool by omission.

## CLI: platform commands

All 26 top-level tool commands move under `ads google` unchanged — 49 runnable
commands in total. The subcommand trees below each are untouched.

| Old | New |
| --- | --- |
| `goads accounts` | `ads google accounts` |
| `goads accounts info` | `ads google accounts info` |
| `goads ad draft-rsa` | `ads google ad draft-rsa` |
| `goads ad draft-app` | `ads google ad draft-app` |
| `goads adgroup create` | `ads google adgroup create` |
| `goads adgroup update` | `ads google adgroup update` |
| `goads ads` | `ads google ads` |
| `goads asset image` | `ads google asset image` |
| `goads asset text` | `ads google asset text` |
| `goads asset youtube` | `ads google asset youtube` |
| `goads asset upload-video` | `ads google asset upload-video` |
| `goads audience create` | `ads google audience create` |
| `goads audience target` | `ads google audience target` |
| `goads bidding create-strategy` | `ads google bidding create-strategy` |
| `goads bidding set-keyword-bid` | `ads google bidding set-keyword-bid` |
| `goads budget set` | `ads google budget set` |
| `goads budget delete` | `ads google budget delete` |
| `goads campaign create` | `ads google campaign create` |
| `goads campaign create-app` | `ads google campaign create-app` |
| `goads campaign update` | `ads google campaign update` |
| `goads campaigns` | `ads google campaigns` |
| `goads conversions` | `ads google conversions` |
| `goads enable` | `ads google enable` |
| `goads extension sitelinks` | `ads google extension sitelinks` |
| `goads extension callouts` | `ads google extension callouts` |
| `goads extension snippets` | `ads google extension snippets` |
| `goads extension remove` | `ads google extension remove` |
| `goads extensions` | `ads google extensions` |
| `goads geo search` | `ads google geo search` |
| `goads geo performance` | `ads google geo performance` |
| `goads keyword-forecasts` | `ads google keyword-forecasts` |
| `goads keyword-ideas` | `ads google keyword-ideas` |
| `goads keywords performance` | `ads google keywords performance` |
| `goads keywords search-terms` | `ads google keywords search-terms` |
| `goads keywords negative` | `ads google keywords negative` |
| `goads keywords add` | `ads google keywords add` |
| `goads keywords add-negative` | `ads google keywords add-negative` |
| `goads keywords remove` | `ads google keywords remove` |
| `goads keywords remove-negative` | `ads google keywords remove-negative` |
| `goads pause` | `ads google pause` |
| `goads pmax create` | `ads google pmax create` |
| `goads policy` | `ads google policy` |
| `goads recommendations list` | `ads google recommendations list` |
| `goads recommendations apply` | `ads google recommendations apply` |
| `goads recommendations dismiss` | `ads google recommendations dismiss` |
| `goads remove` | `ads google remove` |
| `goads report` | `ads google report` |
| `goads schedule` | `ads google schedule` |
| `goads search` | `ads google search` |

## CLI: shared infrastructure

Unnamespaced, but platform-aware.

| Old | New | Note |
| --- | --- | --- |
| `goads login` | `ads login google` | One `login` subcommand per registered platform. |
| `goads doctor` | `ads doctor` | Now checks every registered platform in turn. |
| — | `ads doctor google` | New: check one platform. |
| `goads config path` | `ads config path` | Unchanged — the config file is shared. |
| `goads config show` | `ads config show` | Now prints a section per platform. |
| `goads config set-customer` | `ads config google set-customer` | Customer IDs are a Google concept. |
| `goads confirm <token>` | `ads confirm <token>` | Unchanged — the confirm store is shared. |
| `goads audit` | `ads audit` | Unchanged — one audit log across platforms. |
| `goads mcp` | `ads mcp` | Unchanged — serves every platform's tools. |
| `goads version` | `ads version` | Unchanged. |
| `goads completion <shell>` | `ads completion <shell>` | Cobra-generated; picks up the new tree automatically. |

## Environment and config keys

`GOOGLE_ADS_*` keeps its names and the TOML keys keep theirs. What changed in
the platform split is *where they are read*: a Google provider owns them instead
of a flat global `Config`.

One key has since moved out of this table entirely. `GOOGLE_ADS_REFRESH_TOKEN`
and the `refresh_token` TOML key are **deprecated** (issue #37): the refresh
token lives in the per-platform token store, and both are accepted only as a
one-time seed into it, with a warning, through the 0.x line. `GOADS_TOKEN_STORE`
is platform-neutral and overrides the store directory.

| Key | Owner after the split |
| --- | --- |
| `GOOGLE_ADS_DEVELOPER_TOKEN` | Google provider |
| `GOOGLE_ADS_CLIENT_ID` | Google provider |
| `GOOGLE_ADS_CLIENT_SECRET` | Google provider |
| `GOOGLE_ADS_REFRESH_TOKEN` | Google provider (deprecated — seeds the token store) |
| `GOADS_TOKEN_STORE` | Shared (token store directory override) |
| `GOOGLE_ADS_LOGIN_CUSTOMER_ID` | Google provider |
| `GOOGLE_ADS_CUSTOMER_ID` | Google provider |
| `GOOGLE_ADS_API_BASE_URL` | Google provider (per-platform base-URL override; `httptest` hooks in here) |
| `GOOGLE_ADS_MAX_DAILY_BUDGET` | Google provider (safety guards) |

## Distribution names

| Thing | Old | New |
| --- | --- | --- |
| Binary | `goads` | `ads` |
| Release asset | `goads-darwin-arm64` | `ads-darwin-arm64` |
| Homebrew formula | `Formula/goads.rb`, `class Goads` | `Formula/ads.rb`, `class Ads` |
| Homebrew install | `brew install Limetric/tap/goads` | `brew install Limetric/tap/ads` |
| Claude Code plugin | `goads@goads` | `ads@goads` |
| Codex plugin | `goads@goads` | `ads@goads` |
| Marketplace ID | `goads` | unchanged |
| Skill | `plugins/goads/skills/goads` | `plugins/ads/skills/ads` |
| Go module / repo | `github.com/Limetric/goads` | unchanged |
| Config directory | `~/.config/goads` | `~/.config/ads` |

The plugin refs are `<plugin>@<marketplace>`. The **marketplace** ID is the repo
(`Limetric/goads`), which this change does not rename, so it stays `goads` in
both `.claude-plugin/marketplace.json` and `.agents/plugins/marketplace.json`;
only the plugin inside it becomes `ads`. Keep the two manifests' `name` fields
in step — renaming one and not the other silently breaks the install command for
one host.

The config directory (which holds `config.toml`, the confirm-token store, and
the audit log) moves with the binary. There is **no fallback** to the old path:
an existing install has to move the directory by hand, or re-run
`ads login google` and re-set its default customer. macOS uses
`~/Library/Application Support/<name>` for the same directory.
