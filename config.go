package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Configuration is platform-neutral here: this file knows how to read the
// shared TOML file and overlay the environment, but nothing about which ad
// networks exist or what credentials they need. Each platform owns its own
// credential struct, env vars, and validation — see config_google.go for the
// pattern a new platform copies.
//
// Resolution order (later wins): TOML file (if present) < environment
// variables. Credentials are most commonly supplied via the environment so the
// MCP host config and CI never need a file on disk.

// decodeConfigFile decodes the TOML config selected by path into dst and
// returns the file it read. An empty path means "use the default path if it
// exists"; when no file is found dst is left untouched and the returned path is
// empty, which is the env-only case.
//
// The path comes back because a deprecated key has to be retired from the exact
// file it was read out of, not from wherever the default resolves today.
func decodeConfigFile(path string, dst any) (string, error) {
	resolved, err := resolveConfigPath(path)
	if err != nil {
		return "", err
	}
	if resolved == "" {
		return "", nil
	}
	if _, err := toml.DecodeFile(resolved, dst); err != nil {
		return "", fmt.Errorf("read config %q: %w", resolved, err)
	}
	return resolved, nil
}

// overlayEnv copies each non-empty environment variable over the string it maps
// to, so environment values win over anything read from the config file.
func overlayEnv(vars map[string]*string) {
	for env, dst := range vars {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			*dst = v
		}
	}
}

// isLoopbackURL reports whether raw points at a local/offline host (httptest
// servers and the like), in which case a platform can relax auth and credential
// checks. Platforms call it from their own isTest.
//
// Only loopback hosts count. Any remote endpoint — a regional endpoint, proxy,
// future API version, even plain-HTTP — is a real target and keeps the user's
// real credentials: inferring test mode from any URL difference used to
// silently swap in fake credentials (issue #5).
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// normalizeBaseURL trims whitespace and any trailing slash, falling back to
// def when the result is empty.
func normalizeBaseURL(raw, def string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return def
	}
	return raw
}
