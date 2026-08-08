package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

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
			fmt.Fprintln(cmd.OutOrStdout(), "environment only (no config file)")
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
		source := resolved
		if source == "" {
			source = "(none — environment only)"
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "config file:          %s\n", source)
		for _, p := range platforms() {
			if p.ShowConfig == nil {
				continue
			}
			fmt.Fprintf(out, "\n[%s] %s\n", p.Name, p.Title)
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
func upsertConfigKey(path, key, value string) error {
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse existing config %q: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read config %q: %w", path, err)
	}
	settings[key] = value
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(settings); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	// Write-then-rename so an interrupted write can never truncate a config
	// file that holds credentials.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config %q: %w", path, err)
	}
	return nil
}

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
