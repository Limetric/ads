package main

import (
	"strings"
	"testing"
	"time"
)

func TestCLI_Audit(t *testing.T) {
	useTempState(t)

	t.Run("empty log", func(t *testing.T) {
		out, err := runCLI(t, "audit")
		if err != nil {
			t.Fatalf("audit: %v\noutput: %s", err, out)
		}
		if !strings.Contains(out, "no audited writes yet") {
			t.Errorf("unexpected empty-log output: %q", out)
		}
	})

	// Append three entries the way the confirm flow does.
	for i, tool := range []string{"set_campaign_budget", "pause_entity", "remove_entity"} {
		auditLog(&PendingMutation{
			Token:      strings.Repeat("a", 15) + string(rune('0'+i)),
			Tool:       tool,
			CustomerID: "1234567890",
			Operations: []any{map[string]any{}},
			CreatedAt:  time.Now().UTC(),
		}, true)
	}

	t.Run("prints entries", func(t *testing.T) {
		out, err := runCLI(t, "audit")
		if err != nil {
			t.Fatalf("audit: %v\noutput: %s", err, out)
		}
		// The platform is on every line: two networks both have a
		// set_campaign_budget, and an account ID does not say which is which.
		for _, want := range []string{"platform=google", "tool=set_campaign_budget", "tool=pause_entity", "tool=remove_entity", "account=1234567890", "applied=true"} {
			if !strings.Contains(out, want) {
				t.Errorf("audit output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("limit keeps the most recent", func(t *testing.T) {
		out, err := runCLI(t, "audit", "--limit", "1")
		if err != nil {
			t.Fatalf("audit --limit 1: %v\noutput: %s", err, out)
		}
		if strings.Contains(out, "tool=set_campaign_budget") || !strings.Contains(out, "tool=remove_entity") {
			t.Errorf("audit --limit 1 should show only the last entry:\n%s", out)
		}
	})
}
