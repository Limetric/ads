package main

import (
	"os"
	"path/filepath"
)

// configDirName is the per-user subdirectory under the OS config dir
// (~/.config/ads on Linux, ~/Library/Application Support/ads on macOS). It
// holds config.toml plus state/ (the confirm-token store and the audit log).
//
// It moved with the binary rename and there is no fallback to the old `goads`
// directory: anyone upgrading has to move it themselves (or re-run
// `ads login google`). A pre-1.0 break, chosen over carrying a lookup path
// forever.
const configDirName = "ads"

// defaultConfigFile is the file consulted when --config is not given.
const defaultConfigFile = "config.toml"

// resolveConfigPath returns the config file to read. An explicit path is used
// as-is. Otherwise the default path is returned only if it exists; a missing
// default file is not an error (env-only operation is supported).
func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", nil // no usable config dir → env only
	}
	p := filepath.Join(dir, defaultConfigFile)
	if _, err := os.Stat(p); err != nil {
		return "", nil
	}
	return p, nil
}

// stateDir is where the confirm-token store and audit log live. It is created
// on demand. Callers should treat a returned error as "persistence disabled".
func stateDir() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "state")
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

func userConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, configDirName), nil
}
