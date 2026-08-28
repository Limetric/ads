package main

import "testing"

func TestCreateAdGroup_DefaultsPausedWithHint(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := CreateAdGroupArgs{CustomerID: "123-456-7890", CampaignID: "111", Name: "AG1", CpcBidMicros: 2_000_000}
	prev, err := runCreateAdGroup(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.StatusAfterApply != "PAUSED" || prev.NextActionHint == nil {
		t.Errorf("expected PAUSED with hint, got %+v", prev)
	}
	if prev.NextActionHint.Params["entity_type"] != "ad_group" {
		t.Errorf("hint entity_type = %v", prev.NextActionHint.Params)
	}

	args.Confirm = prev.Token
	if _, err := runCreateAdGroup(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	create := opCreate(t, cap.firstOp(t), "adGroupOperation")
	if create["status"] != "PAUSED" || create["type"] != "SEARCH_STANDARD" {
		t.Errorf("unexpected create: %v", create)
	}
	if create["cpcBidMicros"] != "2000000" {
		t.Errorf("cpcBidMicros = %v", create["cpcBidMicros"])
	}
	if create["campaign"] != "customers/1234567890/campaigns/111" {
		t.Errorf("campaign = %v", create["campaign"])
	}
}

func TestCreateAdGroup_ExplicitEnabledNoHint(t *testing.T) {
	useTempState(t)
	prev, err := runCreateAdGroup(t.Context(), nil, CreateAdGroupArgs{CustomerID: "1", CampaignID: "1", Name: "AG", Status: "ENABLED"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if prev.StatusAfterApply != "ENABLED" || prev.NextActionHint != nil {
		t.Errorf("ENABLED should carry no hint, got %+v", prev)
	}
}

func TestCreateAdGroup_CanOmitTypeForAppCampaign(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := CreateAdGroupArgs{CustomerID: "1", CampaignID: "2", Name: "App creatives", OmitType: true}
	preview, err := runCreateAdGroup(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = preview.Token
	if _, err := runCreateAdGroup(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	create := opCreate(t, cap.firstOp(t), "adGroupOperation")
	if _, exists := create["type"]; exists {
		t.Fatalf("App ad group must omit type: %v", create)
	}
}

func TestCreateAdGroup_EmptyName(t *testing.T) {
	if _, err := runCreateAdGroup(t.Context(), nil, CreateAdGroupArgs{CustomerID: "1", CampaignID: "1"}); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestUpdateAdGroup_BuildsMask(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateAdGroupArgs{CustomerID: "1", AdGroupID: "9", Name: "New", CpcBidMicros: 500000, AdRotationMode: "ROTATE_FOREVER"}
	prev, err := runUpdateAdGroup(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateAdGroup(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op, _ := cap.firstOp(t)["adGroupOperation"].(map[string]any)
	if op["updateMask"] != "name,cpcBidMicros,adRotationMode" {
		t.Errorf("updateMask = %v", op["updateMask"])
	}
	upd, _ := op["update"].(map[string]any)
	if upd["adRotationMode"] != "ROTATE_FOREVER" {
		t.Errorf("adRotationMode = %v", upd["adRotationMode"])
	}
}

func TestUpdateAdGroup_RequiresAField(t *testing.T) {
	if _, err := runUpdateAdGroup(t.Context(), nil, UpdateAdGroupArgs{CustomerID: "1", AdGroupID: "9"}); err == nil {
		t.Fatal("expected error when no fields provided")
	}
}

func TestUpdateAdGroup_InvalidRotationMode(t *testing.T) {
	if _, err := runUpdateAdGroup(t.Context(), nil, UpdateAdGroupArgs{CustomerID: "1", AdGroupID: "9", AdRotationMode: "NOPE"}); err == nil {
		t.Fatal("expected error for invalid rotation mode")
	}
}

func TestUpdateAdGroup_SetsAdGroupLevelTargets(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	// An ad-group target overrides the campaign's for that ad group alone; it
	// could be read but never written before (issue #54).
	args := UpdateAdGroupArgs{CustomerID: "1", AdGroupID: "7", TargetCpaMicros: 12_500_000}
	prev, err := runUpdateAdGroup(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateAdGroup(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op, _ := cap.firstOp(t)["adGroupOperation"].(map[string]any)
	upd := opUpdate(t, cap.firstOp(t), "adGroupOperation")
	if upd["targetCpaMicros"] != "12500000" || op["updateMask"] != "targetCpaMicros" {
		t.Errorf("staged %v mask=%v", upd, op["updateMask"])
	}
}

func TestUpdateAdGroup_SetsTargetROAS(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := UpdateAdGroupArgs{CustomerID: "1", AdGroupID: "7", TargetROAS: 3.5}
	prev, err := runUpdateAdGroup(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateAdGroup(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	upd := opUpdate(t, cap.firstOp(t), "adGroupOperation")
	if upd["targetRoas"] != 3.5 {
		t.Errorf("targetRoas = %v", upd["targetRoas"])
	}
}

func TestUpdateAdGroup_ClearsInheritableValues(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	// Every leaf is an independent scalar on the ad group, so clearing more
	// than one at a time is coherent — unlike a campaign's bidding targets,
	// which are members of one oneof.
	args := UpdateAdGroupArgs{CustomerID: "1", AdGroupID: "7", ClearCpcBid: true, ClearTargetCPA: true, ClearTargetROAS: true}
	prev, err := runUpdateAdGroup(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	args.Confirm = prev.Token
	if _, err := runUpdateAdGroup(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	op, _ := cap.firstOp(t)["adGroupOperation"].(map[string]any)
	if op["updateMask"] != "cpcBidMicros,targetCpaMicros,targetRoas" {
		t.Errorf("updateMask = %v", op["updateMask"])
	}
	upd := opUpdate(t, cap.firstOp(t), "adGroupOperation")
	// Masked but absent is what Google reads as unset; a literal 0 would bid
	// zero rather than fall back to what the ad group inherits.
	for _, leaf := range []string{"cpcBidMicros", "targetCpaMicros", "targetRoas"} {
		if _, ok := upd[leaf]; ok {
			t.Errorf("clear must leave %s out of the update, got %v", leaf, upd)
		}
	}
}

func TestUpdateAdGroup_RejectsContradictoryArguments(t *testing.T) {
	useTempState(t)
	cases := map[string]UpdateAdGroupArgs{
		"bid and its clear":    {CustomerID: "1", AdGroupID: "7", CpcBidMicros: 100, ClearCpcBid: true},
		"tCPA and its clear":   {CustomerID: "1", AdGroupID: "7", TargetCpaMicros: 100, ClearTargetCPA: true},
		"tROAS and its clear":  {CustomerID: "1", AdGroupID: "7", TargetROAS: 3, ClearTargetROAS: true},
		"both targets at once": {CustomerID: "1", AdGroupID: "7", TargetCpaMicros: 100, TargetROAS: 3},
		"negative tCPA":        {CustomerID: "1", AdGroupID: "7", TargetCpaMicros: -1},
		"negative tROAS":       {CustomerID: "1", AdGroupID: "7", TargetROAS: -1},
		"nothing at all":       {CustomerID: "1", AdGroupID: "7"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			// A nil client would panic on any request, so reaching the API at
			// all fails the test.
			if _, err := runUpdateAdGroup(t.Context(), nil, args); err == nil {
				t.Error("expected the arguments to be rejected")
			}
		})
	}
}
