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
fighting for names. Shared plumbing (`login`, `doctor`, `config`, `confirm`,
`audit`, `mcp`) stays unnamespaced, with a platform argument where it needs one.

You only configure the platforms you use: `ads doctor` skips the ones you
haven't set up, and `ads mcp` serves the tools it can and says on stderr which
platform it left out and why.

## Platforms

| Platform | Setup guide | Surface |
| --- | --- | --- |
| Google Ads | [`docs/google.md`](docs/google.md) | 49 tools — GAQL search, reporting, and campaign management |
| Microsoft Advertising (Bing Ads) | [`docs/bing.md`](docs/bing.md) | 10 tools — entity reads, async reporting, budgets |

Each guide covers that platform's prerequisites, sign-in, environment
variables, tool coverage, and troubleshooting.
[`docs/name-map.md`](docs/name-map.md) is the full CLI ↔ MCP name map.

## Install

Three ways in, no wrong answer:

```bash
# Homebrew (macOS/Linux)
brew install Limetric/tap/ads

# Prebuilt binary (macOS, Linux, Windows) — grab one from the releases page
open https://github.com/Limetric/goads/releases/latest

# Build from source
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

Each platform's guide covers the rest: deprecated env-var seeds, and running
several sign-ins side by side.

### Defaults and output

Work with one account most of the time? Set a default once and drop the
per-command flag (any explicit flag still wins):

```bash
ads config google set-customer 123-456-7890   # or: ads config bing set-account 123456789
ads google campaigns                          # uses the default account
ads config show                               # see the resolved config (secrets redacted)
```

Read commands print JSON by default; pass `--format table` or `--format csv`
for human- or spreadsheet-friendly output (`campaigns`, `ads`, `keywords …`,
`search`, `report`, and the other row-returning reads all take it):

```bash
ads google campaigns --format table
```

The human-facing commands — `login`, `doctor`, `config show` — colour their
output when they are writing to a terminal, and print plain text whenever they
are not (a pipe, a redirect, CI). To turn it off explicitly, pass `--no-color`
or set `NO_COLOR`; to keep it through a pager, set `CLICOLOR_FORCE=1`:

```bash
ads doctor --no-color            # plain text on a terminal
CLICOLOR_FORCE=1 ads doctor | less -R
```

### Writes preview first

Every mutation previews first and applies only on confirm:

```bash
ads google budget set --budget-id 555 --amount-micros 5000000
# → prints a preview and a confirm token, e.g. a1b2c3d4e5f6a7b8
ads confirm a1b2c3d4e5f6a7b8   # applies the staged change as previewed
ads audit                      # log of every write ads has applied
```

(Re-running the original command with `--confirm <token>` still works too.)

Beyond the confirm flow: a client-side allow-list rejects invalid mutation
operation keys, guard rails (spend cap, bid-increase limit, blocked-op list)
bound every write across platforms, and new campaigns, ad groups, and ads ship
**PAUSED** by default. See [`docs/google.md`](docs/google.md#guard-rails) for the
thresholds and their defaults.

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
