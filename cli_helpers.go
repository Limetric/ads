package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// newGoogleClient builds a Google Ads API client from the resolved
// configuration (the global --config flag plus the environment). Shared by
// every `ads google` subcommand and by the MCP server's google_* tools.
func newGoogleClient(ctx context.Context) (*Client, error) {
	cfg, err := loadGoogleConfig(configPath)
	if err != nil {
		return nil, err
	}
	return NewClient(ctx, cfg)
}

// printJSON writes v as indented JSON followed by a newline. This is the
// default CLI output so results pipe cleanly into jq.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// rowSource is implemented by read-tool results whose rows can render as a
// table or CSV. fields gives the column order (the GAQL SELECT order).
type rowSource interface {
	tableRows() (rows []json.RawMessage, fields []string)
}

// printResult renders a read-tool result for the CLI: json (the default,
// printing the full structured result) or table/csv over its rows. This is
// CLI-only shaping — MCP always returns the structured result.
func printResult(w io.Writer, format string, res any) error {
	f := strings.ToLower(strings.TrimSpace(format))
	switch f {
	case "", "json":
		return printJSON(w, res)
	case "table", "csv":
		rs, ok := res.(rowSource)
		if !ok {
			return fmt.Errorf("this command cannot render %s output", f)
		}
		rows, fields := rs.tableRows()
		rendered := formatTable(rows, fields)
		if f == "csv" {
			rendered = formatCSV(rows, fields)
		}
		_, err := fmt.Fprint(w, rendered)
		return err
	default:
		return fmt.Errorf("unknown format %q — use json, table, or csv", format)
	}
}

// addFormatFlag registers the shared --format flag on a read command.
func addFormatFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "format", "json", "output format: json, table, or csv")
}
