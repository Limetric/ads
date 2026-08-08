package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// sharedCommands are the unnamespaced commands: infrastructure that spans every
// platform (one config file, one confirm store, one audit log, one MCP server).
// Anything else at the root would be a platform tool that escaped its namespace.
var sharedCommands = map[string]bool{
	"version": true,
	"mcp":     true,
	"doctor":  true,
	"login":   true,
	"config":  true,
	"confirm": true,
	"audit":   true,
	// Added by cobra itself.
	"completion": true,
	"help":       true,
}

// TestRootCommandsAreNamespaced is the guard the platform split exists for: a
// tool command added straight to rootCmd would take a name the next platform
// wants. Every command at the root must be shared infrastructure or a platform
// namespace.
func TestRootCommandsAreNamespaced(t *testing.T) {
	namespaces := map[string]bool{}
	for _, p := range platforms() {
		namespaces[p.Name] = true
	}
	for _, cmd := range rootCmd.Commands() {
		name := cmd.Name()
		if !sharedCommands[name] && !namespaces[name] {
			t.Errorf("`ads %s` is neither shared infrastructure nor a platform namespace — tool commands belong under `ads <platform>`", name)
		}
	}
}

func TestPlatformsAreRegistered(t *testing.T) {
	if len(platforms()) == 0 {
		t.Fatal("no platforms registered")
	}
	if _, err := lookupPlatform("google"); err != nil {
		t.Errorf("google should be registered: %v", err)
	}
}

// TestPlatformDefinitions checks the contract every platform has to satisfy, so
// a second one can't half-register and fail only at runtime.
func TestPlatformDefinitions(t *testing.T) {
	for _, p := range platforms() {
		t.Run(p.Name, func(t *testing.T) {
			// An underscore in the name would make the MCP prefix ambiguous:
			// `a_b_tool` could be platform "a" or "a_b".
			if strings.Contains(p.Name, "_") || p.Name != strings.ToLower(p.Name) {
				t.Errorf("platform name %q must be lowercase and underscore-free", p.Name)
			}
			if p.Title == "" || p.Short == "" {
				t.Error("platform needs a Title and a Short description for help text")
			}
			if len(p.Commands) == 0 {
				t.Error("platform registered no CLI commands")
			}
			if p.RegisterMCP == nil {
				t.Error("platform registered no MCP tools")
			}
			if p.Doctor == nil {
				t.Error("platform has no doctor check")
			}
			if p.ShowConfig == nil {
				t.Error("platform contributes nothing to `ads config show`")
			}
		})
	}
}

// TestPlatformCommandsAreWired proves the registry actually built the tree:
// `ads <platform>`, `ads login <platform>`, and `ads config <platform>`.
func TestPlatformCommandsAreWired(t *testing.T) {
	for _, p := range platforms() {
		if findSubcommand(rootCmd, p.Name) == nil {
			t.Errorf("`ads %s` is not wired into the root command", p.Name)
		}
		if p.Login != nil && findSubcommand(loginCmd, p.Name) == nil {
			t.Errorf("`ads login %s` is not wired up", p.Name)
		}
		if len(p.ConfigCommands) > 0 && findSubcommand(configCmd, p.Name) == nil {
			t.Errorf("`ads config %s` is not wired up", p.Name)
		}
	}
}

// TestGoogleCommandsMatchRegistration guards the CLI/MCP pairing AGENTS.md asks
// for: the Google namespace must expose the same command set the platform
// declares, in one place.
func TestGoogleCommandsMatchRegistration(t *testing.T) {
	google := findSubcommand(rootCmd, "google")
	if google == nil {
		t.Fatal("`ads google` is missing")
	}
	wired := map[string]bool{}
	for _, cmd := range google.Commands() {
		wired[cmd.Name()] = true
	}
	for _, cmd := range googlePlatform.Commands {
		if !wired[cmd.Name()] {
			t.Errorf("`ads google %s` is declared but not wired", cmd.Name())
		}
	}
}

func TestLookupPlatformUnknown(t *testing.T) {
	_, err := lookupPlatform("bing")
	if err == nil {
		t.Fatal("expected an error for an unregistered platform")
	}
	// The message has to name the platforms that do exist, or the user is left
	// guessing at the namespace.
	if !strings.Contains(err.Error(), "google") {
		t.Errorf("error should list the supported platforms: %v", err)
	}
}

func TestRegisterPlatformRejectsDuplicates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate platform name should panic")
		}
		// The panic fires before the append, so the registry is untouched and
		// the other tests still see exactly the real platforms.
	}()
	registerPlatform(&Platform{Name: "google"})
}

func TestRegisterPlatformRejectsMissingName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a nameless platform should panic")
		}
	}()
	registerPlatform(&Platform{Title: "Nameless"})
}

func findSubcommand(parent *cobra.Command, name string) *cobra.Command {
	for _, cmd := range parent.Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}
	return nil
}
