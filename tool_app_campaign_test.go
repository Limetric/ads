package main

import (
	"strings"
	"testing"
)

func validAppCampaignArgs() CreateAppCampaignArgs {
	return CreateAppCampaignArgs{
		CustomerID:   "123-456-7890",
		CampaignName: "CloudMount Android",
		DailyBudget:  5,
		AppID:        "app.cloudmount",
		TargetCPI:    0.5,
		Headlines:    []string{"Cloud Storage in Android", "Mount Clouds in Files"},
		Descriptions: []string{"Browse cloud storage directly in Android Files."},
		GeoTargetIDs: []string{"2840"},
		LanguageIDs:  []string{"1000"},
	}
}

func TestCreateAppCampaign_PreviewThenApply(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := validAppCampaignArgs()
	preview, err := runCreateAppCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Applied || preview.Token == "" || preview.StatusAfterApply != "PAUSED" || preview.NextActionHint == nil {
		t.Fatalf("bad preview: %+v", preview)
	}

	args.Confirm = preview.Token
	if _, err := runCreateAppCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ops := cap.lastOps()
	if len(ops) != 6 {
		t.Fatalf("expected 6 ops, got %d", len(ops))
	}

	budget := opCreate(t, ops[0].(map[string]any), "campaignBudgetOperation")
	if budget["amountMicros"] != "5000000" || budget["explicitlyShared"] != false {
		t.Errorf("budget op wrong: %v", budget)
	}
	campaign := opCreate(t, ops[1].(map[string]any), "campaignOperation")
	if campaign["advertisingChannelType"] != "MULTI_CHANNEL" || campaign["advertisingChannelSubType"] != "APP_CAMPAIGN" {
		t.Errorf("campaign type wrong: %v", campaign)
	}
	if _, present := campaign["networkSettings"]; present {
		t.Errorf("App campaign must not set networkSettings: %v", campaign)
	}
	targetCPA := campaign["targetCpa"].(map[string]any)
	if targetCPA["targetCpaMicros"] != "500000" {
		t.Errorf("target CPA wrong: %v", targetCPA)
	}
	settings := campaign["appCampaignSetting"].(map[string]any)
	if settings["appId"] != "app.cloudmount" || settings["appStore"] != "GOOGLE_APP_STORE" || settings["biddingStrategyGoalType"] != "OPTIMIZE_INSTALLS_TARGET_INSTALL_COST" {
		t.Errorf("app settings wrong: %v", settings)
	}
	if campaign["containsEuPoliticalAdvertising"] != "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING" {
		t.Errorf("missing EU political advertising declaration: %v", campaign)
	}
	geo := opCreate(t, ops[2].(map[string]any), "campaignCriterionOperation")
	location := geo["location"].(map[string]any)
	if location["geoTargetConstant"] != "geoTargetConstants/2840" {
		t.Errorf("geo targeting wrong: %v", geo)
	}
	language := opCreate(t, ops[3].(map[string]any), "campaignCriterionOperation")
	languageSetting := language["language"].(map[string]any)
	if languageSetting["languageConstant"] != "languageConstants/1000" {
		t.Errorf("language targeting wrong: %v", language)
	}

	adGroup := opCreate(t, ops[4].(map[string]any), "adGroupOperation")
	if _, present := adGroup["type"]; present {
		t.Errorf("App campaign ad group must omit type: %v", adGroup)
	}
	if adGroup["name"] != "Ad group 1" || adGroup["status"] != "ENABLED" {
		t.Errorf("App campaign ad group should be ready behind paused campaign: %v", adGroup)
	}
	adCreate := opCreate(t, ops[5].(map[string]any), "adGroupAdOperation")
	if adCreate["adGroup"] != "customers/1234567890/adGroups/-3" || adCreate["status"] != "ENABLED" {
		t.Errorf("app ad create wrong: %v", adCreate)
	}
	appAd := adCreate["ad"].(map[string]any)["appAd"].(map[string]any)
	if len(appAd["headlines"].([]any)) != 2 || len(appAd["descriptions"].([]any)) != 1 {
		t.Errorf("app ad copy wrong: %v", appAd)
	}
}

func TestCreateAppCampaign_StartEnabled(t *testing.T) {
	useTempState(t)
	srv, cap := mutateServer(t)
	defer srv.Close()
	c := newTestClient(t, srv)

	args := validAppCampaignArgs()
	args.Status = "ENABLED"
	preview, err := runCreateAppCampaign(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.StatusAfterApply != "ENABLED" || preview.NextActionHint != nil {
		t.Fatalf("enabled campaign should carry no enable hint: %+v", preview)
	}
	args.Confirm = preview.Token
	if _, err := runCreateAppCampaign(t.Context(), c, args); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ops := cap.lastOps()
	for index, operationKey := range map[int]string{1: "campaignOperation", 4: "adGroupOperation", 5: "adGroupAdOperation"} {
		create := opCreate(t, ops[index].(map[string]any), operationKey)
		if create["status"] != "ENABLED" {
			t.Errorf("%s status = %v, want ENABLED", operationKey, create["status"])
		}
	}
}

func TestCreateAppCampaign_Validation(t *testing.T) {
	mutate := func(change func(*CreateAppCampaignArgs)) CreateAppCampaignArgs {
		args := validAppCampaignArgs()
		change(&args)
		return args
	}
	tests := map[string]CreateAppCampaignArgs{
		"missing campaign name": mutate(func(args *CreateAppCampaignArgs) { args.CampaignName = "" }),
		"nonpositive budget":    mutate(func(args *CreateAppCampaignArgs) { args.DailyBudget = 0 }),
		"budget over cap":       mutate(func(args *CreateAppCampaignArgs) { args.DailyBudget = 100 }),
		"missing app id":        mutate(func(args *CreateAppCampaignArgs) { args.AppID = "" }),
		"nonpositive target cpi": mutate(func(args *CreateAppCampaignArgs) {
			args.TargetCPI = 0
		}),
		"missing headlines":    mutate(func(args *CreateAppCampaignArgs) { args.Headlines = nil }),
		"empty headline":       mutate(func(args *CreateAppCampaignArgs) { args.Headlines = []string{" "} }),
		"too many headlines":   mutate(func(args *CreateAppCampaignArgs) { args.Headlines = headlines(6) }),
		"missing descriptions": mutate(func(args *CreateAppCampaignArgs) { args.Descriptions = nil }),
		"empty description":    mutate(func(args *CreateAppCampaignArgs) { args.Descriptions = []string{" "} }),
		"removed status":       mutate(func(args *CreateAppCampaignArgs) { args.Status = "REMOVED" }),
		"empty geo target":     mutate(func(args *CreateAppCampaignArgs) { args.GeoTargetIDs = []string{""} }),
		"empty language":       mutate(func(args *CreateAppCampaignArgs) { args.LanguageIDs = []string{""} }),
		"long description": mutate(func(args *CreateAppCampaignArgs) {
			args.Descriptions = []string{strings.Repeat("x", 91)}
		}),
		"empty asset": mutate(func(args *CreateAppCampaignArgs) {
			args.ImageAssets = []string{""}
		}),
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := runCreateAppCampaign(t.Context(), nil, args); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestCreateAppCampaign_Blocked(t *testing.T) {
	t.Setenv("GOOGLE_ADS_BLOCKED_OPS", "create_app_campaign")
	if _, err := runCreateAppCampaign(t.Context(), nil, validAppCampaignArgs()); err == nil {
		t.Fatal("expected blocked-operation error")
	}
}

func TestCreateAppCampaign_EUPoliticalAds(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"default", "", "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING"},
		{"does not contain", "does-not-contain", "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING"},
		{"contains", "contains", "CONTAINS_EU_POLITICAL_ADVERTISING"},
		{"invalid", "maybe", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			useTempState(t)
			srv, cap := mutateServer(t)
			defer srv.Close()
			c := newTestClient(t, srv)
			args := validAppCampaignArgs()
			args.EUPoliticalAds = tt.input
			prev, err := runCreateAppCampaign(t.Context(), c, args)
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), "eu_political_ads") {
					t.Fatalf("expected declaration validation error, got %v", err)
				}
				if len(cap.lastOps()) != 0 || prev.Token != "" {
					t.Fatal("invalid declaration must not stage or apply a mutation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(prev.Preview, tt.want) {
				t.Errorf("preview does not disclose declaration %s: %s", tt.want, prev.Preview)
			}
			if len(cap.lastOps()) != 0 {
				t.Fatal("preview mutated the campaign")
			}
			args.Confirm = prev.Token
			if _, err := runCreateAppCampaign(t.Context(), c, args); err != nil {
				t.Fatal(err)
			}
			ops := cap.lastOps()
			if len(ops) < 2 {
				t.Fatalf("missing campaign operation: %v", ops)
			}
			campaign := opCreate(t, ops[1].(map[string]any), "campaignOperation")
			if campaign["containsEuPoliticalAdvertising"] != tt.want {
				t.Errorf("declaration = %v; want %s", campaign["containsEuPoliticalAdvertising"], tt.want)
			}
		})
	}
}
