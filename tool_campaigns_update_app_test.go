package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestUpdateCampaign_AppInstallWithoutTarget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channel     string
		strategy    string
		goal        string
		clear       bool
		wantAppGoal bool
	}{
		{"switch target CPI", "MULTI_CHANNEL", "TARGET_CPA", "OPTIMIZE_INSTALLS_TARGET_INSTALL_COST", false, true},
		{"switch target CPI with explicit clear", "MULTI_CHANNEL", "TARGET_CPA", "OPTIMIZE_INSTALLS_TARGET_INSTALL_COST", true, true},
		{"same strategy still changes install goal", "MULTI_CHANNEL", "MAXIMIZE_CONVERSIONS", "OPTIMIZE_INSTALLS_TARGET_INSTALL_COST", false, true},
		{"search campaign", "SEARCH", "TARGET_CPA", "", false, false},
		{"search campaign with explicit clear", "SEARCH", "TARGET_CPA", "", true, false},
		{"App in-app conversion goal", "MULTI_CHANNEL", "TARGET_CPA", "OPTIMIZE_IN_APP_CONVERSIONS_TARGET_CONVERSION_COST", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			var searches, mutations int
			var mutateBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "googleAds:search"):
					searches++
					var request struct{ Query string }
					if err := decodeJSONBody(r, &request); err != nil {
						t.Errorf("decode search: %v", err)
					}
					for _, field := range []string{"campaign.advertising_channel_type", "campaign.app_campaign_setting.bidding_strategy_goal_type", "campaign.id = 5"} {
						if !strings.Contains(request.Query, field) {
							t.Errorf("search query %q missing %q", request.Query, field)
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"campaign": map[string]any{
						"biddingStrategyType":    tc.strategy,
						"advertisingChannelType": tc.channel,
						"targetCpa":              map[string]any{"targetCpaMicros": "5000000"},
						"appCampaignSetting":     map[string]any{"biddingStrategyGoalType": tc.goal, "appId": "com.example.app", "appStore": "GOOGLE_APP_STORE"},
					}}}})
				case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
					mutations++
					if err := decodeJSONBody(r, &mutateBody); err != nil {
						t.Errorf("decode mutate: %v", err)
					}
					_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
				default:
					t.Errorf("unexpected API request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			args := UpdateCampaignArgs{CustomerID: "1", CampaignID: "5", BiddingStrategy: "MAXIMIZE_CONVERSIONS", ClearTargetCPA: tc.clear}
			preview, err := runUpdateCampaign(t.Context(), c, args)
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if preview.Token == "" || mutations != 0 || searches != 1 {
				t.Fatalf("preview token=%q, mutations=%d, searches=%d; want token, no mutation, one lookup", preview.Token, mutations, searches)
			}
			if tc.wantAppGoal && !strings.Contains(preview.Preview, "without a target CPI") {
				t.Errorf("preview hides App bidding goal change: %q", preview.Preview)
			}
			// Confirm only the staged token; the apply must not re-read campaign
			// state or reconstruct the operation from fresh arguments.
			if _, err := runUpdateCampaign(t.Context(), c, UpdateCampaignArgs{Confirm: preview.Token}); err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if searches != 1 || mutations != 1 {
				t.Fatalf("after confirm: searches=%d mutations=%d, want 1 each", searches, mutations)
			}
			ops, _ := mutateBody["mutateOperations"].([]any)
			if len(ops) != 1 {
				t.Fatalf("want one atomic campaign operation, got %v", mutateBody)
			}
			outer, _ := ops[0].(map[string]any)
			op, _ := outer["campaignOperation"].(map[string]any)
			wantUpdate := map[string]any{"resourceName": "customers/1/campaigns/5", "maximizeConversions": map[string]any{}}
			wantMask := []string{"maximizeConversions.targetCpaMicros"}
			if tc.wantAppGoal {
				wantUpdate["appCampaignSetting"] = map[string]any{"biddingStrategyGoalType": "OPTIMIZE_INSTALLS_WITHOUT_TARGET_INSTALL_COST"}
				wantMask = append(wantMask, "appCampaignSetting.biddingStrategyGoalType")
			}
			if !reflect.DeepEqual(op["update"], wantUpdate) {
				t.Errorf("update = %#v, want %#v", op["update"], wantUpdate)
			}
			mask, _ := op["updateMask"].(string)
			gotMask := strings.Split(mask, ",")
			slices.Sort(gotMask)
			slices.Sort(wantMask)
			if !slices.Equal(gotMask, wantMask) {
				t.Errorf("updateMask = %q, want leaves %v", mask, wantMask)
			}
		})
	}
}

func TestUpdateCampaign_AppInstallLookupFailureDoesNotStage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"missing campaign", http.StatusOK, `{"results":[]}`},
		{"API denied", http.StatusForbidden, `{"error":{"message":"lookup denied"}}`},
		{"malformed campaign", http.StatusOK, `{"results":[{"campaign":"invalid"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempState(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "googleAds:search") {
					t.Errorf("lookup failure triggered unexpected request: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			for _, clear := range []bool{false, true} {
				result, err := runUpdateCampaign(t.Context(), newTestClient(t, srv), UpdateCampaignArgs{
					CustomerID: "1", CampaignID: "5", BiddingStrategy: "MAXIMIZE_CONVERSIONS", ClearTargetCPA: clear,
				})
				if err == nil || result.Token != "" {
					t.Errorf("clear=%v: result=%+v error=%v; want lookup error and no token", clear, result, err)
				}
			}
		})
	}
}
