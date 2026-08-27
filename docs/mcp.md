# Using ads as an MCP server

`ads mcp` serves every configured platform's tools over stdio, under a
platform prefix — `google_search`, `google_campaigns`,
`google_set_campaign_budget`, `bing_…`, and so on.

New to `ads`? Start at the [README](../README.md) for installation and the
concepts shared across platforms (token store, confirm flow, output formats).

## Host config

Point your MCP host at the binary:

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

The `env` block supplies Google's app credentials (developer token, OAuth
client) — include them only if they aren't already in `config.toml`. The
refresh token is separate and never belongs in host config: run
`ads login google` once first and it's read from the token store. Add
`"GOADS_TOKEN_STORE": "..."` if the host runs somewhere `~/.config/ads` isn't
writable.

Add `BING_ADS_DEVELOPER_TOKEN`, `BING_ADS_CLIENT_ID`, and (optionally)
`BING_ADS_CUSTOMER_ID` / `BING_ADS_ACCOUNT_ID` to serve `bing_…` tools as well,
after `ads login bing`. A platform whose credentials don't resolve is skipped
with a warning rather than taking the server down, so configuring one network
is fine.
