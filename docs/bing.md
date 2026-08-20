# Microsoft Advertising (Bing Ads)

Setup, sign-in, and the full tool surface for the `bing` platform —
`ads bing …` on the CLI, `bing_…` over MCP.

New to `ads`? Start at the [README](../README.md) for installation and the
concepts shared across platforms (token store, confirm flow, output formats).

## Two differences from Google, before you script against it

- **Metrics are asynchronous.** Microsoft has no query language; every figure
  comes from the Reporting service, which queues a job. The metric tools wait up
  to 45 seconds and then return either rows or a job handle —
  `report queued: job_8f3c` → `ads bing report fetch job_8f3c`. Handles are
  saved on disk, so a handle from the CLI is fetchable from the MCP server and
  the other way round.
- **Money is plain currency, not micros.** `--daily-budget 30` means 30 in the
  account's currency; `ads bing account-info` reports which. Google's
  `--amount-micros` has no equivalent here.

## Prerequisites

1. **A Microsoft Entra application registration** — Azure portal → **App
   registrations**. Register a **public/native client**, supporting accounts in
   any organizational directory **and** personal Microsoft accounts, with the
   redirect URI `http://localhost:8086`.

   Entra requires the redirect URI to match the registered value literally, so
   spell it `localhost`, not `127.0.0.1`. If you change the port with `--port`,
   register the matching URI too.

   `ads` signs in as a public client using **PKCE**, not a client secret — a CLI
   cannot keep a secret on someone else's machine, and Entra rejects a public
   client that sends one. If you registered a web application instead, set
   `BING_ADS_CLIENT_SECRET` and it is sent alongside PKCE, which Entra accepts.

2. **A Microsoft Advertising developer token** — for production only. The
   sandbox needs none; see below.

## Guided setup

```bash
ads login bing --client-id <entra-application-id>   # browser sign-in (auth code + PKCE)
ads config bing set-account 123456789               # default account for later commands
ads doctor bing                                     # verify it works
```

The sign-in opens your browser, captures the authorization code on
`localhost:8086`, exchanges it for a refresh token, and saves it to the token
store. `--no-browser` prints the URL instead.

Because Microsoft invalidates the old refresh token the moment a new one is
issued, `ads login bing` checks that the token store is writable *before*
starting the browser flow — a sign-in that cannot be saved would spend a
credential for nothing.

## Sandbox

The sandbox needs no developer token of your own: selecting it fills in
Microsoft's universal sandbox token (`BBD37VB98`) for the run.

```bash
ads login bing --client-id <id> --environment sandbox
# or: export BING_ADS_ENVIRONMENT=sandbox
```

Sandbox and production have **entirely separate credentials** — a separate
application registration and a separate sign-in. Selecting the sandbox never
overwrites the production developer token you have configured; the sandbox value
is applied per run, not persisted.

## Manual setup (scripts and CI)

```bash
export BING_ADS_DEVELOPER_TOKEN=...   # production only
export BING_ADS_CLIENT_ID=...         # Entra application (client) ID
export BING_ADS_ACCOUNT_ID=...        # default ad account
export BING_ADS_CUSTOMER_ID=...       # optional manager account — see below

ads login bing
ads doctor bing
ads bing accounts
```

Settings other than the refresh token can live in the `[bing]` table of
`config.toml` (`ads config path` shows where):

```toml
[bing]
developer_token    = "..."
client_id          = "..."
customer_id        = "..."
default_account_id = "123456789"
environment        = "production"
```

### Environment variables

| Variable | Purpose |
| --- | --- |
| `BING_ADS_DEVELOPER_TOKEN` | Developer token (production only) |
| `BING_ADS_CLIENT_ID` | Entra application (client) ID |
| `BING_ADS_CLIENT_SECRET` | Only if you registered a web application |
| `BING_ADS_CUSTOMER_ID` | Manager account — optional, see below |
| `BING_ADS_ACCOUNT_ID` | Default ad account, so `--account-id` can be omitted |
| `BING_ADS_ENVIRONMENT` | `production` (default) or `sandbox` |

### The manager account is optional

Microsoft requires a customer ID on most operations, but you rarely need to
supply one: `ads` reads it from the ad account on first use and sends it from
then on. Set `BING_ADS_CUSTOMER_ID` explicitly only to pin a particular manager
account.

## The token store must be writable

Microsoft replaces the refresh token on every refresh and invalidates the old
one, so Bing's sign-in **cannot** live in an environment variable at all — a
pinned token would work exactly once. It lives in the token store, which has to
be writable.

```bash
ads doctor bing                            # reports whether it is
export GOADS_TOKEN_STORE=/path/to/tokens   # containers and CI: mount a writable volume
```

## Everyday commands

```bash
ads bing accounts                                   # accounts this sign-in can reach
ads bing account-info                               # name, currency, time zone, status
ads bing campaigns                                  # settings and budgets (sub-second)
ads bing ad-groups --campaign-id 1
ads bing keywords --ad-group-id 2

ads bing campaign-performance --format table        # spend, clicks, conversions
ads bing keyword-performance --days 7
ads bing report fetch job_8f3c                      # collect a queued report

ads bing budget set --campaign-id 1 --daily-budget 30
ads confirm <token>
```

Entity commands (`campaigns`, `ad-groups`, `keywords`) return settings and are
fast. Anything with *performance* in the name goes through Reporting and may
queue. The metric commands take `--days`, or `--date-start` / `--date-end`, and
`--columns` to override the report columns with Microsoft column names.

## Tool coverage

Bing exposes what Microsoft has — 10 tools, each an `ads bing …` subcommand and
a `bing_…` MCP tool. See [`name-map.md`](name-map.md) for the full map.

**Reads** — `list_accounts`, `account_info`, `campaigns`, `ad_groups`,
`keywords` (entity settings, sub-second), and `campaign_performance`,
`keyword_performance`, `ad_performance` (metrics, via Reporting), plus
`report_fetch` for a queued report.

**Writes** — `set_campaign_budget`, through the same preview-then-confirm flow
and the same spend cap as Google's.

That cap is shared, and its environment variable keeps Google's name:
`GOOGLE_ADS_MAX_DAILY_BUDGET` (currency units, default 50) also bounds
`ads bing budget set`. Since Bing budgets are already in currency units, a
`--daily-budget 100` is rejected until you raise it.

There is deliberately no `bing_search` and no `bing_keyword_ideas`: Microsoft
has no query language, and the Ad Insight equivalent is not part of v13. A tool
that only ever answers "unsupported" is worse than its absence, because an agent
has to call it to find out.

## Troubleshooting

Start with `ads doctor bing`.

| Symptom | Likely cause |
| --- | --- |
| `no application (client) ID` | Pass `--client-id` or set `BING_ADS_CLIENT_ID` |
| Entra rejects the redirect | The registered URI must be exactly `http://localhost:8086` |
| "Public clients can't send a client secret" | Registered as a web app — drop the secret, or keep it and let PKCE ride alongside |
| Sign-in refuses to start | Token store not writable — `ads doctor bing` says where it is |
| A report never returns rows | It queued — fetch it with `ads bing report fetch <job>` |
| Figures look 1,000,000× off | Bing budgets and bids are plain currency, not micros |
