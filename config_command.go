package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and update configuration",
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the config path selected by ads",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		resolved, err := resolveConfigPath(configPath)
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
		if resolved == "" {
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, newStyles(out).muted("environment only (no config file)"))
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), resolved)
		return nil
	},
}

// configShowCmd prints the fully resolved configuration (file + env overlay)
// with credentials redacted, so users can see which values are in effect
// without exposing secrets in scrollback or logs. The config file is shared;
// each platform renders its own section.
var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the resolved configuration (secrets redacted)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		resolved, err := resolveConfigPath(configPath)
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
		out := cmd.OutOrStdout()
		st := newStyles(out)
		source := resolved
		if source == "" {
			source = st.muted("(none — environment only)")
		}
		fmt.Fprintf(out, "%s%s\n", st.field("config file", configFieldWidth), source)
		for _, p := range platforms() {
			if p.ShowConfig == nil {
				continue
			}
			fmt.Fprintf(out, "\n%s\n", st.header(fmt.Sprintf("[%s] %s", p.Name, p.Title)))
			if err := p.ShowConfig(out); err != nil {
				return err
			}
		}
		return nil
	},
}

// writableConfigPath returns the config file to write settings to: the
// explicit --config path when given, otherwise the default location (whose
// directory is created on demand — unlike resolveConfigPath, a missing default
// file is fine because we are about to create it).
func writableConfigPath(explicit string) (string, error) {
	if explicit != "" {
		// Create missing parents so a fresh --config path works, matching the
		// default-path branch below.
		if dir := filepath.Dir(explicit); dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", fmt.Errorf("create config directory %q: %w", dir, err)
			}
		}
		return explicit, nil
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("no usable config directory (%v) — set HOME/XDG_CONFIG_HOME or pass --config", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config directory %q: %w", dir, err)
	}
	return filepath.Join(dir, defaultConfigFile), nil
}

// upsertConfigKey sets one key in a TOML config file, preserving all other
// keys. The file is created if missing and rewritten 0600 (it holds secrets).
// Comments do not survive — the file is re-encoded from its parsed form, which
// the settings commands document, and which is acceptable because the user
// asked for this write explicitly.
func upsertConfigKey(path, key, value string) error {
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse existing config %q: %w", path, err)
		}
	case os.IsNotExist(err):
		// Fall through with an empty map: the file is about to be created.
	default:
		return fmt.Errorf("read config %q: %w", path, err)
	}
	settings[key] = value
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(settings); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	// Write-then-rename so an interrupted write can never truncate a config
	// file that holds credentials.
	if err := writeFileAtomic(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

// deleteConfigKey removes one top-level key from a TOML config file, leaving
// the rest of the file byte-for-byte intact — comments and formatting included.
// A missing file or a key that isn't there is not an error: both mean the key
// is already gone, which is all the caller wanted.
//
// It edits lines instead of re-encoding, unlike upsertConfigKey, because this
// one runs *automatically* — on the first command after an upgrade, to retire a
// deprecated credential into the token store. Re-encoding would silently strip
// every comment from a file the user never asked us to touch.
//
// The edit is committed only when the result still parses and differs from the
// original by exactly that one key. Anything else — a multi-line value, a
// layout the line scan misreads — leaves the file alone and reports the
// failure, which callers treat as "warn and move on" rather than as a reason to
// fail the command.
func deleteConfigKey(path, key string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	before := map[string]any{}
	if err := toml.Unmarshal(data, &before); err != nil {
		return fmt.Errorf("parse existing config %q: %w", path, err)
	}
	if _, ok := before[key]; !ok {
		return nil
	}

	edited := dropTopLevelKeyLines(data, key)
	after := map[string]any{}
	if err := toml.Unmarshal(edited, &after); err != nil {
		return fmt.Errorf("removing %s from %q would not leave valid TOML: %w", key, path, err)
	}
	delete(before, key)
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("removing %s from %q would have changed other settings", key, path)
	}
	if err := writeFileAtomic(path, edited, 0o600); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

// dropTopLevelKeyLines removes the `key = …` assignments that sit above the
// first table header, which is where a top-level key lives. Everything else,
// including a same-named key inside a table, is left untouched. The caller
// re-parses the result, so a wrong guess here is caught rather than committed.
func dropTopLevelKeyLines(data []byte, key string) []byte {
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inTable = true
		}
		if !inTable && isAssignmentTo(trimmed, key) {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n"))
}

// isAssignmentTo reports whether a trimmed line assigns to key, in any of the
// three spellings TOML allows for a bare key.
func isAssignmentTo(line, key string) bool {
	for _, form := range []string{key, `"` + key + `"`, `'` + key + `'`} {
		rest, ok := strings.CutPrefix(line, form)
		if ok && strings.HasPrefix(strings.TrimSpace(rest), "=") {
			return true
		}
	}
	return false
}

// configFieldWidth is the column `ads config show` lines its values up in,
// shared by every platform's section so the report reads as one table.
const configFieldWidth = 22

// redactSecret renders a credential for display without exposing it; the last
// four characters are kept so two credentials can be told apart.
func redactSecret(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 8 {
		return "set (redacted)"
	}
	return "set (…" + s[len(s)-4:] + ")"
}

func init() {
	configCmd.AddCommand(configPathCmd)
	configCmd.AddCommand(configShowCmd)
}
