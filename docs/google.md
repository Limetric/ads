# Google Ads

Setup, sign-in, and the full tool surface for the `google` platform —
`ads google …` on the CLI, `google_…` over MCP.

New to `ads`? Start at the [README](../README.md) for installation and the
concepts shared across platforms (token store, confirm flow, output formats).

## Prerequisites

Google gates the Ads API behind three separate things, and you need all three
before the first call succeeds:

1. **A Google Cloud project with the Google Ads API enabled** —
   [console.cloud.google.com/apis/library/googleads.googleapis.com](https://console.cloud.google.com/apis/library/googleads.googleapis.com)
2. **A Desktop-app OAuth client** in that project —
   [console.cloud.google.com/apis/credentials](https://console.cloud.google.com/apis/credentials).
   Download its JSON. A Web-application client also works, but Desktop app is
   the right type for a CLI and is what `ads login google` expects; it warns and
   continues if it finds a Web client.
   You may also need to fill in the
   [OAuth consent screen](https://console.cloud.google.com/apis/credentials/consent)
   before a client can be created.
3. **A Google Ads developer token** — in Google Ads, **Tools & Settings → API
   Center** ([ads.google.com/aw/apicenter](https://ads.google.com/aw/apicenter)).
   A fresh token starts with test-account access only; production access needs
   Google's approval. This is the most common reason a correct-looking setup
   fails to return live data.

Optionally, a **manager (MCC) account ID** if you operate accounts through a
manager account.

## Guided setup

The wizard walks all of the above end to end — about five minutes — then proves
the result with a live API call:

```bash
ads login google
```

It runs five steps: Cloud project + API enable, Desktop-app OAuth client,
browser sign-in, developer token, and the optional manager account ID. It offers
to open each console page for you (`--no-browser` prints the URLs instead), then
saves everything and verifies by listing your accessible accounts.

Run it again any time. If an OAuth client and developer token are already
configured, it skips the setup steps and just re-establishes the sign-in.

Useful flags:

| Flag | Effect |
| --- | --- |
| `--credentials <path>` | Read the OAuth client from a downloaded JSON instead of prompting |
| `--port <n>` | Loopback port for the OAuth callback (default `8085`) |
| `--no-browser` | Print the authorization URL instead of opening a browser |
| `--no-input` | Never prompt — fail if anything is missing (scripts, CI) |

## Manual setup (scripts and CI)

Set the environment directly and skip the wizard with `--no-input`:

```bash
export GOOGLE_ADS_DEVELOPER_TOKEN=...
export GOOGLE_ADS_CLIENT_ID=...
export GOOGLE_ADS_CLIENT_SECRET=...
export GOOGLE_ADS_LOGIN_CUSTOMER_ID=123-456-7890   # optional manager account

ads login google --no-input   # signs in; saves the refresh token to the token store
ads doctor google             # verify credentials resolve
ads google accounts           # list accessible accounts
```

Everything except the refresh token can also live in the `[google]` table of
`config.toml` (`ads config path` shows where):

```toml
[google]
developer_token   = "..."
client_id         = "..."
client_secret     = "..."
login_customer_id = "1234567890"
default_customer_id = "1234567890"
```

### Environment variables

| Variable | Purpose |
| --- | --- |
| `GOOGLE_ADS_DEVELOPER_TOKEN` | Developer token from the API Center |
| `GOOGLE_ADS_CLIENT_ID` | OAuth client ID |
| `GOOGLE_ADS_CLIENT_SECRET` | OAuth client secret |
| `GOOGLE_ADS_LOGIN_CUSTOMER_ID` | Manager (MCC) account, when operating through one |
| `GOOGLE_ADS_CUSTOMER_ID` | Default account, so `--customer-id` can be omitted |
| `GOOGLE_ADS_MAX_DAILY_BUDGET` | Guard rail: reject budgets above this |
| `GOOGLE_ADS_MAX_BID_INCREASE_PCT` | Guard rail: reject bid increases above this |
| `GOOGLE_ADS_BLOCKED_OPS` | Guard rail: refuse these mutation operations |

`GOOGLE_ADS_REFRESH_TOKEN` is deprecated — see
[where the refresh token lives](../README.md#where-the-refresh-token-lives).
It still works through the 0.x line, but the value is copied into the token
store on first use and ignored from then on.

## Pick a default account

Set it once and drop `--customer-id` everywhere (an explicit flag still wins):

```bash
ads config google set-customer 123-456-7890
ads google campaigns          # uses the default account
ads config show               # see the resolved config (secrets redacted)
```

## Everyday commands

```bash
ads google accounts                          # accessible accounts
ads google accounts info                     # currency, time zone, account flags
ads google campaigns --format table          # campaign performance
ads google keywords performance              # keyword metrics
ads google keywords search-terms             # what people actually searched
ads google search --query 'SELECT campaign.id, campaign.name FROM campaign LIMIT 10' | jq
```

Reads print JSON by default; `--format table` and `--format csv` work on every
row-returning read.

`ads google search` takes a raw [GAQL](https://developers.google.com/google-ads/api/docs/query/overview)
query, which is the escape hatch when no dedicated tool covers what you need.

Writes preview first and apply only on confirm:

```bash
ads google budget set --budget-id 555 --amount-micros 5000000
# → prints a preview and a confirm token, e.g. a1b2c3d4e5f6a7b8
ads confirm a1b2c3d4e5f6a7b8
```

Costs and bids are in **micros** — 5000000 micros is 5 units of the account's
currency. `ads google accounts info` reports which currency that is.

## Tool coverage

53 tools, each available as an `ads google …` subcommand and a `google_…` MCP
tool. See [`name-map.md`](name-map.md) for the full CLI ↔ MCP map.

**Reads** — `search` (raw GAQL), `report`, `accounts` (+ `accounts info` for
currency and time zone), `campaigns`, `ads`, keyword performance / search terms
/ negatives, `geo search` + `geo performance`, `conversions`, `policy`,
`extensions`, a campaign's criteria, Keyword Planner ideas and forecasts, and
recommendation listing.

**Writes** (all preview-then-confirm) — Search, App, and Performance Max
campaign create/update, ad group create/update, responsive search ad and
dynamic search ad drafting, keyword add/remove (plus negatives), dynamic ad
targets, portfolio bidding strategy create/update
and keyword bids, sitelink/callout/structured-snippet extensions, custom audiences and
audience targeting, image/YouTube/text asset upload, ad scheduling,
pause/enable/remove (campaign criteria included), and recommendation
apply/dismiss.

`campaign create` and `campaign update` carry the budget, the bidding strategy,
geo/language targeting, and the campaign's **location options** — Google's
"presence or interest" vs "presence" matching, as
`--positive-geo-target-type` / `--negative-geo-target-type`. The App and
Performance Max create commands do not take them; set them afterwards with
`campaign update`.

### Targeting and excluding locations

`--geo-target-id` targets a location; `--exclude-geo-target-id` excludes one.
Both take geo target constant IDs, which `geo search` finds:

```bash
ads google geo search --query "United Kingdom"

# Target the UK, but not Northern Ireland, and match exclusions on presence.
ads google campaign update --campaign-id 111 \
  --geo-target-id 2826 --exclude-geo-target-id 20339 \
  --negative-geo-target-type PRESENCE
```

`--negative-geo-target-type` is what governs an exclusion: `PRESENCE` excludes
people regularly in the location, `PRESENCE_OR_INTEREST` also excludes people
merely interested in it (most campaign types no longer accept it). Without an
exclusion to govern it, that setting configures an empty set.

The same ID cannot be targeted and excluded at once — Google applies the
exclusion, so it would silently do nothing — and that is rejected at preview.
Both flags are **additive**: each call adds to what the campaign already
carries. To take a location back off, see
[campaign criteria](#campaign-criteria-geo-language-ad-schedule) below.

### Renaming a campaign and setting its run dates

```bash
ads google campaign update --campaign-id 111 --name "Brand — EU"

# Wind the campaign down on a schedule instead of being present to pause it.
ads google campaign update --campaign-id 111 --end-date 2026-12-31

# Let it run indefinitely again.
ads google campaign update --campaign-id 111 --clear-end-date
```

Dates are `YYYY-MM-DD`. They set `campaign.start_date_time` /
`end_date_time` — v23 has no plain date field — so a bare date is completed to
the whole-day boundary Google asks for at that end of the range: `00:00:00` for
the start, `23:59:59` for the end. Campaign types that support minute
granularity can be given a `YYYY-MM-DD HH:MM:SS` instead, in the account's time
zone.

Google rejects a `--start-date` change once a campaign has started.
`--clear-end-date` clears the field, which is how Google says to set a running
campaign back to indefinite; it cannot be combined with `--end-date`.

### Dynamic Search Ads

A DSA campaign has Google crawl your site and generate the headline and landing
page for each query, so the four pieces below only work together — a dynamic ad
group with no targets never serves, and an ad group's type is fixed at creation.
`campaign create` builds the campaign and its dynamic ad group in one batch:

```bash
ads google campaign create --name "DSA — Catalogue" --daily-budget 25 \
  --ad-group-name "Dynamic" \
  --dsa-domain example.com --dsa-language-code en
```

`--dsa-domain` wants the bare domain (`example.com`, `www.example.com`), not a
URL, and Google requires the language alongside it. The ad group is created as
`SEARCH_DYNAMIC_ADS`, which is why `--keyword` is refused here: a dynamic ad
group matches pages through *dynamic ad targets*, not keywords. Add
`--dsa-use-supplied-urls-only` to serve only page-feed URLs instead of Google's
crawl.

Then draft the ad and the targets against the new ad group. A dynamic search ad
carries only descriptions — there is no headline and no final URL to give,
because Google writes both:

```bash
ads google ad draft-dsa --ad-group-id 222 \
  --description "Free delivery on every order." \
  --description2 "Browse the full catalogue."

ads google webpage-targets add --ad-group-id 222 \
  --criterion-name "Special offers" --condition URL=/specialoffers
```

Conditions are `OPERAND=ARGUMENT` — `URL`, `CATEGORY`, `PAGE_TITLE`,
`PAGE_CONTENT`, or `CUSTOM_LABEL` — and repeating `--condition` narrows the
target, since Google AND-s them. `CATEGORY` arguments come from the
`domain_category` resource, which `ads google search` can read once Google has
crawled the site. `--cpc-bid-micros` gives one target its own bid.

Targeting the whole site is a separate flag rather than an empty condition
list, so it can never happen by forgetting one:

```bash
ads google webpage-targets add --ad-group-id 222 \
  --criterion-name "All pages" --all-webpages
```

To add a dynamic ad group to a campaign that already exists, pass the type
explicitly — it cannot be changed afterwards:

```bash
ads google adgroup create --campaign-id 111 --name "Dynamic" --type SEARCH_DYNAMIC_ADS
```

And a Search campaign that should have been a DSA campaign can be given the
setting after the fact:

```bash
ads google campaign update --campaign-id 111 --dsa-domain example.com --dsa-language-code en
```

`campaign update` writes the setting as a whole, because Google requires the
domain and the language on every write of it — so pass
`--dsa-use-supplied-urls-only` each time it should stay on. Its existing ad
groups stay standard; dynamic ones have to be created.

### Ad group bids and targets

`adgroup update` carries the ad group's name, its default CPC bid, its ad
rotation mode, and its **own** target, which overrides the campaign's for that
ad group alone:

```bash
ads google adgroup update --ad-group-id 222 --target-cpa-micros 12500000
ads google adgroup update --ad-group-id 222 --target-roas 3.5

# Back to inheriting: the campaign target and the ad group default bid apply.
ads google adgroup update --ad-group-id 222 --clear-target-cpa --clear-cpc-bid
```

As everywhere else, an omitted number means "leave it alone", so each removal
has its own flag: `--clear-cpc-bid`, `--clear-target-cpa`, `--clear-target-roas`.
Unlike a campaign's bidding targets — which are members of one strategy — these
are independent values on the ad group, so more than one can be cleared at once.

### Clearing a bidding target

`--target-cpa` / `--target-roas` are optional on `MAXIMIZE_CONVERSIONS` and
`MAXIMIZE_CONVERSION_VALUE`, and *omitting* one on an update leaves the existing
target alone — otherwise every unrelated campaign edit would wipe it. To drop
the target and let the strategy bid without one, say so explicitly:

```bash
# Pure Maximize Conversions: keep the strategy, remove the target CPA.
ads google campaign update --campaign-id 111 --clear-target-cpa
ads google campaign update --campaign-id 111 --clear-target-cpa --confirm <token>
```

`--clear-target-roas` does the same for a `MAXIMIZE_CONVERSION_VALUE` campaign.
Each flag applies only to its own strategy: `TARGET_CPA` and `TARGET_ROAS`
*require* a target, so a campaign on one of those switches strategy instead
(`--bidding-strategy MAXIMIZE_CONVERSIONS`), and a portfolio strategy's target
is changed on the shared strategy itself. A clear flag cannot be combined with
the target value it removes, and it touches only the target — a
`MAXIMIZE_CONVERSION_VALUE` campaign keeps its ROAS degradation tolerance.

### Keyword bids

`bidding set-keyword-bid` takes `--new-bid` (currency units), which must be
positive. Omitting it is an error rather than a bid of zero — a keyword whose
`cpc_bid_micros` is cleared falls back to its ad group's default bid, and that
is too destructive to infer from a missing flag. Ask for it by name:

```bash
# Set an explicit keyword bid.
ads google bidding set-keyword-bid --ad-group-id 111 --criterion-id 222 --new-bid 1.50

# Drop the keyword's own bid; it inherits the ad group default from here on.
ads google bidding set-keyword-bid --ad-group-id 111 --criterion-id 222 --clear-bid
```

The two flags are mutually exclusive, and exactly one is required. Bid
*increases* are still measured against the keyword's real current bid, fetched
from the API; a clear is never an increase, so it skips that lookup.

### Portfolio (shared) bidding strategies

`--bidding-strategy` sets a **standard** strategy, which each campaign learns on
its own conversions. To pool volume across campaigns, create one portfolio
strategy and attach each campaign to it with `--portfolio-strategy-id`:

```bash
ads google bidding create-strategy --name "Pooled tCPA" --type TARGET_CPA --target-cpa 35
ads google bidding create-strategy … --confirm <token>   # returns resource_names

# Attach — the ID or the whole resource name from above both work.
ads google campaign update --campaign-id 111 --portfolio-strategy-id 9876543210
ads google campaign update --campaign-id 111 --portfolio-strategy-id 9876543210 --confirm <token>
```

The targets live on the shared strategy, so `--target-cpa` / `--target-roas`
are rejected alongside `--portfolio-strategy-id`; change the target on the
strategy itself with `bidding update-strategy` and every attached campaign
moves together. Setting `--bidding-strategy` again moves the campaign back to a
standard strategy. Strategies shared down from a manager account are attachable
too. To list what is available:

```bash
ads google search --query "SELECT accessible_bidding_strategy.id, accessible_bidding_strategy.name, accessible_bidding_strategy.type FROM accessible_bidding_strategy"
```

#### Changing a shared strategy

```bash
# Move every attached campaign to a new target CPA.
ads google bidding update-strategy --strategy-id 9876543210 --target-cpa 42
ads google bidding update-strategy --strategy-id 9876543210 --target-cpa 42 --confirm <token>
ads google bidding update-strategy --strategy-id 9876543210 --target-cpa 42 --confirm <second-token>

# Rename it.
ads google bidding update-strategy --strategy-id 9876543210 --name "Pooled tCPA — EU"
```

The strategy's type decides which target it takes: `--target-cpa` for
`TARGET_CPA` and `MAXIMIZE_CONVERSIONS`, `--target-roas` for `TARGET_ROAS` and
`MAXIMIZE_CONVERSION_VALUE`, and
`--impression-share-location` / `--impression-share-percent` for
`TARGET_IMPRESSION_SHARE` (which `bidding create-strategy` starts at
`ANYWHERE_ON_PAGE` / 50%). A type is fixed once the strategy exists, so passing
the wrong target fails at preview and names the one that applies.

**A target change takes two confirmations.** It moves every campaign attached
to the strategy at once — which is the point of a portfolio, and the reason it
is worth seeing twice. A rename takes one. Only the account that *owns* a
strategy can change it: a strategy shared down from a manager is rejected with
the manager's customer ID to re-run against.

### Campaign criteria: geo, language, ad schedule

Geo targets, languages, ad-schedule windows, and campaign negative keywords are
all *campaign criteria*. `campaign update --geo-target-id/--language-id` and
`schedule` add them; `campaign criteria` lists what a campaign already carries,
and `remove` takes one away:

```bash
# What is on the campaign? Each row carries the ID the removal takes.
ads google campaign criteria --campaign-id 111 --format table
ads google campaign criteria --campaign-id 111 --type AD_SCHEDULE

# Remove one. Destructive, so it takes two confirmations.
ads google remove --type campaign_criterion --id 111~222
ads google remove --type campaign_criterion --id 111~222 --confirm <token>
ads google remove --type campaign_criterion --id 111~222 --confirm <second-token>
```

A criterion is addressed by the composite `campaignId~criterionId`. The
criterion ID is minted by Google, not chosen by you, which is why the listing
lands with the removal: it reports `remove_entity_id` ready to pass straight to
`remove`. `schedule` remains additive — Google has no "replace the schedule"
operation — so a wrong window is corrected by adding the right one and removing
the wrong one.

New campaigns, ad groups, and ads ship **PAUSED** by default.

## Guard rails

Beyond the confirm flow, writes are bounded by thresholds that already have
conservative defaults — you are more likely to be *raising* these than setting
them for the first time:

```bash
export GOOGLE_ADS_MAX_DAILY_BUDGET=500          # currency units, default 50
export GOOGLE_ADS_MAX_BID_INCREASE_PCT=50       # percent, default 100
export GOOGLE_ADS_BLOCKED_OPS=remove_entity,remove_keywords
```

The spend cap is in **currency units, not micros** — a budget of 5000000 micros
is checked as 5. The default of 50 is deliberately low, so the first real budget
you set may well be rejected; the error tells you which variable to raise.

`GOOGLE_ADS_BLOCKED_OPS` is a comma-separated list of **unprefixed tool names**
(`remove_entity`, `delete_campaign_budget`, `remove_keywords`,
`apply_recommendation`, …) — the names in
[`name-map.md`](name-map.md) without the `google_` prefix. A blocked operation
fails before anything is staged.

These variables are Google-named but global: they also govern
[Bing's](bing.md) budget writes.

A client-side allow-list also rejects invalid `googleAds:mutate` operation keys
before they reach the API, and `ads audit` logs every write that was applied.

## Several Google accounts

The token store holds one sign-in per platform, so two configs signing in as
different Google users need a store each — otherwise the second picks up the
first one's sign-in:

```bash
GOADS_TOKEN_STORE=~/.ads/work   ads --config ~/.ads/work.toml   google accounts
GOADS_TOKEN_STORE=~/.ads/client ads --config ~/.ads/client.toml google accounts
```

One Google login that *manages* several accounts needs none of this — set
`GOOGLE_ADS_LOGIN_CUSTOMER_ID` to the manager account and pass `--customer-id`
per command.

## Troubleshooting

Start with `ads doctor google`, which reports which credentials resolve, where
the token store is, whether it is writable, and how old the sign-in is.

| Symptom | Likely cause |
| --- | --- |
| Verification fails right after the wizard | Developer token not approved yet, or mistyped |
| `invalid_grant` | Sign-in revoked or expired — re-run `ads login google` |
| Sign-in mismatch warning | The saved token belongs to a different OAuth client — re-run `ads login google` |
| `listen on 127.0.0.1:8085` fails | Port busy — pass `--port` |
| No accounts listed | The signed-in user has no access, or a manager account ID is needed |
