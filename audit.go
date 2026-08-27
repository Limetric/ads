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

// colorizeAuditEntry highlights the one field a reader scans an audit line for:
// whether the write landed. The timestamp is dimmed to keep it out of the way,
// and a line that doesn't have the expected shape is printed untouched — the
// log is append-only history, and an old or hand-edited line must still read.
func colorizeAuditEntry(st styles, entry string) string {
	fields := strings.Fields(entry)
	if len(fields) < 2 || !strings.Contains(entry, "applied=") {
		return entry
	}
	fields[0] = st.muted(fields[0])
	for i, f := range fields {
		switch f {
		case "applied=true":
			fields[i] = st.success(f)
		case "applied=false":
			fields[i] = st.failure(f)
		}
	}
	return strings.Join(fields, " ")
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
		st := newStyles(out)
		if len(entries) == 0 {
			fmt.Fprintln(out, st.muted("no audited writes yet (the audit log records every confirmed mutation)"))
			return nil
		}
		if auditLimit > 0 && len(entries) > auditLimit {
			entries = entries[len(entries)-auditLimit:]
		}
		for _, e := range entries {
			fmt.Fprintln(out, colorizeAuditEntry(st, e))
		}
		return nil
	},
}

func init() {
	auditCmd.Flags().IntVar(&auditLimit, "limit", 0, "show only the N most recent entries (0 = all)")
}
