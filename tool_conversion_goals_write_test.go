package main

import (
	"strings"
	"testing"
)

var accountGoalsRoute = conversionRoute{"FROM customer_conversion_goal", `[
	{"customerConversionGoal":{"category":"PURCHASE","origin":"WEBSITE","biddable":true}},
	{"customerConversionGoal":{"category":"PAGE_VIEW","origin":"WEBSITE","biddable":false}}]`}

func campaignConfigRoute(level, customGoal string) conversionRoute {
	return conversionRoute{"FROM conversion_goal_campaign_config WHERE campaign.id = 7",
		`[{"campaign":{"name":"Brand"},"conversionGoalCampaignConfig":{"goalConfigLevel":"` + level + `","customConversionGoal":"` + customGoal + `"}}]`}
}

var campaignGoalsRoute = conversionRoute{"FROM campaign_conversion_goal WHERE campaign.id = 7", `[
	{"campaignConversionGoal":{"category":"PURCHASE","origin":"WEBSITE","biddable":true}},
	{"campaignConversionGoal":{"category":"SIGNUP","origin":"APP","biddable":false}}]`}

var customGoalRoute = conversionRoute{"FROM custom_conversion_goal WHERE custom_conversion_goal.id = 5",
	`[{"customConversionGoal":{"resourceName":"customers/999/customConversionGoals/5","name":"Leads","status":"ENABLED","conversionActions":["customers/1/conversionActions/42"]}}]`}

// goalOp digs the named operation out of the i-th captured mutate operation.
func goalOp(t *testing.T, capture *conversionCapture, i int, key string) map[string]any {
	t.Helper()
	ops := capture.ops(t)
	if i >= len(ops) {
		t.Fatalf("only %d operations captured", len(ops))
	}
	op, ok := ops[i][key].(map[string]any)
	if !ok {
		t.Fatalf("operation %d is not a %s: %v", i, key, ops[i])
	}
	return op
}

func TestParseGoalChanges(t *testing.T) {
	changes, err := parseGoalChanges([]string{" purchase:website "}, []string{"PAGE_VIEW:WEBSITE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0] != (goalChange{goalKey{"PURCHASE", "WEBSITE"}, true}) || changes[1] != (goalChange{goalKey{"PAGE_VIEW", "WEBSITE"}, false}) {
		t.Errorf("changes = %+v", changes)
	}
	for name, tc := range map[string]struct {
		biddable, notBiddable []string
		want                  string
	}{
		"empty":            {want: "no changes specified"},
		"missing origin":   {biddable: []string{"PURCHASE"}, want: "must be CATEGORY:ORIGIN"},
		"unknown category": {biddable: []string{"SHOPPING:WEBSITE"}, want: "unknown category"},
		"unknown origin":   {notBiddable: []string{"PURCHASE:EMAIL"}, want: "unknown origin"},
		"named twice":      {biddable: []string{"PURCHASE:WEBSITE"}, notBiddable: []string{"purchase:website"}, want: "more than once"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGoalChanges(tc.biddable, tc.notBiddable); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestUpdateAccountConversionGoals_StagesEveryGoalWithTwoConfirmations(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, selfTracked, accountGoalsRoute)
	c := newTestClient(t, srv)
	preview, err := runUpdateAccountConversionGoals(t.Context(), c, UpdateAccountConversionGoalsArgs{
		CustomerID: "1", Biddable: []string{"PAGE_VIEW:WEBSITE"}, NotBiddable: []string{"PURCHASE:WEBSITE"},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(preview.Preview, "PAGE_VIEW:WEBSITE not biddable → biddable") {
		t.Errorf("preview should show the transition: %s", preview.Preview)
	}
	rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
		return runUpdateAccountConversionGoals(t.Context(), c, UpdateAccountConversionGoalsArgs{Confirm: token})
	})
	if rounds != 2 {
		t.Errorf("took %d confirmations, want 2", rounds)
	}
	for i, want := range []struct {
		resource string
		biddable bool
	}{
		{"customers/1/customerConversionGoals/PAGE_VIEW~WEBSITE", true},
		{"customers/1/customerConversionGoals/PURCHASE~WEBSITE", false},
	} {
		op := goalOp(t, capture, i, "customerConversionGoalOperation")
		update, _ := op["update"].(map[string]any)
		if update["resourceName"] != want.resource || update["biddable"] != want.biddable || op["updateMask"] != "biddable" {
			t.Errorf("op %d = %v, want %s biddable=%t", i, op, want.resource, want.biddable)
		}
	}
}

func TestUpdateAccountConversionGoals_Refusals(t *testing.T) {
	cases := map[string]struct {
		routes []conversionRoute
		args   UpdateAccountConversionGoalsArgs
		want   string
	}{
		"cross-account tracking": {[]conversionRoute{managerTracked}, UpdateAccountConversionGoalsArgs{Biddable: []string{"PURCHASE:WEBSITE"}}, "re-run with customer_id 999"},
		"goal does not exist":    {[]conversionRoute{selfTracked, accountGoalsRoute}, UpdateAccountConversionGoalsArgs{Biddable: []string{"SIGNUP:APP"}}, "has no SIGNUP:APP goal"},
		"nothing changes":        {[]conversionRoute{selfTracked, accountGoalsRoute}, UpdateAccountConversionGoalsArgs{Biddable: []string{"PURCHASE:WEBSITE"}}, "nothing to change"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, tc.routes...)
			tc.args.CustomerID = "1"
			if _, err := runUpdateAccountConversionGoals(t.Context(), newTestClient(t, srv), tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if capture.mutates != 0 {
				t.Error("nothing may be written")
			}
		})
	}
}

func TestUpdateCampaignConversionGoals(t *testing.T) {
	for name, tc := range map[string]struct {
		level       string
		wantWarning bool
	}{
		"campaign following account goals": {"CUSTOMER", true},
		"campaign with its own goals":      {"CAMPAIGN", false},
	} {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, campaignConfigRoute(tc.level, ""), campaignGoalsRoute)
			c := newTestClient(t, srv)
			preview, err := runUpdateCampaignConversionGoals(t.Context(), c, UpdateCampaignConversionGoalsArgs{
				CustomerID: "1", CampaignID: "7", Biddable: []string{"SIGNUP:APP"},
			})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if got := strings.Contains(preview.Preview, "stop applying"); got != tc.wantWarning {
				t.Errorf("switch warning present = %t, want %t: %s", got, tc.wantWarning, preview.Preview)
			}
			rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
				return runUpdateCampaignConversionGoals(t.Context(), c, UpdateCampaignConversionGoalsArgs{Confirm: token})
			})
			if rounds != 1 {
				t.Errorf("took %d confirmations, want 1", rounds)
			}
			update, _ := goalOp(t, capture, 0, "campaignConversionGoalOperation")["update"].(map[string]any)
			if update["resourceName"] != "customers/1/campaignConversionGoals/7~SIGNUP~APP" || update["biddable"] != true {
				t.Errorf("update = %v", update)
			}
		})
	}
}

func TestUpdateCampaignConversionGoals_Refusals(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, conversionRoute{"FROM conversion_goal_campaign_config", `[]`})
	c := newTestClient(t, srv)
	if _, err := runUpdateCampaignConversionGoals(t.Context(), c, UpdateCampaignConversionGoalsArgs{CustomerID: "1", CampaignID: "7", Biddable: []string{"PURCHASE:WEBSITE"}}); err == nil || !strings.Contains(err.Error(), "campaign 7 was not found") {
		t.Errorf("missing campaign err = %v", err)
	}
	if _, err := runUpdateCampaignConversionGoals(t.Context(), nil, UpdateCampaignConversionGoalsArgs{CampaignID: "7 OR 1=1", Biddable: []string{"PURCHASE:WEBSITE"}}); err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Errorf("non-numeric campaign err = %v", err)
	}
	if capture.mutates != 0 {
		t.Error("nothing may be written")
	}
}

func TestUpdateCampaignGoalConfig(t *testing.T) {
	cases := map[string]struct {
		args       UpdateCampaignGoalConfigArgs
		routes     []conversionRoute
		wantRounds int
		wantMask   string
		wantUpdate map[string]any
		wantAbsent string
	}{
		"attach custom goal from the tracking manager": {
			args:       UpdateCampaignGoalConfigArgs{CustomGoalID: "5"},
			routes:     []conversionRoute{campaignConfigRoute("CAMPAIGN", ""), customGoalRoute},
			wantRounds: 1, wantMask: "customConversionGoal",
			wantUpdate: map[string]any{"customConversionGoal": "customers/999/customConversionGoals/5"},
		},
		"return to account goals": {
			args:       UpdateCampaignGoalConfigArgs{GoalConfigLevel: "customer"},
			routes:     []conversionRoute{campaignConfigRoute("CAMPAIGN", "")},
			wantRounds: 2, wantMask: "goalConfigLevel",
			wantUpdate: map[string]any{"goalConfigLevel": "CUSTOMER"},
		},
		"already on account goals": {
			args:       UpdateCampaignGoalConfigArgs{GoalConfigLevel: "CUSTOMER"},
			routes:     []conversionRoute{campaignConfigRoute("CUSTOMER", "")},
			wantRounds: 1, wantMask: "goalConfigLevel",
			wantUpdate: map[string]any{"goalConfigLevel": "CUSTOMER"},
		},
		"clear custom goal": {
			args:       UpdateCampaignGoalConfigArgs{ClearCustomGoal: true},
			routes:     []conversionRoute{campaignConfigRoute("CAMPAIGN", "customers/1/customConversionGoals/5")},
			wantRounds: 1, wantMask: "customConversionGoal",
			wantUpdate: map[string]any{}, wantAbsent: "customConversionGoal",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, tc.routes...)
			c := newTestClient(t, srv)
			tc.args.CustomerID, tc.args.CampaignID = "1", "7"
			preview, err := runUpdateCampaignGoalConfig(t.Context(), c, tc.args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
				return runUpdateCampaignGoalConfig(t.Context(), c, UpdateCampaignGoalConfigArgs{Confirm: token})
			})
			if rounds != tc.wantRounds {
				t.Errorf("took %d confirmations, want %d", rounds, tc.wantRounds)
			}
			op := goalOp(t, capture, 0, "conversionGoalCampaignConfigOperation")
			update, _ := op["update"].(map[string]any)
			if op["updateMask"] != tc.wantMask || update["resourceName"] != "customers/1/conversionGoalCampaignConfigs/7" {
				t.Errorf("op = %v", op)
			}
			for k, v := range tc.wantUpdate {
				if update[k] != v {
					t.Errorf("update[%s] = %v, want %v", k, update[k], v)
				}
			}
			if _, present := update[tc.wantAbsent]; tc.wantAbsent != "" && present {
				t.Errorf("%s must be absent so the mask clears it: %v", tc.wantAbsent, update)
			}
		})
	}
}

func TestUpdateCampaignGoalConfig_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		args UpdateCampaignGoalConfigArgs
		want string
	}{
		"nothing":              {UpdateCampaignGoalConfigArgs{}, "no changes specified"},
		"unknown level":        {UpdateCampaignGoalConfigArgs{GoalConfigLevel: "AD_GROUP"}, "unsupported goal_config_level"},
		"attach and clear":     {UpdateCampaignGoalConfigArgs{CustomGoalID: "5", ClearCustomGoal: true}, "contradict"},
		"customer with custom": {UpdateCampaignGoalConfigArgs{GoalConfigLevel: "CUSTOMER", CustomGoalID: "5"}, "on its own"},
		"non-numeric goal":     {UpdateCampaignGoalConfigArgs{CustomGoalID: "five"}, "numeric"},
	} {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			tc.args.CampaignID = "7"
			if _, err := runUpdateCampaignGoalConfig(t.Context(), nil, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	for name, tc := range map[string]struct {
		routes []conversionRoute
		args   UpdateCampaignGoalConfigArgs
		want   string
	}{
		"clear without a custom goal": {[]conversionRoute{campaignConfigRoute("CAMPAIGN", "")}, UpdateCampaignGoalConfigArgs{ClearCustomGoal: true}, "no custom conversion goal"},
		"unknown custom goal":         {[]conversionRoute{campaignConfigRoute("CAMPAIGN", ""), {"FROM custom_conversion_goal", `[]`}}, UpdateCampaignGoalConfigArgs{CustomGoalID: "5"}, "was not found"},
		"removed custom goal": {[]conversionRoute{campaignConfigRoute("CAMPAIGN", ""), {"FROM custom_conversion_goal", `[{"customConversionGoal":{"name":"Old","status":"REMOVED"}}]`}},
			UpdateCampaignGoalConfigArgs{CustomGoalID: "5"}, "is removed"},
	} {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, tc.routes...)
			tc.args.CustomerID, tc.args.CampaignID = "1", "7"
			if _, err := runUpdateCampaignGoalConfig(t.Context(), newTestClient(t, srv), tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if capture.mutates != 0 {
				t.Error("nothing may be written")
			}
		})
	}
}

var actionsRoute = conversionRoute{"FROM conversion_action WHERE conversion_action.id IN", `[{"conversionAction":{"id":"42"}},{"conversionAction":{"id":"43"}}]`}

func TestCreateCustomConversionGoal(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, selfTracked, actionsRoute)
	c := newTestClient(t, srv)
	preview, err := runCreateCustomConversionGoal(t.Context(), c, CreateCustomConversionGoalArgs{
		CustomerID: "1", Name: "Leads", ConversionActionIDs: []string{"43", "42"},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
		return runCreateCustomConversionGoal(t.Context(), c, CreateCustomConversionGoalArgs{Confirm: token})
	}); rounds != 1 {
		t.Errorf("took %d confirmations, want 1", rounds)
	}
	create := opCreate(t, capture.ops(t)[0], "customConversionGoalOperation")
	actions, _ := create["conversionActions"].([]any)
	if create["name"] != "Leads" || create["status"] != "ENABLED" || len(actions) != 2 ||
		actions[0] != "customers/1/conversionActions/43" || actions[1] != "customers/1/conversionActions/42" {
		t.Errorf("create = %v", create)
	}
}

func TestCreateCustomConversionGoal_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		routes []conversionRoute
		args   CreateCustomConversionGoalArgs
		want   string
	}{
		"no name":        {nil, CreateCustomConversionGoalArgs{ConversionActionIDs: []string{"42"}}, "name is required"},
		"no actions":     {nil, CreateCustomConversionGoalArgs{Name: "x"}, "at least one"},
		"cross-account":  {[]conversionRoute{managerTracked}, CreateCustomConversionGoalArgs{Name: "x", ConversionActionIDs: []string{"42"}}, "re-run with customer_id 999"},
		"duplicate id":   {[]conversionRoute{selfTracked}, CreateCustomConversionGoalArgs{Name: "x", ConversionActionIDs: []string{"42", "42"}}, "more than once"},
		"non-numeric id": {[]conversionRoute{selfTracked}, CreateCustomConversionGoalArgs{Name: "x", ConversionActionIDs: []string{"42)"}}, "numeric"},
		"unknown action": {[]conversionRoute{selfTracked, actionsRoute}, CreateCustomConversionGoalArgs{Name: "x", ConversionActionIDs: []string{"42", "77"}}, "conversion action 77 was not found"},
	} {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, tc.routes...)
			tc.args.CustomerID = "1"
			if _, err := runCreateCustomConversionGoal(t.Context(), newTestClient(t, srv), tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if capture.mutates != 0 {
				t.Error("nothing may be written")
			}
		})
	}
}

func TestUpdateCustomConversionGoal(t *testing.T) {
	cases := map[string]struct {
		args       UpdateCustomConversionGoalArgs
		wantRounds int
		wantMask   string
	}{
		"rename":          {UpdateCustomConversionGoalArgs{Name: "Qualified leads"}, 1, "name"},
		"replace actions": {UpdateCustomConversionGoalArgs{ConversionActionIDs: []string{"42", "43"}}, 2, "conversionActions"},
		"both":            {UpdateCustomConversionGoalArgs{Name: "N", ConversionActionIDs: []string{"42"}}, 2, "name,conversionActions"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, selfTracked, customGoalRoute, actionsRoute)
			c := newTestClient(t, srv)
			tc.args.CustomerID, tc.args.GoalID = "1", "5"
			preview, err := runUpdateCustomConversionGoal(t.Context(), c, tc.args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
				return runUpdateCustomConversionGoal(t.Context(), c, UpdateCustomConversionGoalArgs{Confirm: token})
			}); rounds != tc.wantRounds {
				t.Errorf("took %d confirmations, want %d", rounds, tc.wantRounds)
			}
			op := goalOp(t, capture, 0, "customConversionGoalOperation")
			update, _ := op["update"].(map[string]any)
			if op["updateMask"] != tc.wantMask || update["resourceName"] != "customers/1/customConversionGoals/5" {
				t.Errorf("op = %v", op)
			}
		})
	}

	useTempState(t)
	if _, err := runUpdateCustomConversionGoal(t.Context(), nil, UpdateCustomConversionGoalArgs{GoalID: "5"}); err == nil || !strings.Contains(err.Error(), "no changes specified") {
		t.Errorf("empty update err = %v", err)
	}
}

func TestRemoveCustomConversionGoal_TakesTwoConfirmations(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, selfTracked, customGoalRoute)
	c := newTestClient(t, srv)
	preview, err := runRemoveCustomConversionGoal(t.Context(), c, RemoveCustomConversionGoalArgs{CustomerID: "1", GoalID: "5"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
		return runRemoveCustomConversionGoal(t.Context(), c, RemoveCustomConversionGoalArgs{Confirm: token})
	}); rounds != 2 {
		t.Errorf("took %d confirmations, want 2", rounds)
	}
	if op := goalOp(t, capture, 0, "customConversionGoalOperation"); op["remove"] != "customers/1/customConversionGoals/5" {
		t.Errorf("op = %v", op)
	}
}

func TestGoalTokens_AreBoundToTheirTool(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, selfTracked, customGoalRoute)
	c := newTestClient(t, srv)
	preview, err := runRemoveCustomConversionGoal(t.Context(), c, RemoveCustomConversionGoalArgs{CustomerID: "1", GoalID: "5"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := runUpdateCustomConversionGoal(t.Context(), c, UpdateCustomConversionGoalArgs{Confirm: preview.Token}); err == nil {
		t.Fatal("a remove token must not confirm through the update tool")
	}
	if capture.mutates != 0 {
		t.Error("nothing may be written")
	}
}
