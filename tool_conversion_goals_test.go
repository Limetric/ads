package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestConversionGoalReads(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t,
		conversionRoute{"FROM customer_conversion_goal", `[{"customerConversionGoal":{"category":"PURCHASE","origin":"WEBSITE","biddable":true}}]`},
		conversionRoute{"FROM campaign_conversion_goal", `[{"campaign":{"id":"7"},"campaignConversionGoal":{"category":"PURCHASE","origin":"WEBSITE","biddable":false}}]`},
		conversionRoute{"FROM conversion_goal_campaign_config", `[{"campaign":{"id":"7"},"conversionGoalCampaignConfig":{"goalConfigLevel":"CAMPAIGN"}}]`},
		conversionRoute{"FROM custom_conversion_goal", `[{"customConversionGoal":{"id":"5","name":"Leads"}},{"customConversionGoal":{"id":"6","name":"Sales"}}]`},
	)
	c := newTestClient(t, srv)

	account, err := runAccountConversionGoals(t.Context(), c, AccountConversionGoalsArgs{CustomerID: "1"})
	if err != nil || account.TotalCount != 1 {
		t.Fatalf("account goals = %+v, %v", account, err)
	}
	if _, fields := account.tableRows(); strings.Join(fields, ",") != "customer_conversion_goal.category,customer_conversion_goal.origin,customer_conversion_goal.biddable" {
		t.Errorf("table fields = %v", fields)
	}

	campaign, err := runCampaignConversionGoals(t.Context(), c, CampaignConversionGoalsArgs{CustomerID: "1", CampaignID: "7"})
	if err != nil || campaign.TotalCount != 1 || len(campaign.GoalConfigs) != 1 {
		t.Fatalf("campaign goals = %+v, %v", campaign, err)
	}
	for _, q := range capture.queries[1:3] {
		if !strings.Contains(q, "campaign.status != 'REMOVED' AND campaign.id = 7") {
			t.Errorf("campaign read must filter to the campaign: %s", q)
		}
	}

	custom, err := runCustomConversionGoals(t.Context(), c, CustomConversionGoalsArgs{CustomerID: "1"})
	if err != nil || custom.TotalCount != 2 {
		t.Fatalf("custom goals = %+v, %v", custom, err)
	}
	if last := capture.queries[len(capture.queries)-1]; !strings.Contains(last, "status != 'REMOVED'") {
		t.Errorf("custom read must skip removed goals: %s", last)
	}

	if _, err := runCampaignConversionGoals(t.Context(), c, CampaignConversionGoalsArgs{CustomerID: "1", CampaignID: "7 OR 1=1"}); err == nil {
		t.Error("a non-numeric campaign_id must be refused")
	}
}

func TestConversionsRead_ListsGoalFields(t *testing.T) {
	srv := gaqlServer(t, `[]`, "conversion_action.origin", "conversion_action.primary_for_goal")
	defer srv.Close()
	if _, err := runConversions(t.Context(), newTestClient(t, srv), ConversionsArgs{CustomerID: "1"}); err != nil {
		t.Fatal(err)
	}
}

func TestMCP_ConversionGoalTools(t *testing.T) {
	cs := mcpSession(t, &Client{cfg: &GoogleConfig{}})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	schemas := map[string]string{}
	for _, tool := range res.Tools {
		b, _ := json.Marshal(tool.InputSchema)
		schemas[tool.Name] = string(b)
	}
	for _, name := range []string{
		"google_create_conversion_action", "google_update_conversion_action", "google_remove_conversion_action",
		"google_account_conversion_goals", "google_campaign_conversion_goals", "google_custom_conversion_goals",
		"google_update_account_conversion_goals", "google_update_campaign_conversion_goals", "google_update_campaign_goal_config",
		"google_create_custom_conversion_goal", "google_update_custom_conversion_goal", "google_remove_custom_conversion_goal",
	} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("MCP tool %q is not registered", name)
		}
	}
	// The shared settings are an embedded struct; its fields must be flattened
	// into both tools' schemas, not nested or dropped.
	for _, name := range []string{"google_create_conversion_action", "google_update_conversion_action"} {
		for _, prop := range []string{`"primary_for_goal"`, `"click_through_lookback_window_days"`, `"attribution_model"`} {
			if !strings.Contains(schemas[name], prop) {
				t.Errorf("%s schema lacks %s: %s", name, prop, schemas[name])
			}
		}
		if strings.Contains(schemas[name], "ConversionActionSettings") {
			t.Errorf("%s schema nests the embedded settings: %s", name, schemas[name])
		}
	}
}
