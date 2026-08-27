// Command ads is a multi-platform ad management MCP server and CLI.
//
// It exposes each ad platform's tools two ways: as a conventional CLI
// (`ads google search …`) and as an MCP server over stdio (`ads mcp`), where the
// same tools appear namespaced (`google_search`). Both front-ends share one
// handler per tool; see tool_*.go. Google Ads is the platform implemented today
// — see platform.go for what a second one has to supply.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// exitErr lets a command request a specific process exit code. When a command
// returns one, it has already printed its own diagnostics, so main() exits with
// the requested code without printing the generic "error:" line.
type exitErr struct {
	code int
	err  error
}

func (e *exitErr) Error() string { return e.err.Error() }
func (e *exitErr) Unwrap() error { return e.err }

// configPath is the optional --config flag (a TOML credentials/settings file).
// When empty, configuration comes from the environment and the default path.
var configPath string

var rootCmd = &cobra.Command{
	Use:           "ads",
	Short:         "Ad platform management — CLI and MCP server",
	Long:          "ads exposes ad platform tools as both a CLI and an MCP server (`ads mcp`).\n\nEvery platform has its own namespace: `ads google campaigns` on the CLI,\n`google_campaigns` over MCP.\n\nCredentials are read from the environment or a TOML config file.\nRun `ads doctor` to check your setup.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

var versionVerbose bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print ads version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		out := cmd.OutOrStdout()
		if versionVerbose {
			fmt.Fprintln(out, versionVerboseString())
			return
		}
		fmt.Fprintln(out, versionString())
	},
}

func init() {
	rootCmd.Version = versionString()
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "path to TOML credentials/settings file (env overrides)")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable coloured output (also honours NO_COLOR)")

	versionCmd.Flags().BoolVarP(&versionVerbose, "verbose", "v", false, "print detailed build metadata")
	doctorCmd.Flags().BoolVar(&doctorOffline, "offline", false, "skip the live API check; only verify that credentials resolve")

	// Shared infrastructure. These are not namespaced — one config file, one
	// confirm store, one audit log, one MCP server across every platform — but
	// each grows a platform dimension below.
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(confirmCmd)
	rootCmd.AddCommand(auditCmd)

	// Platform namespaces. Everything platform-specific arrives through the
	// registry, so adding a network means adding a platform_*.go — not editing
	// this wiring. Package-level variable initialization (where platforms
	// register) is complete before any init() runs, so the registry is full.
	for _, p := range platforms() {
		rootCmd.AddCommand(p.command())
		if p.Login != nil {
			loginCmd.AddCommand(p.Login)
		}
		if cmd := p.configCommand(); cmd != nil {
			configCmd.AddCommand(cmd)
		}
	}
}

func main() {
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	// A command that carries its own exit code has already reported details;
	// just exit with that code (e.g. doctor's inconclusive vs failed).
	var ex *exitErr
	if errors.As(err, &ex) {
		os.Exit(ex.code)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
