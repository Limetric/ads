package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestCLI_BingCampaigns(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	srv := bingJSONServer(t, map[string]string{
		"/Campaigns/QueryByAccountId": `{"Campaigns":[{"Id":"1","Name":"Brand","Status":"Active","DailyBudget":25.5}]}`,
	})
	defer srv.Close()
	t.Setenv("BING_ADS_API_BASE_URL", srv.URL) // loopback → no OAuth, no credentials
	t.Setenv("BING_ADS_ACCOUNT_ID", "123456")

	out, err := runCLI(t, "bing", "campaigns")
	if err != nil {
		t.Fatalf("ads bing campaigns: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"name": "Brand"`) {
		t.Errorf("output:\n%s", out)
	}
}

func TestCLI_BingCampaignPerformanceTable(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	bingReportPollInterval = time.Millisecond
	t.Cleanup(func() { bingReportPollInterval = 2 * time.Second })

	srv := bingReportServer(t, 0, "CampaignName,Spend,Clicks\nBrand,12.34,7\n")
	defer srv.Close()
	t.Setenv("BING_ADS_API_BASE_URL", srv.URL)
	t.Setenv("BING_ADS_ACCOUNT_ID", "123456")

	out, err := runCLI(t, "bing", "campaign-performance", "--format", "table")
	if err != nil {
		t.Fatalf("ads bing campaign-performance: %v\n%s", err, out)
	}
	// Report rows render through the same --format path as the Google reads.
	for _, want := range []string{"CampaignName", "Brand", "12.34"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestCLI_BingReportFetchRoundTrip(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	bingReportPollInterval = time.Millisecond
	bingReportDeadline = 10 * time.Millisecond
	t.Cleanup(func() {
		bingReportPollInterval = 2 * time.Second
		bingReportDeadline = 45 * time.Second
	})

	// Pending until the test says otherwise: the metric command gives up and
	// hands back a handle, and a later `report fetch` collects the rows — which
	// is the whole point of persisting the handle across processes.
	var ready atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/GenerateReport/Submit"):
			_, _ = w.Write([]byte(`{"ReportRequestId":"req-1"}`))
		case strings.HasSuffix(r.URL.Path, "/GenerateReport/Poll"):
			if !ready.Load() {
				_, _ = w.Write([]byte(`{"ReportRequestStatus":{"Status":"Pending"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ReportRequestStatus":{"Status":"Success","ReportDownloadUrl":"` + srv.URL + `/download"}}`))
		case strings.HasSuffix(r.URL.Path, "/download"):
			_, _ = w.Write(zipReport(t, "report.csv", "CampaignName,Spend\nBrand,5.00\n"))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("BING_ADS_API_BASE_URL", srv.URL)
	t.Setenv("BING_ADS_ACCOUNT_ID", "123456")

	queued, err := runCLI(t, "bing", "campaign-performance")
	if err != nil {
		t.Fatalf("ads bing campaign-performance: %v\n%s", err, queued)
	}
	if !strings.Contains(queued, "report queued") {
		t.Fatalf("expected a queued report:\n%s", queued)
	}
	handle := extractJobHandle(t, queued)

	ready.Store(true)
	fetched, err := runCLI(t, "bing", "report", "fetch", handle)
	if err != nil {
		t.Fatalf("ads bing report fetch: %v\n%s", err, fetched)
	}
	if !strings.Contains(fetched, "Brand") || !strings.Contains(fetched, "5.00") {
		t.Errorf("fetch output:\n%s", fetched)
	}
}

// extractJobHandle pulls the `job_…` handle out of command output.
func extractJobHandle(t *testing.T, out string) string {
	t.Helper()
	for _, field := range strings.FieldsFunc(out, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if validBingJobID(field) {
			return field
		}
	}
	t.Fatalf("no job handle in output:\n%s", out)
	return ""
}

func TestCLI_BingSetAccount(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")

	out, err := runCLI(t, "config", "bing", "set-account", "123-456", "--config", path)
	if err != nil {
		t.Fatalf("ads config bing set-account: %v\n%s", err, out)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		t.Fatal(err)
	}
	table, _ := doc[bingPlatformName].(map[string]any)
	if table["default_account_id"] != "123456" {
		t.Errorf("config = %v, want the normalized ID under [bing]", doc)
	}

	if _, err := runCLI(t, "config", "bing", "set-account", "not-an-id", "--config", path); err == nil {
		t.Error("a non-numeric account ID must be rejected")
	}
}

func TestCLI_ConfigShowIncludesBothPlatforms(t *testing.T) {
	useTempState(t)
	clearAdsEnv(t)
	clearBingEnv(t)
	t.Setenv("BING_ADS_DEVELOPER_TOKEN", "must-not-be-printed")
	t.Setenv("BING_ADS_CLIENT_ID", "entra-app-id")

	out, err := runCLI(t, "config", "show")
	if err != nil {
		t.Fatalf("ads config show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Microsoft Advertising") {
		t.Errorf("config show should cover every platform:\n%s", out)
	}
	if strings.Contains(out, "must-not-be-printed") {
		t.Errorf("a secret leaked into config show:\n%s", out)
	}
	if !strings.Contains(out, "entra-app-id") {
		t.Errorf("the client ID is not a secret and should be shown:\n%s", out)
	}
}

func TestCLI_DoctorSkipsAnUnconfiguredPlatform(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	clearAdsEnv(t)
	// A Google-only user: Bing has nothing set up, and a plain `ads doctor`
	// must not report that as a broken setup.
	t.Setenv("GOOGLE_ADS_DEVELOPER_TOKEN", "devtok")
	t.Setenv("GOOGLE_ADS_CLIENT_ID", "cid")
	t.Setenv("GOOGLE_ADS_CLIENT_SECRET", "csec")

	out, _ := runCLI(t, "doctor", "--offline")
	if !strings.Contains(out, "skipped bing") {
		t.Errorf("an unconfigured platform should be skipped and said so:\n%s", out)
	}
	if strings.Contains(out, "Microsoft Advertising (bing) ===") {
		t.Errorf("bing should not have been checked:\n%s", out)
	}

	// Naming it explicitly checks it anyway: "I haven't set this up yet" is
	// exactly what the user is asking about.
	named, err := runCLI(t, "doctor", "bing", "--offline")
	if err == nil {
		t.Error("an explicitly named unconfigured platform should report NOT READY")
	}
	if !strings.Contains(named, "token store") {
		t.Errorf("`ads doctor bing` should report what it found:\n%s", named)
	}
}

func TestCLI_BingBudgetSetPreviews(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	var writes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			writes++
			_, _ = w.Write([]byte(`{"PartialErrors":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"Campaigns":[{"Id":"1","Name":"Brand","DailyBudget":25.0}]}`))
	}))
	defer srv.Close()
	t.Setenv("BING_ADS_API_BASE_URL", srv.URL)
	t.Setenv("BING_ADS_ACCOUNT_ID", "123456")

	out, err := runCLI(t, "bing", "budget", "set", "--campaign-id", "1", "--daily-budget", "30")
	if err != nil {
		t.Fatalf("ads bing budget set: %v\n%s", err, out)
	}
	if writes != 0 {
		t.Fatal("the first call must never mutate")
	}
	if !strings.Contains(out, "confirm_token") {
		t.Errorf("output should carry a confirm token:\n%s", out)
	}
	if !strings.Contains(out, "PREVIEW") {
		t.Errorf("output should carry the preview:\n%s", out)
	}
	// Sanity: the state directory really is the temp one, so the suite never
	// writes a pending mutation into a developer's real config.
	if dir, err := stateDir(); err != nil || !strings.HasPrefix(dir, os.TempDir()) {
		t.Errorf("state dir = %q (err %v), want a temp directory", dir, err)
	}
}
