package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// mcpCmd starts the MCP server over stdio. It exposes the same tools as the CLI
// subcommands, backed by the same handlers.
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run the ads MCP server over stdio",
	Long:  "Serve every platform's tools to an MCP host (Claude Desktop, Cursor, …) over stdio.\n\nTool names carry their platform: `google_campaigns`, `google_search`, ….\n\nConfigure your host to run `ads mcp` and pass credentials via the environment.",
	Args:  cobra.NoArgs,
	RunE:  runMCP,
}

func runMCP(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	server := mcp.NewServer(&mcp.Implementation{Name: "ads", Version: versionString()}, nil)
	if err := registerTools(ctx, server); err != nil {
		return err
	}
	// Run blocks until stdin closes or the context is cancelled.
	return server.Run(ctx, &mcp.StdioTransport{})
}

// registerTools wires every registered platform's tools into the MCP server,
// each under its own `<platform>_` prefix.
//
// Keep each platform's tool list in sync with the CLI subcommands it exposes
// through Platform.Commands.
func registerTools(ctx context.Context, server *mcp.Server) error {
	for _, p := range platforms() {
		if p.RegisterMCP == nil {
			continue
		}
		// A platform whose credentials don't resolve fails the whole server,
		// as it did before the platform split. An MCP host has no way to
		// surface a half-configured server, so a loud failure at startup beats
		// silently serving a subset of the tools.
		//
		// Revisit when a second platform lands: a user configured for only one
		// network would then be unable to start the server at all. The fix is a
		// deliberate policy (skip-with-warning, or an explicit platform
		// allow-list), not an accident of this loop.
		if err := p.RegisterMCP(ctx, &toolRegistrar{server: server, prefix: p.Name + "_"}); err != nil {
			return fmt.Errorf("register %s tools: %w", p.Title, err)
		}
	}
	return nil
}

// toolRegistrar is a platform's handle on the MCP server. It carries the
// platform's namespace so tool names are prefixed on the way in rather than
// spelled out at each of the ~50 registration sites.
type toolRegistrar struct {
	server *mcp.Server
	prefix string
}

// addTool adapts a shared handler func(ctx, C, A) (R, error) into an MCP tool,
// returning the result as both a JSON text block and structured content. name
// is the platform-local name; the registrar's prefix is applied here.
//
// The input schema for A is derived by the SDK via reflection over its struct
// tags (the `jsonschema` tag value becomes each field's description). The
// handler's output type is deliberately `any` (not R): that opts out of the
// SDK's output-schema generation and validation, which otherwise mis-infers
// result fields typed `[]json.RawMessage` (a `[]byte` alias) as byte arrays and
// rejects the real object rows at call time.
//
// C is the platform's client type, so this adapter is shared by every platform.
func addTool[C, A, R any](reg *toolRegistrar, client C, name, desc string, handler func(context.Context, C, A) (R, error)) {
	mcp.AddTool(reg.server, &mcp.Tool{Name: reg.prefix + name, Description: desc},
		func(ctx context.Context, _ *mcp.CallToolRequest, args A) (*mcp.CallToolResult, any, error) {
			result, err := handler(ctx, client, args)
			if err != nil {
				return nil, nil, err
			}
			text, _ := json.MarshalIndent(result, "", "  ")
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
				StructuredContent: result,
			}, nil, nil
		})
}

// toolError is a small helper for handlers to produce consistent messages.
func toolError(tool string, err error) error {
	return fmt.Errorf("%s: %w", tool, err)
}
