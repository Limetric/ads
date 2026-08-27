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

49 tools, each available as an `ads google …` subcommand and a `google_…` MCP
tool. See [`name-map.md`](name-map.md) for the full CLI ↔ MCP map.

**Reads** — `search` (raw GAQL), `report`, `accounts` (+ `accounts info` for
currency and time zone), `campaigns`, `ads`, keyword performance / search terms
/ negatives, `geo search` + `geo performance`, `conversions`, `policy`,
`extensions`, Keyword Planner ideas and forecasts, and recommendation listing.

**Writes** (all preview-then-confirm) — Search, App, and Performance Max
campaign create/update, ad group create/update, responsive search ad drafting,
keyword add/remove (plus negatives), portfolio bidding strategies and keyword
bids, sitelink/callout/structured-snippet extensions, custom audiences and
audience targeting, image/YouTube/text asset upload, ad scheduling,
pause/enable/remove, and recommendation apply/dismiss.

`campaign create` and `campaign update` carry the budget, the bidding strategy,
geo/language targeting, and the campaign's **location options** — Google's
"presence or interest" vs "presence" matching, as
`--positive-geo-target-type` / `--negative-geo-target-type`. The App and
Performance Max create commands do not take them; set them afterwards with
`campaign update`.

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
strategy itself and every attached campaign moves together. Setting
`--bidding-strategy` again moves the campaign back to a standard strategy.
Strategies shared down from a manager account are attachable too. To list what
is available:

```bash
ads google search --query "SELECT accessible_bidding_strategy.id, accessible_bidding_strategy.name, accessible_bidding_strategy.type FROM accessible_bidding_strategy"
```

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
