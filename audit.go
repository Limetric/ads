package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// `ads audit` surfaces the audit log that safety.go appends to on every
// confirmed write (success or failure): what ads changed, when, on which
// customer, with which token. It closes the safety loop — the log existed but
// nothing displayed it (issue #17).

// readAuditLog returns the audit log entries, oldest first. A missing log is
// not an error: it just means no write has been confirmed yet.
func readAuditLog() ([]string, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, fmt.Errorf("audit log unavailable (%v) — set HOME/XDG_CONFIG_HOME", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.log"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// --- CLI front-end ---

var auditLimit int

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Show the log of writes ads has applied (and failed applies)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		entries, err := readAuditLog()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(entries) == 0 {
			fmt.Fprintln(out, "no audited writes yet (the audit log records every confirmed mutation)")
			return nil
		}
		if auditLimit > 0 && len(entries) > auditLimit {
			entries = entries[len(entries)-auditLimit:]
		}
		for _, e := range entries {
			fmt.Fprintln(out, e)
		}
		return nil
	},
}

func init() {
	auditCmd.Flags().IntVar(&auditLimit, "limit", 0, "show only the N most recent entries (0 = all)")
}
