# ads

[![CI](https://github.com/Limetric/goads/actions/workflows/ci.yml/badge.svg)](https://github.com/Limetric/goads/actions/workflows/ci.yml)

**Ad platform management — a Go CLI and MCP server.**

It talks to ad platform **REST APIs** (no gRPC, no protobuf codegen), and ships
as a single binary with two front-ends over one shared set of tools:

- **CLI** — `ads google search …`, `ads google accounts`, `ads google budget set …`.
  Scriptable, pipeable into `jq`, usable in CI. This is what the bundled agent
  **skill** drives.
- **MCP server** — `ads mcp` serves the same tools over stdio to MCP hosts
  (Claude Desktop, Cursor, …).

Every platform gets its own namespace — `ads google campaigns` on the CLI,
`google_campaigns` over MCP — so networks slot in beside each other rather than
fighting for names. **Google Ads and Microsoft Advertising (Bing Ads) are
implemented today.** Shared plumbing (`login`, `doctor`, `config`, `confirm`,
`audit`, `mcp`) stays unnamespaced, with a platform argument where it needs one.

You only configure the platforms you use: `ads doctor` skips the ones you
haven't set up, and `ads mcp` serves the tools it can and says on stderr which
platform it left out and why.

## Quick start

On macOS and Linux, install `ads` from the Limetric Homebrew tap:

```bash
brew install Limetric/tap/ads
```

The fastest way to get set up is the guided sign-in — it walks you through the
Google Cloud + developer-token prerequisites, signs you in via the browser, and
verifies the connection:

```bash
ads login google             # interactive: guides you from scratch (or just re-signs in), then verifies
```

Prefer to wire it up manually (or in CI)? Set the environment directly and skip
the wizard with `--no-input`:

```bash
go mod tidy
go build -o build/ads .

export GOOGLE_ADS_DEVELOPER_TOKEN=...
export GOOGLE_ADS_CLIENT_ID=...
export GOOGLE_ADS_CLIENT_SECRET=...
export GOOGLE_ADS_LOGIN_CUSTOMER_ID=123-456-7890   # optional manager account

build/ads login google --no-input   # sign in; saves the refresh token to the token store

build/ads doctor              # verify credentials resolve (every platform)
build/ads google accounts     # list accessible accounts
build/ads google search --customer-id 123-456-7890 \
  --query 'SELECT campaign.id, campaign.name FROM campaign LIMIT 10' | jq
```

### Where the refresh token lives

`ads login` saves the refresh token to a per-platform **token store** — one
`0600` file per platform under `~/.config/ads/state/tokens/` — and not to
`config.toml` or an environment variable. The store is writable, which is what
lets ads keep working on platforms that issue a *new* refresh token on every
refresh and invalidate the old one; a token pinned in an env var would work
exactly once there.

```bash
ads doctor google        # where the store is, whether it's writable, how old the sign-in is
export GOADS_TOKEN_STORE=/path/to/tokens   # containers and CI: mount a writable volume here
```

`GOOGLE_ADS_REFRESH_TOKEN` (and `refresh_token` in `config.toml`) still work
through the 0.x line: the value is copied into the store the first time it is
used, with a warning, and ignored from then on. Existing setups need no change.

The store holds one sign-in per platform, so two `--config` files that sign in
as different Google users need a store each — otherwise the second one picks up
the first one's sign-in:

```bash
GOADS_TOKEN_STORE=~/.ads/work     ads --config ~/.ads/work.toml google accounts
GOADS_TOKEN_STORE=~/.ads/client   ads --config ~/.ads/client.toml google accounts
```

Work with one account most of the time? Set a default once and drop
`--customer-id` everywhere (any explicit flag still wins):

```bash
ads config google set-customer 123-456-7890   # or: export GOOGLE_ADS_CUSTOMER_ID=123-456-7890
ads google campaigns                          # uses the default account
ads config show                               # see the resolved config (secrets redacted)
```

Read commands print JSON by default; pass `--format table` or `--format csv`
for human- or spreadsheet-friendly output (`campaigns`, `ads`, `keywords …`,
`search`, `report`, and the other row-returning reads all take it):

```bash
ads google campaigns --format table
```

Writes preview first, then apply with the returned token:

```bash
ads google budget set --budget-id 555 --amount-micros 5000000
# → prints a preview and a confirm token, e.g. a1b2c3d4e5f6a7b8
ads confirm a1b2c3d4e5f6a7b8   # applies the staged change as previewed
ads audit                      # log of every write ads has applied
```

(Re-running the original command with `--confirm <token>` still works too.)

## As an MCP server

Point your MCP host at the binary. Tools are served under their platform
prefix — `google_search`, `google_campaigns`, `google_set_campaign_budget`, …

```json
{
  "mcpServers": {
    "ads": {
      "command": "/path/to/build/ads",
      "args": ["mcp"],
      "env": {
        "GOOGLE_ADS_DEVELOPER_TOKEN": "...",
        "GOOGLE_ADS_CLIENT_ID": "...",
        "GOOGLE_ADS_CLIENT_SECRET": "...",
        "GOOGLE_ADS_LOGIN_CUSTOMER_ID": "..."
      }
    }
  }
}
```

Run `ads login google` once first: the refresh token comes from the token store,
so it does not belong in the host config. Add `"GOADS_TOKEN_STORE": "..."` if the
host runs somewhere `~/.config/ads` isn't writable.

Add `BING_ADS_DEVELOPER_TOKEN`, `BING_ADS_CLIENT_ID`, and (optionally)
`BING_ADS_CUSTOMER_ID` / `BING_ADS_ACCOUNT_ID` to serve `bing_…` tools as well,
after `ads login bing`. A platform whose credentials don't resolve is skipped
with a warning rather than taking the server down, so configuring one network is
fine.

## As a Claude Code skill

The repo bundles a skill (`plugins/ads/skills/ads/SKILL.md`, symlinked at
`.claude/skills/ads` for contributors working in a clone) that teaches an agent
when and how to drive the CLI — token-efficient because nothing loads until it's
relevant, and big result sets stay in the shell (`| jq`) instead of the context
window.

If you installed `ads` via Homebrew and don't have the repo cloned, install the
skill as a Claude Code plugin instead:

```text
/plugin marketplace add Limetric/goads
/plugin install ads@goads
```

## As a Codex plugin

Codex reads the same skill through its own plugin manifest:

```bash
codex plugin marketplace add Limetric/goads
codex plugin add ads@goads
```

## Microsoft Advertising (Bing Ads)

Bing works the same way, under its own namespace:

```bash
ads login bing --client-id <entra-application-id>   # browser sign-in (auth code + PKCE)
ads config bing set-account 123456789               # default account for later commands
ads doctor bing                                     # verify it works

ads bing campaigns                    # campaign settings and budgets
ads bing campaign-performance --format table        # spend, clicks, conversions
ads bing budget set --campaign-id 1 --daily-budget 30
```

Sign-in needs a **Microsoft Entra application registration** (a public/native
client, supporting personal and work accounts) whose redirect URI includes
`http://localhost:8086`, plus a Microsoft Advertising developer token for
production. The sandbox needs neither: `BING_ADS_ENVIRONMENT=sandbox` fills in
Microsoft's universal sandbox developer token for you.

Two differences from Google are worth knowing before you script against it:

- **Metrics are asynchronous.** Microsoft has no query language; every figure
  comes from the Reporting service, which queues a job. The metric tools wait up
  to 45 seconds and then return either rows or a job handle:
  `report queued: job_8f3c` → `ads bing report fetch job_8f3c`. Handles are
  saved on disk, so a handle from the CLI is fetchable from the MCP server and
  the other way round.
- **Money is plain currency, not micros.** `--daily-budget 30` is 30 in the
  account's currency (`ads bing account-info` reports which).

Microsoft replaces the refresh token on every refresh, so Bing's sign-in cannot
live in an environment variable at all — the token store has to be writable.
`ads doctor bing` reports whether it is.

## Tool coverage

Comprehensive Google Ads campaign management plus first-class App campaign
creation is available (49 MCP tools / equivalent CLI commands). Every name below
is a `ads google …` subcommand and a `google_…` MCP tool; see
[`docs/name-map.md`](docs/name-map.md) for the full map.

- **Reads** — `search`, `report`, `accounts` (+ `accounts info` for currency/time
  zone), `campaigns`, `ads`, keyword performance / search terms / negatives,
  `geo` search + performance, `conversions`, `policy`, `extensions`, Keyword
  Planner ideas + forecasts, and recommendations listing. Row-returning reads
  render as `--format json|table|csv`.
- **Writes** (all preview-then-confirm) — Search, App, and Performance Max
  campaign create/update, ad group
  create/update, RSA drafting, keyword add/remove (+ negatives), bidding
  strategies + keyword bids, sitelink/callout/snippet extensions, audiences,
  image/text assets, ad scheduling, Performance Max campaigns, pause/enable/remove,
  and recommendation apply/dismiss.

Microsoft Advertising exposes what Microsoft has — 10 `bing_…` MCP tools /
`ads bing …` commands:

- **Reads** — `list_accounts`, `account_info`, `campaigns`, `ad_groups`,
  `keywords` (entity settings, sub-second) and `campaign_performance`,
  `keyword_performance`, `ad_performance` (metrics, via Reporting), plus
  `report_fetch` for a queued report.
- **Writes** — `set_campaign_budget`, through the same preview-then-confirm flow
  and the same spend cap as Google's.

There is deliberately no `bing_search` or `bing_keyword_ideas`: Microsoft has no
query language and no v13 analogue, and a tool that only ever answers
"unsupported" costs an agent a call to discover that.

Write safety: every mutation previews first and applies only on confirm
(`ads confirm <token>` or re-running with `--confirm`); `ads audit` shows
every applied write; a client-side allow-list rejects invalid `googleAds:mutate`
operation keys; and guard rails (spend cap, bid-increase limit, blocked-op list)
are configurable via `GOOGLE_ADS_MAX_DAILY_BUDGET`,
`GOOGLE_ADS_MAX_BID_INCREASE_PCT`, and `GOOGLE_ADS_BLOCKED_OPS`. New
campaigns/ad groups/ads ship **PAUSED** by default.

## Shell completion

Homebrew installs completions automatically. For a manual install, `ads
completion` generates the script for your shell:

```bash
# bash (requires bash-completion)
ads completion bash > /usr/local/etc/bash_completion.d/ads

# zsh
ads completion zsh > "${fpath[1]}/_ads"

# fish
ads completion fish > ~/.config/fish/completions/ads.fish
```

See [`AGENTS.md`](AGENTS.md) for the contributor workflow and conventions.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
