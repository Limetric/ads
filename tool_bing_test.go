package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	// Midday UTC is the same calendar day in Pacific, so these cases are about
	// the range arithmetic; the zone boundary itself is covered separately by
	// TestBingReportRange_ResolvesYesterdayInTheReportingTimeZone.
	now := time.Date(2026, 8, 10, 18, 0, 0, 0, time.UTC)
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
	// In the sandbox the token is a constant, not a preference: no other value
	// works there. (This assertion previously required the configured token to
	// be kept, which is the rule being reversed.)
	captureWarnings(t)
	sandbox := &BingConfig{DeveloperToken: "a-production-token"}
	sandbox.applyEnvironment("sandbox")
	if sandbox.DeveloperToken != bingSandboxDeveloperToken {
		t.Errorf("developer token = %q, want the universal sandbox token", sandbox.DeveloperToken)
	}
	// Production keeps whatever was configured — there is no constant there.
	kept := &BingConfig{DeveloperToken: "mine"}
	kept.applyEnvironment("production")
	if kept.DeveloperToken != "mine" {
		t.Errorf("developer token = %q, want the configured one kept in production", kept.DeveloperToken)
	}
}

func TestBingClient_DiscoversAManagerPerAccount(t *testing.T) {
	var mu sync.Mutex
	sent := map[string]string{}
	lookups := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		account := r.Header.Get("CustomerAccountId")
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, bingAccountQueryRoute) {
			lookups[account]++
			// Each account sits under its own manager — the ordinary agency
			// shape, where every client is its own customer.
			_, _ = w.Write([]byte(`{"Account":{"Id":"` + account + `","ParentCustomerId":"M` + account + `"}}`))
			return
		}
		sent[account] = r.Header.Get("CustomerId")
		_, _ = w.Write([]byte(`{"Campaigns":[]}`))
	}))
	defer srv.Close()
	c := newTestBingClientWith(t, srv, &BingConfig{DefaultAccountID: "1"}) // no CustomerID

	for _, account := range []string{"1", "2", "1", "2"} {
		if _, err := c.ListCampaigns(t.Context(), account); err != nil {
			t.Fatalf("ListCampaigns(%s): %v", account, err)
		}
	}
	// One client serves every account an MCP caller names. Caching the first
	// account's manager and sending it for the second is a mismatched pair
	// Microsoft rejects — worse than the omitted header this replaced.
	if sent["1"] != "M1" || sent["2"] != "M2" {
		t.Errorf("CustomerId per account = %v, want each account's own manager", sent)
	}
	if lookups["1"] != 1 || lookups["2"] != 1 {
		t.Errorf("lookups = %v, want one per account", lookups)
	}
}

func TestBingClient_ManagerDiscoveryRetriesAfterAFailure(t *testing.T) {
	var mu sync.Mutex
	var attempts int
	var lastCustomer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, bingAccountQueryRoute) {
			attempts++
			if attempts == 1 {
				// A failure on the very first tool call. 403 rather than 5xx:
				// a 5xx is retried inside the client, so it would never reach
				// the caching decision this test is about.
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"OperationErrors":[{"Code":106,"ErrorCode":"UserIsNotAuthorized"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"Account":{"Id":"123456","ParentCustomerId":"555"}}`))
			return
		}
		lastCustomer = r.Header.Get("CustomerId")
		_, _ = w.Write([]byte(`{"Campaigns":[]}`))
	}))
	defer srv.Close()
	captureWarnings(t)
	c := newTestBingClientWith(t, srv, &BingConfig{DefaultAccountID: "123456"})

	if _, err := c.ListCampaigns(t.Context(), "123456"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.ListCampaigns(t.Context(), "123456"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	// A failed lookup must not decide, for the life of the process, that this
	// account has no manager — otherwise the header Microsoft requires goes
	// missing based on the timing of one early request.
	if lastCustomer != "555" {
		t.Errorf("CustomerId after a recovered discovery = %q, want 555", lastCustomer)
	}
}

func TestSaveBingCredentials_PersistsAnEnvironmentSwitch(t *testing.T) {
	useTempState(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := "[bing]\ndeveloper_token = \"a-production-token\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	// Moving back to production has to be written down. mergeBingConfigValues
	// only ever sets keys, so an omitted default would leave the old sandbox
	// value in place and point the new sign-in at the wrong environment.
	if err := saveBingCredentials(path, &BingConfig{ClientID: "cid", Environment: bingEnvProduction}, "rt-1"); err != nil {
		t.Fatalf("saveBingCredentials: %v", err)
	}
	cfg, err := loadBingConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != bingEnvProduction {
		t.Errorf("environment = %q, want the switch persisted", cfg.Environment)
	}
}

func TestBingReportRange_ResolvesYesterdayInTheReportingTimeZone(t *testing.T) {
	// Midnight UTC is still the previous afternoon in Pacific. The reporting
	// service interprets the range in its own zone, so a window computed from
	// the host's clock asks for a different one than the caller sees — and with
	// ReturnOnlyCompleteData false, the extra day comes back partial.
	utcMidnight := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	start, end := bingReportRange(utcMidnight, 7)
	if got := end.Format(time.DateOnly); got != "2026-08-08" {
		t.Errorf("end = %s, want 2026-08-08 (yesterday in Pacific, not in UTC)", got)
	}
	if got := start.Format(time.DateOnly); got != "2026-08-02" {
		t.Errorf("start = %s, want 2026-08-02", got)
	}
}

func TestBingReportSpec_NamesTheReportTimeZone(t *testing.T) {
	spec := bingReportSpec{
		Preset:    bingCampaignPerformancePreset,
		AccountID: "123456",
		Start:     time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	}
	body, err := spec.requestBody()
	if err != nil {
		t.Fatal(err)
	}
	request, _ := body["ReportRequest"].(map[string]any)
	period, _ := request["Time"].(map[string]any)
	// Sent rather than left to the documented default, so the zone the dates
	// were computed in and the zone they are read in cannot drift apart.
	if period["ReportTimeZone"] != bingReportTimeZone {
		t.Errorf("ReportTimeZone = %v, want %q", period["ReportTimeZone"], bingReportTimeZone)
	}
}

func TestBingReportLocation_IsAvailable(t *testing.T) {
	// time/tzdata is embedded so this resolves on a host with no zoneinfo
	// database; the UTC fallback would silently shift the window by a day.
	if loc := bingReportLocation(); loc == time.UTC {
		t.Error("the reporting time zone fell back to UTC — is time/tzdata still imported?")
	}
}

func TestBingReportSpec_EncodesAccountIdsAsStrings(t *testing.T) {
	spec := bingReportSpec{Preset: bingCampaignPerformancePreset, AccountID: "123456"}
	body, err := spec.requestBody()
	if err != nil {
		t.Fatal(err)
	}
	request, _ := body["ReportRequest"].(map[string]any)
	scope, _ := request["Scope"].(map[string]any)
	// This API renders every long as a JSON string — Campaign.Id, BudgetId,
	// AccountInfo.Id — and the report scope is documented no differently.
	// Numbers here can have the service reject every report at submission.
	ids, ok := scope["AccountIds"].([]string)
	if !ok {
		t.Fatalf("AccountIds = %#v, want []string", scope["AccountIds"])
	}
	if len(ids) != 1 || ids[0] != "123456" {
		t.Errorf("AccountIds = %v", ids)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"AccountIds":["123456"]`) {
		t.Errorf("encoded scope = %s", encoded)
	}
	// A bad account ID is still refused before anything is sent.
	if _, err := (bingReportSpec{Preset: bingCampaignPerformancePreset, AccountID: "12a"}).requestBody(); err == nil {
		t.Error("a non-numeric account ID must be rejected")
	}
}

func TestAwaitBingReport_DeadlineBoundsAnInFlightPoll(t *testing.T) {
	// A poll that hangs must not outlast the deadline: the whole point of the
	// bound is to hand back a job handle before an MCP host gives up.
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/GenerateReport/Poll") {
			<-blocked // never answers
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	defer close(blocked)
	c := newTestBingClient(t, srv)

	start := time.Now()
	_, ready, err := awaitBingReport(t.Context(), c, "123456", "req-1", 100*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("an unfinished report is not an error: %v", err)
	}
	if ready {
		t.Error("ready = true for a report that never answered")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s — the deadline did not bound the in-flight poll", elapsed)
	}
}

func TestAwaitBingReport_CallerCancellationIsStillAnError(t *testing.T) {
	// The caller giving up is not the same as ads' own deadline elapsing, and
	// must not be reported as "still running".
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked)
	c := newTestBingClient(t, srv)

	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, _, err := awaitBingReport(ctx, c, "123456", "req-1", time.Minute); err == nil {
		t.Error("a cancelled caller should surface its cancellation")
	}
}

func TestSaveBingCredentials_NeverOverwritesTheDeveloperToken(t *testing.T) {
	useTempState(t)
	captureWarnings(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := "[bing]\ndeveloper_token = \"a-production-token\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	// The round trip: a production token in the config, a visit to the sandbox,
	// and back. The credential has to come through untouched — it is stored in
	// exactly one place, and a sign-in that overwrote it would destroy it.
	cfg, err := loadBingConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClientID = "cid"
	cfg.applyEnvironment("sandbox")
	if cfg.DeveloperToken != bingSandboxDeveloperToken {
		t.Fatalf("in-process token = %q, want the sandbox one", cfg.DeveloperToken)
	}
	if err := saveBingCredentials(path, cfg, "rt-1"); err != nil {
		t.Fatalf("saveBingCredentials: %v", err)
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "a-production-token") {
		t.Fatalf("the production token was destroyed by a sandbox sign-in:\n%s", onDisk)
	}
	if strings.Contains(string(onDisk), bingSandboxDeveloperToken) {
		t.Errorf("the sandbox token was written to the config file:\n%s", onDisk)
	}

	// Switching back, as the commands actually run it: load (the sandbox
	// constant applies in memory), select production, save. The developer token
	// is not used during sign-in, and the file is what the next command reads.
	back, err := loadBingConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	back.ClientID = "cid"
	back.applyEnvironment("production")
	if err := saveBingCredentials(path, back, "rt-2"); err != nil {
		t.Fatalf("saveBingCredentials (switch back): %v", err)
	}
	final, err := loadBingConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if final.Environment != bingEnvProduction {
		t.Errorf("environment after the round trip = %q", final.Environment)
	}
	if final.DeveloperToken != "a-production-token" {
		t.Errorf("token after the round trip = %q, want the user's own credential back", final.DeveloperToken)
	}
}

func TestBingDeveloperTokenReport_FlagsTheSandboxTokenInProduction(t *testing.T) {
	// The reachable mismatch: a sandbox token configured against production.
	// The reverse cannot survive load, since the sandbox applies its own.
	got := bingDeveloperTokenReport(styles{}, &BingConfig{DeveloperToken: bingSandboxDeveloperToken, Environment: bingEnvProduction})
	if !strings.Contains(got, "SANDBOX") {
		t.Errorf("report = %q, want it to flag the mismatch", got)
	}
}
