package main

import (
	"encoding/json"
	"testing"

	"github.com/spf13/pflag"
)

// resetPauseCmd clears the pause command's shared flag state so CLI tests
// don't leak values (or cobra's required-flag "Changed" marks) into each other.
func resetPauseCmd(t *testing.T) {
	t.Cleanup(func() {
		pauseArgs = EntityActionArgs{}
		pauseCmd.Flags().VisitAll(func(f *pflag.Flag) {
			f.Changed = false
			_ = f.Value.Set(f.DefValue)
		})
	})
}

func TestEntityResourceAndOp(t *testing.T) {
	cases := []struct {
		typ, id, wantRes, wantOp string
	}{
		{"campaign", "5", "customers/1/campaigns/5", "campaignOperation"},
		{"ad_group", "5", "customers/1/adGroups/5", "adGroupOperation"},
		{"ad", "10~20", "customers/1/adGroupAds/10~20", "adGroupAdOperation"},
		{"keyword", "10~20", "customers/1/adGroupCriteria/10~20", "adGroupCriterionOperation"},
	}
	for _, tc := range cases {
		res, op, err := entityResourceAndOp("1", tc.typ, tc.id)
		if err != nil || res != tc.wantRes || op != tc.wantOp {
			t.Errorf("%s -> (%q,%q,%v), want (%q,%q)", tc.typ, res, op, err, tc.wantRes, tc.wantOp)
		}
	}
	if _, _, err := entityResourceAndOp("1", "bogus", "5"); err == nil {
		t.Error("expected error for invalid entity type")
	}
	// Ads and keywords need the composite adGroupId~entityId; a bare ID used
	// to preview fine and fail only at confirm time (issue #14).
	for _, typ := range []string{"ad", "keyword"} {
		if _, _, err := entityResourceAndOp("1", typ, "5"); err == nil {
			t.Errorf("bare ID for %s should be rejected", typ)
		}
		if _, _, err := entityResourceAndOp("1", typ, "10~x"); err == nil {
			t.Errorf("non-numeric composite for %s should be rejected", typ)
		}
	}
	// Campaigns and ad groups take plain numeric IDs only.
	if _, _, err := entityResourceAndOp("1", "campaign", "5 OR 1=1"); err == nil {
		t.Error("non-numeric campaign ID should be rejected")
	}
}

func TestPauseEntity_PreviewThenApply(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := EntityActionArgs{CustomerID: "123-456-7890", EntityType: "campaign", EntityID: "555"}
	prev, err := runPauseEntity(t.Context(), c, args)
	if err != nil || prev.Token == "" || cap.calls != 0 {
		t.Fatalf("preview: %+v err=%v calls=%d", prev, err, cap.calls)
	}
	args.Confirm = prev.Token
	if _, err := runPauseEntity(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	upd := opUpdate(t, cap.firstOp(t), "campaignOperation")
	if upd["status"] != "PAUSED" || upd["resourceName"] != "customers/1234567890/campaigns/555" {
		t.Errorf("unexpected update op: %v", upd)
	}
}

func TestEnableEntity_SetsEnabled(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := EntityActionArgs{CustomerID: "1", EntityType: "ad_group", EntityID: "9"}
	prev, _ := runEnableEntity(t.Context(), c, args)
	args.Confirm = prev.Token
	if _, err := runEnableEntity(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opUpdate(t, cap.firstOp(t), "adGroupOperation")["status"] != "ENABLED" {
		t.Errorf("expected ENABLED status")
	}
}

func TestRemoveEntity_UsesRemoveOp(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := EntityActionArgs{CustomerID: "1", EntityType: "ad", EntityID: "10~20"}
	prev, err := runRemoveEntity(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// Destructive ops take two confirmations (issue #12): the first confirm
	// re-stages under a fresh token instead of applying.
	args.Confirm = prev.Token
	second, err := runRemoveEntity(t.Context(), c, args)
	if err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	if second.Applied || second.Token == "" || second.Token == prev.Token {
		t.Fatalf("first confirm should re-stage with a fresh token: %+v", second)
	}
	if cap.calls != 0 {
		t.Fatalf("first confirm of a destructive op must not mutate (calls=%d)", cap.calls)
	}
	args.Confirm = second.Token
	if _, err := runRemoveEntity(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op := cap.firstOp(t)
	inner, _ := op["adGroupAdOperation"].(map[string]any)
	if inner["remove"] != "customers/1/adGroupAds/10~20" {
		t.Errorf("expected remove op, got %v", op)
	}
}

func TestEntityActions_Blocked(t *testing.T) {
	t.Setenv("GOOGLE_ADS_BLOCKED_OPS", "remove_entity")
	if _, err := runRemoveEntity(t.Context(), nil, EntityActionArgs{CustomerID: "1", EntityType: "campaign", EntityID: "5"}); err == nil {
		t.Fatal("expected blocked-operation error")
	}
}

func TestEntityActions_InvalidType(t *testing.T) {
	if _, err := runPauseEntity(t.Context(), nil, EntityActionArgs{CustomerID: "1", EntityType: "nope", EntityID: "5"}); err == nil {
		t.Fatal("expected error for invalid entity type")
	}
}

func TestCLI_PauseCommand_PreviewThenApply(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	t.Setenv("GOOGLE_ADS_API_BASE_URL", srv.URL) // non-prod → skips OAuth/creds
	resetPauseCmd(t)

	out, err := runCLI(t, "google", "pause", "--customer-id", "1", "--type", "campaign", "--id", "42")
	if err != nil {
		t.Fatalf("execute pause preview: %v\noutput: %s", err, out)
	}
	var prev WriteResult
	if err := json.Unmarshal([]byte(out), &prev); err != nil {
		t.Fatalf("pause output is not JSON: %v\noutput: %s", err, out)
	}
	if prev.Applied || prev.Token == "" || cap.calls != 0 {
		t.Fatalf("preview should stage without mutating: %+v calls=%d", prev, cap.calls)
	}

	out, err = runCLI(t, "google", "pause", "--customer-id", "1", "--type", "campaign", "--id", "42", "--confirm", prev.Token)
	if err != nil {
		t.Fatalf("execute pause apply: %v\noutput: %s", err, out)
	}
	if cap.calls != 1 {
		t.Fatalf("apply should mutate exactly once, got %d calls", cap.calls)
	}
	upd := opUpdate(t, cap.firstOp(t), "campaignOperation")
	if upd["status"] != "PAUSED" || upd["resourceName"] != "customers/1/campaigns/42" {
		t.Errorf("unexpected update op: %v", upd)
	}
}

func TestCLI_PauseRequiresFlags(t *testing.T) {
	resetPauseCmd(t)
	if out, err := runCLI(t, "google", "pause", "--customer-id", "1"); err == nil {
		t.Fatalf("pause without --type/--id should fail; output: %s", out)
	}
}
