# ads

**Your ad accounts, minus the dashboard-clicking.** `ads` is the CLI and MCP
server that lets you (or your favorite AI agent) query campaigns, check spend,
and ship budget changes without ever opening a browser tab. Most Google Ads
MCP servers, including the official one, only let an agent look — `ads` also
lets it act: create a campaign, change a budget, adjust a bid, all through the
same preview-then-confirm flow you'd use by hand. Google Ads and Microsoft
Advertising (Bing Ads) are supported today, with more platforms on the way.
And it's a direct line — `ads` talks straight to the Google Ads and Microsoft
Advertising APIs from your own machine, with no relay server in between to
see your data or credentials.

It ships as a single binary with two front-ends over one shared set of tools:

- **CLI** — `ads google search …`, `ads google accounts`, `ads google budget set …`.
  Scriptable, pipeable into `jq`, usable in CI. This is what the bundled agent
  **skill** drives.
- **MCP server** — `ads mcp` serves the same tools over stdio to MCP hosts
  (Claude Desktop, Cursor, …).

Every platform gets its own namespace — `ads google campaigns` on the CLI,
`google_campaigns` over MCP — so networks slot in beside each other rather than
fighting for names. Shared plumbing (`login`, `doctor`, `config`, `confirm`,
`audit`, `mcp`) stays unnamespaced, with a platform argument where it needs one.

You only configure the platforms you use: `ads doctor` skips the ones you
haven't set up, and `ads mcp` serves the tools it can and says on stderr which
platform it left out and why.

## Platforms

| Platform | Setup guide | Surface |
| --- | --- | --- |
| Google Ads | [`docs/google.md`](docs/google.md) | 53 tools — GAQL search, reporting, and campaign management |
| Microsoft Advertising (Bing Ads) | [`docs/bing.md`](docs/bing.md) | 10 tools — entity reads, async reporting, budgets |

Each guide covers that platform's prerequisites, sign-in, environment
variables, tool coverage, and troubleshooting.
[`docs/name-map.md`](docs/name-map.md) is the full CLI ↔ MCP name map.

## Install

Three ways in, no wrong answer:

- **Homebrew** (macOS/Linux):

  ```bash
  brew install Limetric/tap/ads
  ```

- **Prebuilt binary** (macOS, Linux, Windows) — grab one from the
  [releases page](https://github.com/Limetric/ads/releases/latest).

- **Build from source**:

  ```bash
  go build -o build/ads .
  ```

## Quick start

```bash
ads login google      # guided sign-in, verifies the connection
ads doctor            # confirm credentials resolve
ads google accounts   # list accessible accounts

ads google search --customer-id 123-456-7890 \
  --query 'SELECT campaign.id, campaign.name FROM campaign LIMIT 10' | jq
```

Google's prerequisites (Cloud project, OAuth client, developer token) and the
non-interactive `--no-input` path for CI are in
[`docs/google.md`](docs/google.md). Using Microsoft Advertising? Start at
[`docs/bing.md`](docs/bing.md) — it differs in ways worth reading first.

## Integrations

### As an MCP server

`ads mcp` serves the same tools over stdio to MCP hosts, under their platform
prefix — `google_search`, `google_campaigns`, `google_set_campaign_budget`, …
See [`docs/mcp.md`](docs/mcp.md) for host config and credentials.

### As a Claude Code plugin

The repo bundles a skill (`plugins/ads/skills/ads/SKILL.md`, symlinked at
`.claude/skills/ads` for contributors working in a clone) that teaches an agent
when and how to drive the CLI — token-efficient because nothing loads until it's
relevant, and big result sets stay in the shell (`| jq`) instead of the context
window.

If you installed `ads` via Homebrew and don't have the repo cloned, install the
skill as a Claude Code plugin instead:

```text
/plugin marketplace add Limetric/ads
/plugin install ads@ads
```

### As a Codex plugin

Codex reads the same skill through its own plugin manifest:

```bash
codex plugin marketplace add Limetric/ads
codex plugin add ads@ads
```

## Concepts

### Where the refresh token lives

`ads login` writes the refresh token to a per-platform **token store**
(`0600` files under `~/.config/ads/state/tokens/`), never to `config.toml` or
an env var — it's writable so ads keeps working on platforms that rotate the
refresh token on every use.

```bash
ads doctor google        # store location, writability, sign-in age
export GOADS_TOKEN_STORE=/path/to/tokens   # containers/CI: mount a writable volume
```

See each platform's guide for deprecated env-var seeds and running multiple
sign-ins side by side.

### Defaults and output

Set a default account once and drop the per-command flag (an explicit flag
still wins):

```bash
ads config google set-customer 123-456-7890   # or: ads config bing set-account 123456789
ads google campaigns                          # uses the default
ads config show                               # resolved config, secrets redacted
```

Reads print JSON by default; add `--format table` or `--format csv` for
human- or spreadsheet-friendly output:

```bash
ads google campaigns --format table
```

`login`, `doctor`, and `config show` colour their output on a terminal and
print plain text otherwise. Force it either way with `--no-color`/`NO_COLOR`
or `CLICOLOR_FORCE=1`.

### Writes preview first

Every mutation previews first and applies only on confirm:

```bash
ads google budget set --budget-id 555 --amount-micros 5000000
# → preview + confirm token, e.g. a1b2c3d4e5f6a7b8
ads confirm a1b2c3d4e5f6a7b8   # applies it (or re-run with --confirm <token>)
ads audit                      # log of every write ads has applied
```

Guard rails (spend cap, bid-increase limit, blocked-op list) bound every
write, and new campaigns/ad groups/ads ship **PAUSED** by default — see
[`docs/google.md`](docs/google.md#guard-rails) for thresholds.

See [`AGENTS.md`](AGENTS.md) for the contributor workflow and conventions.

## License

Apache-2.0. See [`LICENSE`](LICENSE).
