# AGENTS.md

Multi-platform ads MCP server + CLI, in Go. Single binary named `ads`, all source
in `package main` at the repo root. (The Go module and GitHub repo are still
`github.com/Limetric/goads`.) The binary exposes two front-ends over one shared
set of tool handlers:

- **CLI** — `ads login google`, `ads google search …`, `ads google accounts`,
  `ads google budget set …` (for humans, scripts, CI, and the agent skill that
  drives it via shell).
- **MCP server** — `ads mcp` serves the same tools over stdio for MCP hosts
  (Claude Desktop, Cursor, …), under platform-prefixed names (`google_search`).

Each tool is defined once: a typed `Args` struct + a pure
`func(ctx, *Client, Args) (Result, error)` handler. The CLI wires flags → handler;
the MCP front-end registers the same handler with `mcp.AddTool`, which derives the
JSON input schema from the `Args` struct by reflection.

## Platforms

Every ad network lives in its own namespace: `ads <platform> <command>` on the
CLI, `<platform>_<tool>` over MCP. **Google Ads and Microsoft Advertising (Bing
Ads) are implemented today.** Shared infrastructure — `login`, `doctor`,
`config`, `confirm`, `audit`, `mcp`, `version` — is unnamespaced but
platform-aware (`ads login google`, `ads doctor bing`,
`ads config google set-customer`).

A platform exposes only what its API has. Bing has no query language and no
Keyword Planner analogue in v13, so there is no `bing_search` and no
`bing_keyword_ideas` — a tool that answers "unsupported" is worse than its
absence, because an agent has to call it to find out.

A platform is a `Platform` value (see `platform.go`) registered from a
package-level variable:

```go
var googlePlatform = registerPlatform(&Platform{Name: "google", …})
```

It supplies its CLI subcommands, MCP registration, login command, config
section, and doctor checks. **Adding a platform must not require edits to the
shared config/auth/doctor/MCP plumbing** — if it does, the abstraction is in the
wrong place. See `docs/name-map.md` for the naming rules.

## Commands

**Go code must be formatted before every commit** — CI rejects unformatted code.

```bash
go fmt ./...                  # Format — run before commit
go mod tidy                   # Resolve/lock deps (run once after clone)
go build -o build/ads .       # Build binary
go vet ./...                  # Lint
go tool staticcheck ./...     # Static analysis
go test ./... -count=1        # Unit tests (no network; uses httptest)

# Live smoke test against the real API (requires real credentials)
GOOGLE_ADS_DEVELOPER_TOKEN=… GOOGLE_ADS_CLIENT_ID=… GOOGLE_ADS_CLIENT_SECRET=… \
GOOGLE_ADS_REFRESH_TOKEN=… GOOGLE_ADS_LOGIN_CUSTOMER_ID=… \
go test -tags integration -count=1 -v ./...
```

## Layout

Core (platform-neutral):

- `main.go` — Cobra root command, subcommand wiring, `main()`.
- `platform.go` — the `Platform` struct + registry; the `login` parent command.
- `config.go` / `config_paths.go` — TOML loading, env overlay, path resolution.
- `auth.go` — OAuth2 token source, driven by a platform's `oauthClient`.
- `token_store.go` — per-platform refresh-token store, rotation write-back, and
  the one-time migration of deprecated env/TOML refresh tokens.
- `doctor.go` — `doctor` command, verdict classification, exit codes.
- `config_command.go` — `config path` / `config show` and the TOML writers.
- `mcp.go` — `ads mcp`; iterates platforms and namespaces their tools. A
  platform whose credentials don't resolve is skipped with a warning; the server
  fails only when nothing can be served.
- `safety.go` — the confirm-token flow: staging, TTL, double confirmation. It
  knows a write's *platform*, never its payload.
- `write_tool.go` — `WriteResult`, and the shared apply path over the
  `mutationApplier` interface each platform's client implements.
- `guards.go` — spend cap, bid-increase limit, blocked-op list.

Google provider:

- `platform_google.go` — the registered `Platform` value.
- `config_google.go` — `GoogleConfig`: credentials, endpoint, base-URL override, validation.
- `doctor_google.go` / `config_command_google.go` — Google's doctor probes and settings commands.
- `mcp_google.go` — every Google MCP tool registration.
- `client.go` — Google Ads **REST** client (`googleads.googleapis.com/v23`). No gRPC, no protobuf.
- `gaql.go` — GAQL query building + validation.
- `login.go` / `login_wizard.go` — `ads login google`, plus the shared loopback
  OAuth server both platforms capture their authorization code on.
- `write_google.go` — Google's dispatch routes and preview/apply helpers.
- `tool_*.go` — one file per tool (`Args` + handler + CLI subcommand). Test lives next to it.

Microsoft Advertising (Bing) provider:

- `platform_bing.go` — the registered `Platform` value.
- `config_bing.go` — `BingConfig` (the `[bing]` TOML table + `BING_ADS_*`),
  environments, Entra endpoint, validation.
- `doctor_bing.go` / `config_command_bing.go` — Bing's doctor probes and settings.
- `mcp_bing.go` — every Bing MCP tool registration.
- `bing_client.go` — Microsoft Advertising **REST** v13 client: per-service
  hosts, the four required headers, throttle backoff, fault parsing.
- `bing_report.go` / `bing_report_job.go` — the Reporting service (submit → poll
  → download ZIP → parse CSV) and the on-disk job handles that let a slow report
  be collected by a different process.
- `login_bing.go` — `ads login bing` (authorization code + PKCE).
- `write_bing.go` — Bing's dispatch route and per-item partial-failure handling.
- `tool_bing_*.go` — one file per tool group. Test lives next to it.

## Conventions (match these)

- All code is `package main` at the repo root. No `cmd/` or `internal/`.
- New tool = new `tool_<name>.go` + `tool_<name>_test.go`. Register it in **two**
  places: the platform's `Commands` (CLI) and its MCP registration function —
  for Google, `googlePlatform` in `platform_google.go` and `addGoogleTools` in
  `mcp_google.go`. Keep the two in sync.
- MCP tool names are written unprefixed at the registration site; the registrar
  applies the platform prefix. Never hand-write `google_` into a tool name.
- Write/mutating tools MUST go through `safety.go`: return a preview + confirm
  token first, execute only on `--confirm <token>`. Never mutate on the first call.
- Refresh tokens live in the token store (`token_store.go`), never in config
  TOML or an env var. A platform supplies a `tokenPolicy` saying whether its
  refresh token rotates; everything else — persistence, migration of deprecated
  seeds, `invalid_grant` handling — is shared.
- A confirmed write is applied through the platform's `mutationApplier`, and the
  staged record names the platform — that is how `ads confirm <token>` routes a
  token back to the API that staged it. New platform ⇒ set `Platform.NewApplier`.
- Partial success is not success. Where an API applies a batch item-by-item
  (Bing's `PartialErrors`, Google's `partialFailureError`), the apply must fail
  and say which items landed.
- Errors: wrap with `%w`, and make messages actionable (tell the user the fix).
- Tests are table-driven and offline — use `net/http/httptest` to fake the Ads API
  (set `GOOGLE_ADS_API_BASE_URL` / `BING_ADS_API_BASE_URL` to the test server; a
  loopback base URL is what puts a config in test mode). `//go:build integration`
  for live tests.

## Key references

- User-facing docs: `README.md` (shared concepts), `docs/google.md` and
  `docs/bing.md` (per-platform setup, login, and tool coverage),
  `docs/name-map.md` (CLI ↔ MCP name map). A new platform gets its own
  `docs/<platform>.md` and a row in the README's Platforms table; keep each
  guide's tool coverage in sync with that platform's MCP registration.
- Google Ads REST API (v23): <https://developers.google.com/google-ads/api/rest/overview>
- Microsoft Advertising REST API (v13): <https://learn.microsoft.com/en-us/advertising/guides/get-started?view=bingads-13>
  (sandbox: `*.api.sandbox.bingads.microsoft.com`, universal developer token `BBD37VB98`)
- MCP Go SDK: <https://github.com/modelcontextprotocol/go-sdk>
