package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunBingAccounts(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{
		"/CustomerManagement/v13/AccountsInfo/Query": `{"AccountsInfo":[
			{"Id":"111","Name":"Main","Number":"X0001","AccountLifeCycleStatus":"Active"},
			{"Id":"222","Name":"Second","Number":"X0002","AccountLifeCycleStatus":"Pause","PauseReason":"1"}]}`,
	})
	defer srv.Close()

	res, err := runBingAccounts(t.Context(), newTestBingClient(t, srv), BingAccountsArgs{})
	if err != nil {
		t.Fatalf("runBingAccounts: %v", err)
	}
	if res.TotalCount != 2 || res.Accounts[1].PauseReason != "1" {
		t.Fatalf("result = %+v", res)
	}
	if res.Message != "" {
		t.Errorf("with a manager account configured there is nothing to explain: %q", res.Message)
	}
}

func TestRunBingAccounts_ExplainsMissingManagerAccount(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{"/AccountsInfo/Query": `{"AccountsInfo":[]}`})
	defer srv.Close()
	c := newTestBingClientWith(t, srv, &BingConfig{}) // no CustomerID

	res, err := runBingAccounts(t.Context(), c, BingAccountsArgs{})
	if err != nil {
		t.Fatalf("runBingAccounts: %v", err)
	}
	if !strings.Contains(res.Message, "BING_ADS_CUSTOMER_ID") {
		t.Errorf("the fallback should explain itself: %q", res.Message)
	}
}

func TestRunBingAccountInfo(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{
		"/CustomerManagement/v13/Account/Query": `{"Account":{"Id":"123456","Name":"Main","Number":"X0001","CurrencyCode":"USD","TimeZone":"PacificTimeUSCanadaTijuana","AccountLifeCycleStatus":"Active","ParentCustomerId":"777"}}`,
	})
	defer srv.Close()

	res, err := runBingAccountInfo(t.Context(), newTestBingClient(t, srv), BingAccountInfoArgs{})
	if err != nil {
		t.Fatalf("runBingAccountInfo: %v", err)
	}
	// The currency is the point of this tool: every Spend figure the platform
	// returns is denominated in it.
	if res.CurrencyCode != "USD" || res.AccountID != "123456" {
		t.Errorf("result = %+v", res)
	}
}

func TestRunBingCampaigns(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{
		"/Campaigns/QueryByAccountId": `{"Campaigns":[
			{"Id":"1","Name":"Brand","Status":"Active","CampaignType":"Search","DailyBudget":25.5,"BudgetType":"DailyBudgetStandard"},
			{"Id":"2","Name":"Shared","Status":"Paused","CampaignType":"Shopping","BudgetId":"900"}]}`,
	})
	defer srv.Close()

	res, err := runBingCampaigns(t.Context(), newTestBingClient(t, srv), BingCampaignsArgs{})
	if err != nil {
		t.Fatalf("runBingCampaigns: %v", err)
	}
	if res.TotalCount != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Campaigns[0].DailyBudget == nil || *res.Campaigns[0].DailyBudget != 25.5 {
		t.Errorf("daily budget = %v", res.Campaigns[0].DailyBudget)
	}
	if res.Campaigns[1].SharedBudgetID != "900" {
		t.Errorf("a shared budget must be visible in the row: %+v", res.Campaigns[1])
	}
	// A tool called "campaigns" that returns no spend has to say where spend is.
	if !strings.Contains(res.Message, "bing_campaign_performance") {
		t.Errorf("message = %q", res.Message)
	}
}

func TestRunBingAdGroupsAndKeywords_RequireTheirParent(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{"/": `{}`})
	defer srv.Close()
	c := newTestBingClient(t, srv)

	if _, err := runBingAdGroups(t.Context(), c, BingAdGroupsArgs{}); err == nil {
		t.Error("ad groups need a campaign ID")
	}
	if _, err := runBingKeywords(t.Context(), c, BingKeywordsArgs{}); err == nil {
		t.Error("keywords need an ad group ID")
	}
	if _, err := runBingAdGroups(t.Context(), c, BingAdGroupsArgs{CampaignID: "abc"}); err == nil {
		t.Error("a non-numeric campaign ID must be rejected before it is sent")
	}
}

func TestRunBingReportTool_FastPathReturnsRows(t *testing.T) {
	bingReportPollInterval = time.Millisecond
	t.Cleanup(func() { bingReportPollInterval = 2 * time.Second })
	useTempState(t)

	srv := bingReportServer(t, 1, "CampaignName,Spend,Clicks\nBrand,12.34,7\n")
	defer srv.Close()

	res, err := runBingReportTool(t.Context(), newTestBingClient(t, srv), bingCampaignPerformancePreset, BingPerformanceArgs{})
	if err != nil {
		t.Fatalf("runBingReportTool: %v", err)
	}
	if res.Job != nil {
		t.Fatalf("a report that finished should return rows, not a job: %+v", res.Job)
	}
	if res.RowCount != 1 {
		t.Fatalf("rows = %d", res.RowCount)
	}
	var row map[string]string
	_ = json.Unmarshal(res.Rows[0], &row)
	if row["Spend"] != "12.34" {
		t.Errorf("row = %v", row)
	}
	// The columns come back in the order the CSV carried them, which is what
	// keys the values.
	if len(res.Columns) != 3 || res.Columns[0] != "CampaignName" {
		t.Errorf("columns = %v", res.Columns)
	}
	if res.DateRange == "" {
		t.Error("a metrics answer has to say which days it covers")
	}
}

func TestRunBingReportTool_SlowPathHandsBackAJob(t *testing.T) {
	bingReportPollInterval = time.Millisecond
	bingReportDeadline = 20 * time.Millisecond
	t.Cleanup(func() {
		bingReportPollInterval = 2 * time.Second
		bingReportDeadline = 45 * time.Second
	})
	useTempState(t)

	srv := bingReportServer(t, 1000, "") // never finishes
	defer srv.Close()
	c := newTestBingClient(t, srv)

	res, err := runBingReportTool(t.Context(), c, bingCampaignPerformancePreset, BingPerformanceArgs{})
	if err != nil {
		t.Fatalf("a slow report must not fail the call: %v", err)
	}
	if res.Job == nil {
		t.Fatal("expected a job handle")
	}
	if !strings.Contains(res.Job.FetchCommand, res.Job.Job) {
		t.Errorf("the handle must come with the command that collects it: %+v", res.Job)
	}
	// The handle has to survive the process, since the CLI and the MCP server
	// are different processes.
	job, err := loadBingReportJob(res.Job.Job)
	if err != nil {
		t.Fatalf("the returned handle must be loadable: %v", err)
	}
	if job.ReportRequestID != "req-1" || job.AccountID != "123456" {
		t.Errorf("persisted job = %+v", job)
	}
}

func TestRunBingReportFetch_CollectsRowsAndClearsTheHandle(t *testing.T) {
	bingReportPollInterval = time.Millisecond
	t.Cleanup(func() { bingReportPollInterval = 2 * time.Second })
	useTempState(t)

	srv := bingReportServer(t, 0, "CampaignName,Spend\nBrand,9.99\n")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	id, err := newBingJobID()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveBingReportJob(&bingReportJob{
		ID: id, Tool: "campaign_performance", ReportRequestID: "req-1",
		AccountID: "123456", SubmittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := runBingReportFetch(t.Context(), c, BingReportFetchArgs{Job: id})
	if err != nil {
		t.Fatalf("runBingReportFetch: %v", err)
	}
	if res.RowCount != 1 {
		t.Fatalf("rows = %d", res.RowCount)
	}
	// The rows are in hand; the handle has nothing left to offer.
	if _, err := loadBingReportJob(id); err == nil {
		t.Error("a collected job handle should be cleared")
	}
}

func TestRunBingReportFetch_StillRunning(t *testing.T) {
	bingReportPollInterval = time.Millisecond
	bingReportDeadline = 20 * time.Millisecond
	t.Cleanup(func() {
		bingReportPollInterval = 2 * time.Second
		bingReportDeadline = 45 * time.Second
	})
	useTempState(t)

	srv := bingReportServer(t, 1000, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	id, _ := newBingJobID()
	if err := saveBingReportJob(&bingReportJob{ID: id, ReportRequestID: "req-1", AccountID: "123456", SubmittedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	res, err := runBingReportFetch(t.Context(), c, BingReportFetchArgs{Job: id})
	if err != nil {
		t.Fatalf("a still-running report is not an error: %v", err)
	}
	if res.Job == nil || res.Job.Job != id {
		t.Fatalf("the same handle should come back: %+v", res.Job)
	}
	// It is still fetchable — the handle must not be consumed by a failed fetch.
	if _, err := loadBingReportJob(id); err != nil {
		t.Errorf("handle should survive a fetch that found nothing yet: %v", err)
	}
}

func TestBingPerformanceArgs_ResolveRange(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name               string
		args               BingPerformanceArgs
		wantStart, wantEnd string
		wantErr            string
	}{
		{name: "default is 30 complete days", wantStart: "2026-07-11", wantEnd: "2026-08-09"},
		{name: "days", args: BingPerformanceArgs{Days: 7}, wantStart: "2026-08-03", wantEnd: "2026-08-09"},
		{
			name:      "explicit range",
			args:      BingPerformanceArgs{DateStart: "2026-01-01", DateEnd: "2026-01-31"},
			wantStart: "2026-01-01", wantEnd: "2026-01-31",
		},
		{name: "half a range", args: BingPerformanceArgs{DateStart: "2026-01-01"}, wantErr: "together"},
		{name: "unparseable", args: BingPerformanceArgs{DateStart: "01/01/2026", DateEnd: "2026-01-31"}, wantErr: "YYYY-MM-DD"},
		{name: "backwards", args: BingPerformanceArgs{DateStart: "2026-02-01", DateEnd: "2026-01-01"}, wantErr: "before"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := tc.args.resolveRange(now)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRange: %v", err)
			}
			if start.Format(time.DateOnly) != tc.wantStart || end.Format(time.DateOnly) != tc.wantEnd {
				t.Errorf("range = %s..%s, want %s..%s",
					start.Format(time.DateOnly), end.Format(time.DateOnly), tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// --- writes ---------------------------------------------------------------

// bingBudgetServer serves one campaign and records the update it receives.
func bingBudgetServer(t *testing.T, campaign string, partialErrors string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var update map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Campaigns/QueryByAccountId"):
			_, _ = w.Write([]byte(`{"Campaigns":[` + campaign + `]}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/Campaigns"):
			_ = json.NewDecoder(r.Body).Decode(&update)
			_, _ = w.Write([]byte(`{"PartialErrors":[` + partialErrors + `]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	return srv, &update
}

func TestRunBingBudgetSet_PreviewThenConfirm(t *testing.T) {
	useTempState(t)
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0,"BudgetType":"DailyBudgetStandard"}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Applied {
		t.Fatal("the first call must never mutate")
	}
	if *update != nil {
		t.Fatal("the preview call issued a write")
	}
	// The preview has to say what it will write and what it will leave alone —
	// this API is full-replace in places, and the two read identically unless
	// the preview spells it out.
	for _, want := range []string{"Fields written: Id, DailyBudget", "from 25.00", "30.00", "partial update"} {
		if !strings.Contains(preview.Preview, want) {
			t.Errorf("preview should mention %q:\n%s", want, preview.Preview)
		}
	}

	applied, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30, Confirm: preview.Token})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !applied.Applied {
		t.Fatal("the confirmed call should apply")
	}
	campaigns, _ := (*update)["Campaigns"].([]any)
	if len(campaigns) != 1 {
		t.Fatalf("update = %v", *update)
	}
	sent, _ := campaigns[0].(map[string]any)
	if sent["Id"] != "1" || sent["DailyBudget"] != 30.0 {
		t.Errorf("sent campaign = %v", sent)
	}
	// Partial update: only the fields being changed travel.
	if len(sent) != 2 {
		t.Errorf("sent %d fields, want only Id and DailyBudget: %v", len(sent), sent)
	}
}

func TestRunBingBudgetSet_PartialFailureIsNotSuccess(t *testing.T) {
	useTempState(t)
	srv, _ := bingBudgetServer(t,
		`{"Id":"1","Name":"Brand","DailyBudget":25.0}`,
		`{"Code":1042,"ErrorCode":"CampaignServiceInvalidDailyBudget","Message":"too low","Index":0}`)
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30, Confirm: preview.Token})
	if err == nil {
		t.Fatal("an item that failed must fail the call, never a bare ok")
	}
	if !strings.Contains(err.Error(), "CampaignServiceInvalidDailyBudget") {
		t.Errorf("error = %v", err)
	}
}

func TestRunBingBudgetSet_Guards(t *testing.T) {
	useTempState(t)
	srv, _ := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	if _, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 0}); err == nil {
		t.Error("a non-positive budget must be rejected")
	}
	// The spend cap is shared across platforms and defaults to 50/day.
	if _, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 5000}); err == nil {
		t.Error("a budget over the cap must be rejected")
	}
	t.Setenv("GOOGLE_ADS_BLOCKED_OPS", "set_campaign_budget")
	if _, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30}); err == nil {
		t.Error("a blocked operation must be refused")
	}
}

func TestRunBingBudgetSet_SharedBudgetIsRefused(t *testing.T) {
	useTempState(t)
	srv, _ := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0,"BudgetId":"900"}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	_, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err == nil || !strings.Contains(err.Error(), "shared budget") {
		t.Fatalf("a shared budget cannot be changed campaign-side; error = %v", err)
	}
}

func TestRunBingBudgetSet_UnknownCampaign(t *testing.T) {
	useTempState(t)
	srv, _ := bingBudgetServer(t, `{"Id":"1","Name":"Brand"}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	// Better to fail now than to stage a preview whose apply will fail.
	_, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "42", DailyBudget: 30})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunBingBudgetSet_LargeIncreaseTakesTwoConfirmations(t *testing.T) {
	useTempState(t)
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":10.0}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	// 10 → 40 is a 300% increase (issue #12).
	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 40})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 40, Confirm: preview.Token})
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied {
		t.Fatal("a large increase must not apply on the first confirmation")
	}
	if second.Token == "" || *update != nil {
		t.Fatalf("expected a second token and no write yet: %+v", second)
	}
	final, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 40, Confirm: second.Token})
	if err != nil {
		t.Fatal(err)
	}
	if !final.Applied || *update == nil {
		t.Error("the second confirmation should apply")
	}
}

func TestBingWrite_TokenIsBoundToItsPlatformAndTool(t *testing.T) {
	useTempState(t)
	srv, _ := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := peekMutation(preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	// `ads confirm` routes on this: a Bing token must never be applied through
	// Google's API.
	if staged.Platform != bingPlatformName {
		t.Errorf("staged platform = %q, want %q", staged.Platform, bingPlatformName)
	}
	if staged.Dispatch != dispatchBingUpdateCampaign {
		t.Errorf("dispatch = %q", staged.Dispatch)
	}
	// The declared amount is what lets the shared spend cap be re-checked at
	// confirm time without safety.go knowing Bing's payload shape.
	if len(staged.BudgetAmounts) != 1 || staged.BudgetAmounts[0] != 30 {
		t.Errorf("budget amounts = %v", staged.BudgetAmounts)
	}
}

func TestRunConfirm_AppliesABingWrite(t *testing.T) {
	useTempState(t)
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runConfirm(t.Context(), c, preview.Token)
	if err != nil {
		t.Fatalf("runConfirm: %v", err)
	}
	if !res.Applied || res.Tool != "set_campaign_budget" {
		t.Fatalf("confirm result = %+v", res)
	}
	if *update == nil {
		t.Error("`ads confirm` should have applied the staged write")
	}
	// Every applied write is audited, and the line has to name the network:
	// both platforms have a set_campaign_budget.
	entries, err := readAuditLog()
	if err != nil {
		t.Fatalf("readAuditLog: %v", err)
	}
	if len(entries) == 0 || !strings.Contains(entries[len(entries)-1], "platform=bing") {
		t.Errorf("audit log = %v", entries)
	}
}

func TestRunConfirm_RevalidatesTheSpendCap(t *testing.T) {
	useTempState(t)
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatal(err)
	}
	// Tightening the cap between preview and confirm has to be enforced on the
	// generic confirm path too, not just by re-running the command.
	t.Setenv("GOOGLE_ADS_MAX_DAILY_BUDGET", "10")
	if _, err := runConfirm(t.Context(), c, preview.Token); err == nil {
		t.Fatal("a staged budget above the current cap must be refused at confirm time")
	}
	if *update != nil {
		t.Error("nothing should have been written")
	}
}

func TestApplierForToken_RoutesToTheStagingPlatform(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	srv, _ := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	// applierForToken builds the client from configuration, so the environment
	// has to point at the same test server.
	t.Setenv("BING_ADS_API_BASE_URL", srv.URL)
	t.Setenv("BING_ADS_ACCOUNT_ID", "123456")

	c := newTestBingClient(t, srv)
	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatal(err)
	}
	applier, platform, err := applierForToken(t.Context(), preview.Token)
	if err != nil {
		t.Fatalf("applierForToken: %v", err)
	}
	if platform != bingPlatformName {
		t.Errorf("platform = %q", platform)
	}
	if _, ok := applier.(*BingClient); !ok {
		t.Errorf("applier = %T, want a Bing client", applier)
	}
}

func TestBingApplyMutation_RejectsAnUnknownRoute(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{"/": `{}`})
	defer srv.Close()
	c := newTestBingClient(t, srv)

	_, err := c.applyMutation(t.Context(), &PendingMutation{Dispatch: "not_a_route"})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("an unrecognized route must not reach the API: %v", err)
	}
}

func TestBingCampaignOperations_RejectsCorruptPayloads(t *testing.T) {
	if _, err := bingCampaignOperations(nil); err == nil {
		t.Error("an empty operation list is corrupt")
	}
	if _, err := bingCampaignOperations([]any{"not an object"}); err == nil {
		t.Error("a non-object operation is corrupt")
	}
	if _, err := bingCampaignOperations([]any{map[string]any{"DailyBudget": 1.0}}); err == nil {
		t.Error("a campaign update with no Id would be applied to nothing")
	}
}

func TestRunBingBudgetSet_ConfirmRevalidatesTheSpendCap(t *testing.T) {
	useTempState(t)
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatal(err)
	}
	// Tightening the cap between preview and confirm must be enforced on the
	// per-tool confirm path too, not only through `ads confirm`.
	t.Setenv("GOOGLE_ADS_MAX_DAILY_BUDGET", "10")
	if _, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30, Confirm: preview.Token}); err == nil {
		t.Fatal("a staged budget above the current cap must be refused at confirm time")
	}
	if *update != nil {
		t.Error("nothing should have been written")
	}
}

func TestRunBingReportFetch_KeepsTheHandleWhenThePollItselfFails(t *testing.T) {
	bingThrottleBaseDelay = time.Millisecond
	t.Cleanup(func() { bingThrottleBaseDelay = 2 * time.Second })
	useTempState(t)

	// The poll is throttled, not the report: the report is still generating
	// server-side, so the handle has to survive. Deleting it would force a
	// resubmission, which counts against the in-flight report limit that
	// probably caused the throttle.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"Errors":[{"Code":117,"ErrorCode":"CallRateExceeded","Message":"slow down"}]}`))
	}))
	defer srv.Close()
	c := newTestBingClient(t, srv)

	id, _ := newBingJobID()
	if err := saveBingReportJob(&bingReportJob{ID: id, ReportRequestID: "req-1", AccountID: "123456", SubmittedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := runBingReportFetch(t.Context(), c, BingReportFetchArgs{Job: id}); err == nil {
		t.Fatal("expected the throttle to surface")
	}
	if _, err := loadBingReportJob(id); err != nil {
		t.Errorf("a transient poll failure must not consume the handle: %v", err)
	}
}

func TestRunBingReportFetch_DropsTheHandleWhenTheServiceFailedTheReport(t *testing.T) {
	useTempState(t)

	// The service finished the report and failed it: this handle can never
	// produce rows, so keeping it would only offer to fetch it forever.
	srv := bingJSONServer(t, map[string]string{
		"/GenerateReport/Poll": `{"ReportRequestStatus":{"Status":"Error"}}`,
	})
	defer srv.Close()
	c := newTestBingClient(t, srv)

	id, _ := newBingJobID()
	if err := saveBingReportJob(&bingReportJob{ID: id, ReportRequestID: "req-1", AccountID: "123456", SubmittedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := runBingReportFetch(t.Context(), c, BingReportFetchArgs{Job: id}); err == nil {
		t.Fatal("expected the failed report to surface")
	}
	if _, err := loadBingReportJob(id); err == nil {
		t.Error("a report the service failed should not leave a fetchable handle")
	}
}

func TestRunBingReportTool_KeepsAHandleWhenTheFirstPollFails(t *testing.T) {
	bingThrottleBaseDelay = time.Millisecond
	t.Cleanup(func() { bingThrottleBaseDelay = 2 * time.Second })
	useTempState(t)

	// Submit succeeds, so the report is queued and this process holds the only
	// copy of its request ID. A throttled poll must not throw that away.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/GenerateReport/Submit") {
			_, _ = w.Write([]byte(`{"ReportRequestId":"req-1"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"Errors":[{"Code":117,"ErrorCode":"CallRateExceeded","Message":"slow down"}]}`))
	}))
	defer srv.Close()
	c := newTestBingClient(t, srv)

	_, err := runBingReportTool(t.Context(), c, bingCampaignPerformancePreset, BingPerformanceArgs{})
	if err == nil {
		t.Fatal("expected the throttle to surface")
	}
	// The error has to hand back the handle, or the queued report is stranded
	// until it expires and the user resubmits.
	if !strings.Contains(err.Error(), "report fetch job_") {
		t.Errorf("error should name the handle that collects the queued report: %v", err)
	}
}

func TestApplyConfirmed_RejectsAnotherPlatformsToken(t *testing.T) {
	useTempState(t)
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	bing := newTestBingClient(t, srv)

	// Both platforms name this tool set_campaign_budget, so the tool binding
	// alone would let a Google token through to Bing's applier.
	staged, err := stageMutation("set_campaign_budget", "1234567890", "Google budget change", []any{
		map[string]any{"campaignBudgetOperation": map[string]any{"update": map[string]any{"amountMicros": 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyConfirmed(t.Context(), bing, "set_campaign_budget", staged.Token)
	if err == nil {
		t.Fatal("a Google token must not be applied through Bing's API")
	}
	for _, want := range []string{"google", "bing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name both platforms (missing %q): %v", want, err)
		}
	}
	if *update != nil {
		t.Error("nothing should have been written")
	}
}

func TestApplyConfirmed_AcceptsItsOwnPlatformsToken(t *testing.T) {
	useTempState(t)
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatal(err)
	}
	res, err := applyConfirmed(t.Context(), c, "set_campaign_budget", preview.Token)
	if err != nil {
		t.Fatalf("a platform's own token must still apply: %v", err)
	}
	if !res.Applied || *update == nil {
		t.Error("the write should have been applied")
	}
}

func TestBingCampaign_SharedBudgetIsReadByValue(t *testing.T) {
	// Microsoft documents both BudgetId and BidStrategyId the same way: not
	// null *and greater than zero* means the campaign uses the shared entity,
	// and "0" is what you write to detach it. Presence is not the test.
	zero, blank, real := "0", "", "900"
	tests := []struct {
		name string
		id   *string
		want string
	}{
		{"absent", nil, ""},
		{"detached", &zero, ""},
		{"blank", &blank, ""},
		{"shared", &real, "900"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := BingCampaign{BudgetID: tc.id, BidStrategyID: tc.id}
			if got := c.sharedBudgetID(); got != tc.want {
				t.Errorf("sharedBudgetID() = %q, want %q", got, tc.want)
			}
			if got := c.portfolioBidStrategyID(); got != tc.want {
				t.Errorf("portfolioBidStrategyID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunBingBudgetSet_DetachedSharedBudgetIsWritable(t *testing.T) {
	useTempState(t)
	// BudgetId "0" means the campaign was moved off its shared budget and owns
	// its DailyBudget again — the API accepts this write.
	srv, update := bingBudgetServer(t, `{"Id":"1","Name":"Brand","DailyBudget":25.0,"BudgetId":"0"}`, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	preview, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30})
	if err != nil {
		t.Fatalf("a detached campaign must be writable: %v", err)
	}
	if _, err := runBingBudgetSet(t.Context(), c, BingBudgetSetArgs{CampaignID: "1", DailyBudget: 30, Confirm: preview.Token}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if *update == nil {
		t.Error("the write should have been applied")
	}
}

func TestRunBingCampaigns_DetachedSharedBudgetIsNotReported(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{
		"/Campaigns/QueryByAccountId": `{"Campaigns":[{"Id":"1","Name":"Brand","BudgetId":"0","BidStrategyId":"0"}]}`,
	})
	defer srv.Close()

	res, err := runBingCampaigns(t.Context(), newTestBingClient(t, srv), BingCampaignsArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Campaigns[0].SharedBudgetID != "" {
		t.Errorf("shared_budget_id = %q, want empty for a detached campaign", res.Campaigns[0].SharedBudgetID)
	}
	if res.Campaigns[0].BidStrategyID != "" {
		t.Errorf("bid_strategy_id = %q, want empty for a campaign with its own strategy", res.Campaigns[0].BidStrategyID)
	}
}

func TestBingAllCampaignTypes_CoversTheWholeValueSet(t *testing.T) {
	// The v13 CampaignType value set. A type missing from the filter is
	// invisible to `bing_campaigns` and, because GetCampaign filters that same
	// list, unwritable by bing_set_campaign_budget.
	for _, want := range []string{"Search", "Shopping", "DynamicSearchAds", "Audience", "Hotel", "PerformanceMax", "App"} {
		if !slices.Contains(strings.Fields(bingAllCampaignTypes), want) {
			t.Errorf("campaign type %q is missing from the filter (%q)", want, bingAllCampaignTypes)
		}
	}
}

func TestBingClient_DiscoversTheManagerAccountWhenNoneIsConfigured(t *testing.T) {
	var customerHeaders []string
	var accountQueries int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Account/Query"):
			accountQueries++
			// Customer Management takes neither header, so the lookup cannot
			// need the value it is looking up.
			if got := r.Header.Get("CustomerId"); got != "" {
				t.Errorf("the discovery call must not send CustomerId, got %q", got)
			}
			_, _ = w.Write([]byte(`{"Account":{"Id":"123456","ParentCustomerId":"555"}}`))
		default:
			customerHeaders = append(customerHeaders, r.Header.Get("CustomerId"))
			_, _ = w.Write([]byte(`{"Campaigns":[]}`))
		}
	}))
	defer srv.Close()
	c := newTestBingClientWith(t, srv, &BingConfig{DefaultAccountID: "123456"}) // no CustomerID

	for range 3 {
		if _, err := c.ListCampaigns(t.Context(), "123456"); err != nil {
			t.Fatalf("ListCampaigns: %v", err)
		}
	}
	// Microsoft documents CustomerId as required for most operations, so the
	// documented setup — sign in, set an account — has to produce it.
	for i, got := range customerHeaders {
		if got != "555" {
			t.Errorf("call %d sent CustomerId %q, want the account's parent customer", i+1, got)
		}
	}
	if accountQueries != 1 {
		t.Errorf("discovery ran %d times, want once per client", accountQueries)
	}
}

func TestBingClient_ConfiguredManagerAccountSkipsDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Account/Query") {
			t.Error("a configured manager account must not be looked up")
		}
		w.Header().Set("Content-Type", "application/json")
		if got := r.Header.Get("CustomerId"); got != "777" {
			t.Errorf("CustomerId = %q, want the configured value", got)
		}
		_, _ = w.Write([]byte(`{"Campaigns":[]}`))
	}))
	defer srv.Close()
	if _, err := newTestBingClient(t, srv).ListCampaigns(t.Context(), "123456"); err != nil {
		t.Fatal(err)
	}
}

func TestBingClient_ManagerDiscoveryFailureIsNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/Account/Query") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"Errors":[{"Code":106,"ErrorCode":"UserIsNotAuthorized"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"Campaigns":[]}`))
	}))
	defer srv.Close()
	captureWarnings(t)
	c := newTestBingClientWith(t, srv, &BingConfig{DefaultAccountID: "123456"})

	// The request still goes out, so the API's own error is what the user sees
	// rather than one invented here.
	if _, err := c.ListCampaigns(t.Context(), "123456"); err != nil {
		t.Errorf("a failed discovery must not fail the call: %v", err)
	}
}

func TestRunBingReportTool_KeepsAHandleWhenTheDownloadFails(t *testing.T) {
	bingReportPollInterval = time.Millisecond
	t.Cleanup(func() { bingReportPollInterval = 2 * time.Second })
	useTempState(t)

	// The report completes and the download fails: it stays downloadable, so
	// the handle must survive rather than forcing a whole new report.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/GenerateReport/Submit"):
			_, _ = w.Write([]byte(`{"ReportRequestId":"req-1"}`))
		case strings.HasSuffix(r.URL.Path, "/GenerateReport/Poll"):
			_, _ = w.Write([]byte(`{"ReportRequestStatus":{"Status":"Success","ReportDownloadUrl":"` + srv.URL + `/download"}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := newTestBingClient(t, srv)

	_, err := runBingReportTool(t.Context(), c, bingCampaignPerformancePreset, BingPerformanceArgs{})
	if err == nil {
		t.Fatal("expected the download failure to surface")
	}
	if !strings.Contains(err.Error(), "report fetch job_") {
		t.Errorf("error should hand back a handle for the finished report: %v", err)
	}
}

func TestBingDownloadClient_HonoursProxyConfiguration(t *testing.T) {
	// Submit and poll go through the shared client's default transport, which
	// reads HTTP(S)_PROXY. A download built on a bare http.Transport would not,
	// so behind a mandatory proxy only the download would fail — the one step
	// that runs after the report has already been generated and paid for.
	//
	// The proxy function is asserted rather than exercised: net/http resolves
	// the environment once per process, so setting it from a test that runs
	// after any other HTTP call has no effect.
	transport, ok := bingDownloadClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", bingDownloadClient().Transport)
	}
	if transport.Proxy == nil {
		t.Error("the download transport has no Proxy function — it would bypass a configured proxy")
	}
	if transport.ResponseHeaderTimeout == 0 {
		t.Error("the download transport must still bound the response-header wait")
	}
	// Cloned from the default, not hand-built: that is what keeps the rest of
	// the environment's HTTP configuration in place.
	if def := http.DefaultTransport.(*http.Transport); transport.TLSHandshakeTimeout != def.TLSHandshakeTimeout {
		t.Error("the download transport should start from http.DefaultTransport")
	}
}

func TestBingLogin_EnvironmentFlagIsRegistered(t *testing.T) {
	// The command's Long help tells users to pass it; following your own help
	// should not produce "unknown flag".
	flag := bingLoginCmd.Flags().Lookup("environment")
	if flag == nil {
		t.Fatal("`ads login bing --environment` is advertised in the help but not registered")
	}
	if !strings.Contains(bingLoginCmd.Long, "--environment") {
		t.Error("the help no longer mentions the flag this test guards")
	}
}

func TestBingConfig_ApplyEnvironment(t *testing.T) {
	// Selecting the sandbox has to bring its developer token with it, however
	// the environment arrived — a flag that left the token empty would fail as
	// a missing credential.
	cfg := &BingConfig{}
	cfg.applyEnvironment("Sandbox")
	if cfg.Environment != bingEnvSandbox || cfg.DeveloperToken != bingSandboxDeveloperToken {
		t.Errorf("sandbox: %+v", cfg)
	}
	if !cfg.knownEnvironment() {
		t.Error("sandbox should be a known environment")
	}
	bogus := &BingConfig{}
	bogus.applyEnvironment("staging")
	if bogus.knownEnvironment() {
		t.Error("an unknown environment must not pass validation")
	}
	// An explicitly set token is never replaced by the environment's default.
	kept := &BingConfig{DeveloperToken: "mine"}
	kept.applyEnvironment("sandbox")
	if kept.DeveloperToken != "mine" {
		t.Errorf("developer token = %q, want the configured one kept", kept.DeveloperToken)
	}
}
