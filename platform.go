package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// A Platform is one ad network's surface: a CLI namespace (`ads google …`), an
// MCP tool prefix (`google_…`), a credential set, and its own health checks.
//
// Everything network-specific hangs off this struct. Adding a platform means
// adding a Platform value plus its tool_*.go files — the shared config, auth,
// doctor, login, and MCP plumbing never learns the platform's name.
type Platform struct {
	// Name is the namespace: the `ads <Name>` CLI subcommand and the
	// `<Name>_` MCP tool prefix. Lowercase, no underscores (an underscore
	// would make the MCP prefix ambiguous).
	Name string
	// Title is the human-readable network name ("Google Ads"), used in help
	// text and doctor output.
	Title string
	// Short is the one-line description of the `ads <Name>` command group.
	Short string
	// Commands are the platform's tool subcommands, rooted at `ads <Name>`.
	Commands []*cobra.Command
	// Login is the `ads login <Name>` subcommand. Optional.
	Login *cobra.Command
	// ConfigCommands are platform-specific settings subcommands, rooted at
	// `ads config <Name>`. Optional.
	ConfigCommands []*cobra.Command
	// RegisterMCP registers the platform's tools on an MCP server. Tool names
	// passed to addTool are unprefixed; the registrar applies `<Name>_`, so a
	// platform cannot leak an unnamespaced tool by forgetting the prefix.
	RegisterMCP func(ctx context.Context, reg *toolRegistrar) error
	// ShowConfig prints the platform's resolved settings for `ads config show`,
	// with secrets redacted.
	ShowConfig func(w io.Writer) error
	// NewApplier builds the client that applies a staged write for this
	// platform. `ads confirm <token>` uses it to route a token to the API that
	// staged it; a platform with no write tools may leave it nil.
	NewApplier func(ctx context.Context) (mutationApplier, error)
	// Configured reports whether the user has set this platform up at all. It
	// is what keeps a multi-platform binary usable by someone who only uses one
	// network: `ads doctor` with no argument skips platforms nobody configured,
	// instead of reporting a healthy setup as broken. Nil means "always
	// configured" — the platform is then always checked.
	Configured func() bool
	// Doctor checks the platform's setup, printing a line per check to w. It
	// verifies that credentials resolve and, unless offline, probes the live
	// API. The caller prints the status line and picks the exit code.
	Doctor func(ctx context.Context, w io.Writer, offline bool) (liveResult, error)
}

// registeredPlatforms is populated by package-level variable initialization in
// each platform_*.go, which the language guarantees runs before any init()
// function — so the command tree assembled in main.go's init() sees every
// platform regardless of file order.
var registeredPlatforms []*Platform

// registerPlatform adds a platform to the registry. Platform files call it from
// a package-level var so registration cannot be missed:
//
//	var googlePlatform = registerPlatform(&Platform{…})
func registerPlatform(p *Platform) *Platform {
	if p.Name == "" {
		panic("platform registered without a name")
	}
	for _, existing := range registeredPlatforms {
		if existing.Name == p.Name {
			panic("duplicate platform registered: " + p.Name)
		}
	}
	registeredPlatforms = append(registeredPlatforms, p)
	return p
}

// platforms returns every registered platform, in registration order.
func platforms() []*Platform { return registeredPlatforms }

// lookupPlatform finds a platform by namespace name.
func lookupPlatform(name string) (*Platform, error) {
	for _, p := range registeredPlatforms {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown platform %q — supported: %s", name, strings.Join(platformNames(), ", "))
}

// configured reports whether the user has set this platform up. A platform
// that does not answer the question counts as configured, so an omitted hook
// can never hide a platform from `ads doctor`.
func (p *Platform) configured() bool {
	return p.Configured == nil || p.Configured()
}

// platformNames lists the registered namespaces, for help text and errors.
func platformNames() []string {
	names := make([]string, len(registeredPlatforms))
	for i, p := range registeredPlatforms {
		names[i] = p.Name
	}
	return names
}

// command builds `ads <Name>` — the namespace holding this platform's tools.
func (p *Platform) command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   p.Name,
		Short: p.Short,
	}
	for _, sub := range p.Commands {
		cmd.AddCommand(sub)
	}
	return cmd
}

// configCommand builds `ads config <Name>` — the platform's settings commands.
// It returns nil when the platform has none.
func (p *Platform) configCommand() *cobra.Command {
	if len(p.ConfigCommands) == 0 {
		return nil
	}
	cmd := &cobra.Command{
		Use:   p.Name,
		Short: p.Title + " settings",
	}
	for _, sub := range p.ConfigCommands {
		cmd.AddCommand(sub)
	}
	return cmd
}

// loginCmd is the shared parent for per-platform sign-in. Signing in is
// platform-specific (different OAuth clients, scopes, and prerequisites), so
// there is no unnamespaced `ads login`.
var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Sign in to an ad platform and save credentials",
	Long:  "Sign in to one ad platform. Each platform has its own OAuth client and\nprerequisites, so pick the one you want:\n\n  ads login google",
}
