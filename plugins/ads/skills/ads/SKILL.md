---
name: ads
description: Use when working with Google Ads — querying campaigns/keywords/metrics with GAQL, listing accounts, or changing budgets and other settings. Drives the `ads` CLI. Triggers include "google ads", "GAQL", "campaign budget", "ad spend", "what's my CPC/CTR/impressions", or any read/write against a Google Ads account.
---

# ads — Google Ads via the `ads` CLI

`ads` is a single binary that talks to ad platform REST APIs. Prefer it over
ad-hoc HTTP. Drive it from the shell; pipe large results through `jq` so they
never have to land in full in context.

Every platform lives in its own namespace. Google Ads is `ads google …`; shared
plumbing (`doctor`, `login`, `config`, `confirm`, `audit`) sits at the top level.

## Setup check

Run this first if anything errors with missing credentials:

```bash
ads doctor google
```

If it reports `NOT READY`, the required `GOOGLE_ADS_*` environment variables are
not set — ask the user to provide them; do not invent values.

## Reading data (GAQL)

`google search` runs a [GAQL](https://developers.google.com/google-ads/api/docs/query/overview)
query and prints result rows as JSON. Pass `--customer-id`, or omit it when a
default is configured (`ads config show` reveals it; users set one with
`ads config google set-customer <id>` or `GOOGLE_ADS_CUSTOMER_ID`).

```bash
ads google search --customer-id 123-456-7890 \
  --query 'SELECT campaign.id, campaign.name, metrics.impressions, metrics.cost_micros
           FROM campaign WHERE segments.date DURING LAST_7_DAYS ORDER BY metrics.cost_micros DESC' \
  | jq '.rows[].campaign'
```

Tips:
- Filter and aggregate with `jq` before summarizing — don't dump every row.
- Costs are in **micros** (1,000,000 micros = 1 unit of currency);
  `ads google accounts info` shows which currency the account uses.
- `ads google accounts` lists the customer IDs you can reach.
- Read commands take `--format table|csv` when the user wants a table or
  spreadsheet instead of JSON.

## Making changes (always previews first)

Write commands are **two-step**: the first call returns a preview and a
`confirm_token`; nothing changes until you re-run with `--confirm <token>`.
Show the preview to the user and get their go-ahead before confirming.

```bash
# 1. Preview — read confirm_token from the output
ads google budget set --customer-id 123-456-7890 --budget-id 555 --amount-micros 5000000

# 2. Apply only after the user agrees
ads confirm <token-from-step-1>
```

(`ads confirm <token>` applies the staged change exactly as previewed;
re-running the original command with `--confirm <token>` works too. Destructive
operations return a second token that must be confirmed once more.)

Never skip the preview, never guess a token, and never confirm a write the user
hasn't approved. `ads audit` lists every write that has been applied.

## Command reference

All Google commands below are prefixed with `ads google`; the shared ones
(`audit`, `config …`) are `ads <command>`.

**Reads** (print JSON; pipe through `jq`):

| Command | What it shows |
|---|---|
| `google search` / `google report` | run a GAQL query |
| `google accounts` / `google accounts info` | accessible customer IDs / account name, currency, time zone |
| `google campaigns` / `google ads` | campaign- / ad-level performance |
| `google keywords performance` / `google keywords search-terms` / `google keywords negative` | keyword metrics, search terms, negatives |
| `google geo search` / `google geo performance` | find location IDs / geo performance |
| `google conversions` / `google policy` / `google extensions` | conversion actions / policy issues / extensions |
| `google keyword-ideas` / `google keyword-forecasts` | Keyword Planner ideas / recent metrics |
| `google recommendations list` | active recommendations |
| `audit` | log of applied writes |
| `config show` / `config google set-customer` | resolved config / set the default account |

**Writes** (two-step preview → `--confirm <token>`):

| Command | Action |
|---|---|
| `google budget set` | set a campaign budget |
| `google campaign create` / `google campaign update` | draft / update a campaign |
| `google adgroup create` / `google adgroup update` | create / update an ad group |
| `google ad draft-rsa` | draft a Responsive Search Ad |
| `google keywords add` / `add-negative` / `remove` / `remove-negative` | manage keywords |
| `google bidding create-strategy` / `google bidding set-keyword-bid` | portfolio strategy / keyword bid |
| `google extension sitelinks\|callouts\|snippets\|remove` | manage extensions |
| `google audience create` / `google audience target` | custom audiences / targeting |
| `google asset image` / `google asset text` | upload assets |
| `google schedule` | set ad schedules |
| `google pmax create` | create a Performance Max campaign |
| `google pause` / `google enable` / `google remove` | change entity status (`remove` is destructive) |
| `google recommendations apply` / `google recommendations dismiss` | act on recommendations |

New entities (campaigns, ad groups, ads, PMax) are created **PAUSED** by default;
the preview's `next_action_hint` shows how to `enable` them afterward.

## Over MCP

The same tools are served by `ads mcp` under platform-prefixed names —
`google_search`, `google_campaigns`, `google_set_campaign_budget`, and so on.

## Discovering commands

```bash
ads --help                 # top-level: platforms and shared commands
ads google --help          # all Google Ads commands
ads google <command> --help  # flags for one command
```

Use `--help` to learn a command's flags rather than guessing.
