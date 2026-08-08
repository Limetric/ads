package main

import (
	"strings"
	"testing"
)

// stagedBudgetOps is a minimal valid mutate operation list for staging in tests.
func stagedBudgetOps() []any {
	return []any{map[string]any{"campaignBudgetOperation": map[string]any{
		"update":     map[string]any{"resourceName": "customers/1234567890/campaignBudgets/555", "amountMicros": "5000000"},
		"updateMask": "amountMicros",
	}}}
}

func TestRunConfirm_AppliesStagedMutation(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()

	p, err := stageMutation("set_campaign_budget", "1234567890", "Set budget 555 to 5000000 micros", stagedBudgetOps())
	if err != nil {
		t.Fatalf("stageMutation: %v", err)
	}

	res, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token)
	if err != nil {
		t.Fatalf("runConfirm: %v", err)
	}
	if !res.Applied {
		t.Errorf("expected Applied=true, got %+v", res)
	}
	if res.Tool != "set_campaign_budget" {
		t.Errorf("Tool = %q, want set_campaign_budget", res.Tool)
	}
	if cap.calls != 1 {
		t.Errorf("mutate calls = %d, want 1", cap.calls)
	}

	// The token is single-use.
	if _, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token); err == nil {
		t.Error("re-confirming a consumed token should fail")
	}

	// The apply landed in the audit log.
	entries, err := readAuditLog()
	if err != nil {
		t.Fatalf("readAuditLog: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0], "tool=set_campaign_budget") || !strings.Contains(entries[0], "applied=true") {
		t.Errorf("unexpected audit entries: %v", entries)
	}
}

func TestRunConfirm_UnknownToken(t *testing.T) {
	useTempState(t)
	srv, _ := mutateServer(t)
	defer srv.Close()
	if _, err := runConfirm(t.Context(), newTestClient(t, srv), "deadbeefdeadbeef"); err == nil {
		t.Fatal("unknown token should fail")
	}
	if _, err := runConfirm(t.Context(), newTestClient(t, srv), "not-a-token"); err == nil {
		t.Fatal("malformed token should fail")
	}
}

func TestRunConfirm_DoubleConfirmFlow(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()

	p, err := stageMutationDouble("set_campaign_budget", "1234567890", "Raise budget a lot", stagedBudgetOps())
	if err != nil {
		t.Fatalf("stageMutationDouble: %v", err)
	}

	// First confirm re-stages instead of applying.
	first, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token)
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	if first.Applied || first.Token == "" || first.Token == p.Token {
		t.Fatalf("first confirm should re-stage under a fresh token, got %+v", first)
	}
	if cap.calls != 0 {
		t.Fatalf("nothing may be applied on the first confirm, got %d mutate calls", cap.calls)
	}

	// Second confirm applies.
	second, err := runConfirm(t.Context(), newTestClient(t, srv), first.Token)
	if err != nil {
		t.Fatalf("second confirm: %v", err)
	}
	if !second.Applied || cap.calls != 1 {
		t.Errorf("second confirm should apply once, got %+v (calls=%d)", second, cap.calls)
	}
}

func TestRunConfirm_FailedApplyIsAudited(t *testing.T) {
	useTempState(t)
	srv := errServer(t)
	defer srv.Close()

	p, err := stageMutation("set_campaign_budget", "1234567890", "Set budget", stagedBudgetOps())
	if err != nil {
		t.Fatalf("stageMutation: %v", err)
	}
	if _, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token); err == nil {
		t.Fatal("expected the apply to fail against the error server")
	}
	entries, err := readAuditLog()
	if err != nil {
		t.Fatalf("readAuditLog: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0], "applied=false") {
		t.Errorf("failed apply should be audited with applied=false: %v", entries)
	}
}

func TestRunConfirm_BlockedOperation(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()

	p, err := stageMutation("set_campaign_budget", "1234567890", "Set budget", stagedBudgetOps())
	if err != nil {
		t.Fatalf("stageMutation: %v", err)
	}
	t.Setenv("GOOGLE_ADS_BLOCKED_OPS", "set_campaign_budget")

	if _, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token); err == nil {
		t.Fatal("confirming a blocked operation should fail")
	}
	if cap.calls != 0 {
		t.Errorf("blocked operation must not reach the API, got %d mutate calls", cap.calls)
	}

	// The blocked confirm must not burn the token: once the block is lifted,
	// the same token still applies (parity with the per-tool confirm path).
	t.Setenv("GOOGLE_ADS_BLOCKED_OPS", "")
	res, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token)
	if err != nil {
		t.Fatalf("confirm after unblocking: %v", err)
	}
	if !res.Applied || cap.calls != 1 {
		t.Errorf("expected the surviving token to apply once, got %+v (calls=%d)", res, cap.calls)
	}
}

func TestRunConfirm_ReenforcesBudgetCap(t *testing.T) {
	// Tightening GOOGLE_ADS_MAX_DAILY_BUDGET between preview and confirm must
	// block `ads confirm` just like re-running the original command would,
	// and the rejected token must survive for when the cap is raised again.
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()

	p, err := stageMutation("set_campaign_budget", "1234567890", "Set budget", stagedBudgetOps()) // 5.00/day staged
	if err != nil {
		t.Fatalf("stageMutation: %v", err)
	}
	t.Setenv("GOOGLE_ADS_MAX_DAILY_BUDGET", "2") // tightened below the staged 5.00

	if _, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token); err == nil {
		t.Fatal("confirming a staged budget above the tightened cap should fail")
	}
	if cap.calls != 0 {
		t.Errorf("over-cap budget must not reach the API, got %d mutate calls", cap.calls)
	}

	t.Setenv("GOOGLE_ADS_MAX_DAILY_BUDGET", "10")
	res, err := runConfirm(t.Context(), newTestClient(t, srv), p.Token)
	if err != nil {
		t.Fatalf("confirm after raising the cap: %v", err)
	}
	if !res.Applied || cap.calls != 1 {
		t.Errorf("expected the surviving token to apply once, got %+v (calls=%d)", res, cap.calls)
	}
}

func TestCLI_ConfirmCommand(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	srv, _ := mutateServer(t)
	defer srv.Close()
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL)

	p, err := stageMutation("set_campaign_budget", "1234567890", "Set budget 555", stagedBudgetOps())
	if err != nil {
		t.Fatalf("stageMutation: %v", err)
	}

	out, err := runCLI(t, "confirm", p.Token)
	if err != nil {
		t.Fatalf("ads confirm: %v\noutput: %s", err, out)
	}
	for _, want := range []string{`"applied": true`, `"tool": "set_campaign_budget"`} {
		if !strings.Contains(out, want) {
			t.Errorf("confirm output missing %q:\n%s", want, out)
		}
	}

	if out, err := runCLI(t, "confirm"); err == nil {
		t.Fatalf("confirm without a token should fail; output: %s", out)
	}
}
