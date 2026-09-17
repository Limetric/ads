package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// conversionRoute answers a GAQL search whose query contains match.
type conversionRoute struct {
	match   string
	results string
}

// conversionCapture records what a conversionServer was asked.
type conversionCapture struct {
	queries  []string
	mutates  int
	lastBody map[string]any
}

// ops returns the mutateOperations of the most recent mutate.
func (c *conversionCapture) ops(t *testing.T) []map[string]any {
	t.Helper()
	raw, _ := c.lastBody["mutateOperations"].([]any)
	if len(raw) == 0 {
		t.Fatalf("no mutate operations captured (body=%v)", c.lastBody)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, op := range raw {
		m, _ := op.(map[string]any)
		out = append(out, m)
	}
	return out
}

// conversionServer fakes the Ads API for the conversion tools: each search is
// answered by the first route whose match its query contains (an unmatched
// query fails the test), and mutates are recorded.
func conversionServer(t *testing.T, routes ...conversionRoute) (*httptest.Server, *conversionCapture) {
	t.Helper()
	capture := &conversionCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "googleAds:search"):
			var body struct {
				Query string `json:"query"`
			}
			_ = decodeJSONBody(r, &body)
			capture.queries = append(capture.queries, body.Query)
			for _, route := range routes {
				if strings.Contains(body.Query, route.match) {
					_, _ = w.Write([]byte(`{"results":` + route.results + `}`))
					return
				}
			}
			t.Errorf("unexpected search query: %s", body.Query)
			_, _ = w.Write([]byte(`{"results":[]}`))
		case strings.HasSuffix(r.URL.Path, "googleAds:mutate"):
			capture.mutates++
			_ = decodeJSONBody(r, &capture.lastBody)
			_, _ = w.Write([]byte(`{"mutateOperationResponses":[{}]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

// selfTracked answers the conversion tracking lookup with the account itself.
var selfTracked = conversionRoute{"conversion_tracking_setting", `[{"customer":{"conversionTrackingSetting":{"googleAdsConversionCustomer":"customers/1"}}}]`}

// managerTracked answers it with a conversion tracking manager, 999.
var managerTracked = conversionRoute{"conversion_tracking_setting", `[{"customer":{"conversionTrackingSetting":{"googleAdsConversionCustomer":"customers/999"}}}]`}

func conversionActionRoute(owner, status, category string) conversionRoute {
	ownerField := ""
	if owner != "" {
		ownerField = `,"ownerCustomer":"customers/` + owner + `"`
	}
	return conversionRoute{"FROM conversion_action WHERE conversion_action.id = 42",
		`[{"conversionAction":{"id":"42","name":"Checkout","type":"WEBPAGE","status":"` + status +
			`","category":"` + category + `","origin":"WEBSITE"` + ownerField + `}}]`}
}

// confirmAll walks a preview through every confirmation it asks for and
// returns how many rounds applying took.
func confirmAll(t *testing.T, preview WriteResult, confirm func(token string) (WriteResult, error)) int {
	t.Helper()
	rounds := 0
	for token := preview.Token; token != ""; {
		res, err := confirm(token)
		if err != nil {
			t.Fatalf("confirm round %d: %v", rounds+1, err)
		}
		rounds++
		if res.Applied {
			return rounds
		}
		token = res.Token
	}
	t.Fatal("the write never applied")
	return rounds
}

func boolPtr(b bool) *bool { return &b }

func TestCreateConversionAction_StagesAllSettings(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, selfTracked)
	c := newTestClient(t, srv)
	value, seconds := 0.0, int64(60)

	args := CreateConversionActionArgs{
		CustomerID: "1", Name: " Offline leads ", Type: "upload_clicks",
		ConversionActionSettings: ConversionActionSettings{
			Category: "submit_lead_form", CountingType: "ONE_PER_CLICK", PrimaryForGoal: boolPtr(false),
			DefaultValue: &value, CurrencyCode: "eur", AlwaysUseDefaultValue: boolPtr(false),
			ClickThroughLookbackDays: 90, ViewThroughLookbackDays: 1,
			AttributionModel: "GOOGLE_ADS_LAST_CLICK", PhoneCallDurationSeconds: &seconds,
		},
	}
	preview, err := runCreateConversionAction(t.Context(), c, args)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if capture.mutates != 0 {
		t.Fatal("preview must not mutate")
	}
	if strings.Contains(preview.Preview, "drives bidding") {
		t.Errorf("a secondary action must not carry the primary warning: %s", preview.Preview)
	}
	rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
		return runCreateConversionAction(t.Context(), c, CreateConversionActionArgs{Confirm: token})
	})
	if rounds != 1 {
		t.Errorf("create took %d confirmations, want 1", rounds)
	}
	create := opCreate(t, capture.ops(t)[0], "conversionActionOperation")
	want := map[string]any{
		"name": "Offline leads", "type": "UPLOAD_CLICKS", "status": "ENABLED", "category": "SUBMIT_LEAD_FORM",
		"countingType": "ONE_PER_CLICK", "primaryForGoal": false, "clickThroughLookbackWindowDays": "90",
		"viewThroughLookbackWindowDays": "1", "phoneCallDurationSeconds": "60",
	}
	for k, v := range want {
		if create[k] != v {
			t.Errorf("create[%s] = %#v, want %#v", k, create[k], v)
		}
	}
	values, _ := create["valueSettings"].(map[string]any)
	if values["defaultValue"] != 0.0 || values["defaultCurrencyCode"] != "EUR" || values["alwaysUseDefaultValue"] != false {
		t.Errorf("valueSettings = %v; a zero value and false must still be sent", values)
	}
	attribution, _ := create["attributionModelSettings"].(map[string]any)
	if attribution["attributionModel"] != "GOOGLE_ADS_LAST_CLICK" {
		t.Errorf("attributionModelSettings = %v", attribution)
	}
}

func TestCreateConversionAction_PrimaryByDefaultIsWarned(t *testing.T) {
	useTempState(t)
	srv, _ := conversionServer(t, selfTracked)
	preview, err := runCreateConversionAction(t.Context(), newTestClient(t, srv), CreateConversionActionArgs{CustomerID: "1", Name: "Buy", Type: "WEBPAGE"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(preview.Preview, "drives bidding") {
		t.Errorf("preview should warn that a primary action drives bidding: %s", preview.Preview)
	}
}

func TestCreateConversionAction_RejectsBadInputBeforeAnyRequest(t *testing.T) {
	neg, over := -1.0, int64(10001)
	cases := map[string]struct {
		args CreateConversionActionArgs
		want string
	}{
		"missing name":        {CreateConversionActionArgs{Type: "WEBPAGE"}, "name is required"},
		"missing type":        {CreateConversionActionArgs{Name: "x"}, "type is required"},
		"read-only type":      {CreateConversionActionArgs{Name: "x", Type: "GOOGLE_ANALYTICS_4_CUSTOM"}, "cannot be created"},
		"play without app":    {CreateConversionActionArgs{Name: "x", Type: "GOOGLE_PLAY_DOWNLOAD"}, "app_id is required"},
		"app on web type":     {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", AppID: "com.example"}, "only applies"},
		"unknown category":    {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{Category: "NOPE"}}, "unsupported category"},
		"counting type":       {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{CountingType: "ALL"}}, "unsupported counting_type"},
		"retired attribution": {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{AttributionModel: "LINEAR"}}, "unsupported attribution_model"},
		"click window":        {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{ClickThroughLookbackDays: 91}}, "between 1 and 90"},
		"view window":         {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{ViewThroughLookbackDays: 31}}, "between 1 and 30"},
		"call duration":       {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{PhoneCallDurationSeconds: &over}}, "between 0 and 10000"},
		"negative value":      {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{DefaultValue: &neg}}, "cannot be negative"},
		"currency":            {CreateConversionActionArgs{Name: "x", Type: "WEBPAGE", ConversionActionSettings: ConversionActionSettings{CurrencyCode: "EURO"}}, "three-letter"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			// A nil client proves validation runs before any API call.
			_, err := runCreateConversionAction(t.Context(), nil, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestCreateConversionAction_SendsAppIDForGooglePlay(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, selfTracked)
	c := newTestClient(t, srv)
	preview, err := runCreateConversionAction(t.Context(), c, CreateConversionActionArgs{CustomerID: "1", Name: "Installs", Type: "GOOGLE_PLAY_DOWNLOAD", AppID: " com.example.app "})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	confirmAll(t, preview, func(token string) (WriteResult, error) {
		return runCreateConversionAction(t.Context(), c, CreateConversionActionArgs{Confirm: token})
	})
	if create := opCreate(t, capture.ops(t)[0], "conversionActionOperation"); create["appId"] != "com.example.app" {
		t.Errorf("appId = %v", create["appId"])
	}
}

func TestCreateConversionAction_RefusesNonConversionCustomer(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, managerTracked)
	_, err := runCreateConversionAction(t.Context(), newTestClient(t, srv), CreateConversionActionArgs{CustomerID: "1", Name: "Buy", Type: "WEBPAGE"})
	if err == nil || !strings.Contains(err.Error(), "re-run with customer_id 999") {
		t.Fatalf("err = %v, want a pointer to the conversion tracking account", err)
	}
	if capture.mutates != 0 {
		t.Error("nothing may be written")
	}
}

func TestFetchConversionCustomerID_FallsBackToSelf(t *testing.T) {
	for name, results := range map[string]string{
		"no rows":     `[]`,
		"empty field": `[{"customer":{"conversionTrackingSetting":{}}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := conversionServer(t, conversionRoute{"conversion_tracking_setting", results})
			got, err := fetchConversionCustomerID(t.Context(), newTestClient(t, srv), "1")
			if err != nil || got != "1" {
				t.Fatalf("got %q, %v; want the account itself", got, err)
			}
		})
	}
}

func TestUpdateConversionAction_ConfirmationsFollowBiddingImpact(t *testing.T) {
	cases := map[string]struct {
		settings   ConversionActionSettings
		name       string
		wantRounds int
		wantMask   string
	}{
		"rename":                 {name: "Checkout v2", wantRounds: 1, wantMask: "name"},
		"value settings":         {settings: ConversionActionSettings{CurrencyCode: "USD", ViewThroughLookbackDays: 3}, wantRounds: 1, wantMask: "valueSettings.defaultCurrencyCode,viewThroughLookbackWindowDays"},
		"same category":          {settings: ConversionActionSettings{Category: "purchase"}, wantRounds: 1, wantMask: "category"},
		"new category":           {settings: ConversionActionSettings{Category: "SIGNUP"}, wantRounds: 2, wantMask: "category"},
		"demote to secondary":    {settings: ConversionActionSettings{PrimaryForGoal: boolPtr(false)}, wantRounds: 2, wantMask: "primaryForGoal"},
		"rename and attribution": {name: "N", settings: ConversionActionSettings{AttributionModel: "GOOGLE_SEARCH_ATTRIBUTION_DATA_DRIVEN"}, wantRounds: 1, wantMask: "name,attributionModelSettings.attributionModel"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, conversionActionRoute("1", "ENABLED", "PURCHASE"))
			c := newTestClient(t, srv)
			preview, err := runUpdateConversionAction(t.Context(), c, UpdateConversionActionArgs{
				CustomerID: "1", ConversionActionID: "42", Name: tc.name, ConversionActionSettings: tc.settings,
			})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
				return runUpdateConversionAction(t.Context(), c, UpdateConversionActionArgs{Confirm: token})
			})
			if rounds != tc.wantRounds {
				t.Errorf("took %d confirmations, want %d", rounds, tc.wantRounds)
			}
			op, _ := capture.ops(t)[0]["conversionActionOperation"].(map[string]any)
			if op["updateMask"] != tc.wantMask {
				t.Errorf("updateMask = %v, want %s", op["updateMask"], tc.wantMask)
			}
			update, _ := op["update"].(map[string]any)
			if update["resourceName"] != "customers/1/conversionActions/42" {
				t.Errorf("resourceName = %v", update["resourceName"])
			}
		})
	}
}

func TestUpdateConversionAction_RefusesActionsItCannotChange(t *testing.T) {
	cases := map[string]struct {
		route conversionRoute
		want  string
	}{
		"not found":      {conversionRoute{"FROM conversion_action", `[]`}, "was not found"},
		"system-defined": {conversionActionRoute("", "ENABLED", "PURCHASE"), "system-defined"},
		"other owner":    {conversionActionRoute("999", "ENABLED", "PURCHASE"), "re-run with customer_id 999"},
		"removed":        {conversionActionRoute("1", "REMOVED", "PURCHASE"), "already removed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useTempState(t)
			srv, capture := conversionServer(t, tc.route)
			c := newTestClient(t, srv)
			_, err := runUpdateConversionAction(t.Context(), c, UpdateConversionActionArgs{CustomerID: "1", ConversionActionID: "42", Name: "x"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("update err = %v, want %q", err, tc.want)
			}
			_, err = runRemoveConversionAction(t.Context(), c, RemoveConversionActionArgs{CustomerID: "1", ConversionActionID: "42"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("remove err = %v, want %q", err, tc.want)
			}
			if capture.mutates != 0 {
				t.Error("nothing may be written")
			}
		})
	}
}

func TestUpdateConversionAction_RejectsEmptyAndNonNumeric(t *testing.T) {
	useTempState(t)
	if _, err := runUpdateConversionAction(t.Context(), nil, UpdateConversionActionArgs{ConversionActionID: "42"}); err == nil || !strings.Contains(err.Error(), "no changes specified") {
		t.Errorf("empty update err = %v", err)
	}
	if _, err := runUpdateConversionAction(t.Context(), nil, UpdateConversionActionArgs{ConversionActionID: "42 OR 1=1", Name: "x"}); err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Errorf("non-numeric id err = %v", err)
	}
	if _, err := runRemoveConversionAction(t.Context(), nil, RemoveConversionActionArgs{}); err == nil {
		t.Error("remove without an id must fail")
	}
}

func TestRemoveConversionAction_TakesTwoConfirmations(t *testing.T) {
	useTempState(t)
	srv, capture := conversionServer(t, conversionActionRoute("1", "ENABLED", "PURCHASE"))
	c := newTestClient(t, srv)
	preview, err := runRemoveConversionAction(t.Context(), c, RemoveConversionActionArgs{CustomerID: "1", ConversionActionID: "42"})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(preview.Preview, `"Checkout"`) {
		t.Errorf("preview should name the action: %s", preview.Preview)
	}
	rounds := confirmAll(t, preview, func(token string) (WriteResult, error) {
		return runRemoveConversionAction(t.Context(), c, RemoveConversionActionArgs{Confirm: token})
	})
	if rounds != 2 {
		t.Errorf("remove took %d confirmations, want 2", rounds)
	}
	op, _ := capture.ops(t)[0]["conversionActionOperation"].(map[string]any)
	if op["remove"] != "customers/1/conversionActions/42" {
		t.Errorf("remove = %v", op["remove"])
	}
}

func TestConversionSettingFlags_OnlyGivenFlagsBecomeSettings(t *testing.T) {
	newCmd := func() (*cobra.Command, *ConversionActionSettings, *conversionSettingFlags) {
		cmd := &cobra.Command{Use: "x"}
		s, v := &ConversionActionSettings{}, &conversionSettingFlags{}
		addConversionSettingFlags(cmd, s, v)
		return cmd, s, v
	}

	cmd, s, v := newCmd()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	applyConversionSettingFlags(cmd, s, v)
	if s.PrimaryForGoal != nil || s.DefaultValue != nil || s.AlwaysUseDefaultValue != nil || s.PhoneCallDurationSeconds != nil {
		t.Errorf("unset flags must leave settings nil: %+v", s)
	}

	cmd, s, v = newCmd()
	if err := cmd.ParseFlags([]string{"--primary=false", "--default-value=0", "--always-use-default-value", "--call-duration-seconds=0"}); err != nil {
		t.Fatal(err)
	}
	applyConversionSettingFlags(cmd, s, v)
	if s.PrimaryForGoal == nil || *s.PrimaryForGoal || s.DefaultValue == nil || *s.DefaultValue != 0 ||
		s.AlwaysUseDefaultValue == nil || !*s.AlwaysUseDefaultValue || s.PhoneCallDurationSeconds == nil || *s.PhoneCallDurationSeconds != 0 {
		b, _ := json.Marshal(s)
		t.Errorf("given flags must become settings, zero values included: %s", b)
	}
}
