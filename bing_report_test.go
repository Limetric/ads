package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// zipReport builds a downloadable report archive from CSV text.
func zipReport(t *testing.T, name, csv string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(csv)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBingReportRange_EndsYesterday(t *testing.T) {
	now := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	start, end := bingReportRange(now, 30)
	// Today is always partial; including it would quietly report a fraction of
	// a day as a full one.
	if end.Format(time.DateOnly) != "2026-08-09" {
		t.Errorf("end = %s, want yesterday", end.Format(time.DateOnly))
	}
	if start.Format(time.DateOnly) != "2026-07-11" {
		t.Errorf("start = %s, want 30 complete days ending yesterday", start.Format(time.DateOnly))
	}
	// A one-day window is yesterday alone, not an empty range.
	start, end = bingReportRange(now, 1)
	if start != end {
		t.Errorf("one day: %s..%s", start.Format(time.DateOnly), end.Format(time.DateOnly))
	}
}

func TestBingReportSpec_RequestBody(t *testing.T) {
	spec := bingReportSpec{
		Preset:    bingCampaignPerformancePreset,
		AccountID: "123456",
		Start:     time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	}
	body, err := spec.requestBody()
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	request, _ := body["ReportRequest"].(map[string]any)
	if request == nil {
		t.Fatal("request body has no ReportRequest")
	}
	// The discriminator is not optional: the service has no concrete
	// ReportRequest type, only derived ones.
	if request["Type"] != "CampaignPerformanceReportRequest" {
		t.Errorf("Type = %v", request["Type"])
	}
	if request["Aggregation"] != "Summary" {
		t.Errorf("Aggregation = %v, want Summary (no TimePeriod column is requested)", request["Aggregation"])
	}
	// Header and footer are prose wrapped around the data; column headers are
	// how the values are labelled and must stay.
	if request["ExcludeReportHeader"] != true || request["ExcludeReportFooter"] != true {
		t.Error("report header and footer should be excluded")
	}
	if request["ExcludeColumnHeaders"] != false {
		t.Error("column headers must be kept — they key every row")
	}
	scope, _ := request["Scope"].(map[string]any)
	ids, _ := scope["AccountIds"].([]int64)
	if len(ids) != 1 || ids[0] != 123456 {
		t.Errorf("Scope.AccountIds = %v, want a numeric account ID", scope["AccountIds"])
	}
	period, _ := request["Time"].(map[string]any)
	start, _ := period["CustomDateRangeStart"].(map[string]any)
	if start["Year"] != 2026 || start["Month"] != 7 || start["Day"] != 11 {
		t.Errorf("CustomDateRangeStart = %v", start)
	}
}

func TestBingReportSpec_ColumnsOverridePreset(t *testing.T) {
	spec := bingReportSpec{Preset: bingCampaignPerformancePreset, AccountID: "1"}
	if len(spec.columns()) != len(bingCampaignPerformancePreset.Columns) {
		t.Error("with no override the preset columns are used")
	}
	spec.Columns = []string{"CampaignName", "Spend"}
	if got := spec.columns(); len(got) != 2 || got[1] != "Spend" {
		t.Errorf("columns = %v, want the override", got)
	}
}

func TestParseBingReportZip(t *testing.T) {
	// Real downloads are UTF-8 with a BOM; left in place it becomes part of the
	// first column name and every lookup of it misses.
	csv := "\xef\xbb\xbfCampaignName,Spend,Conversions\nBrand,12.34,3\n\nGeneric,0.00,--\n"
	columns, rows, err := parseBingReportZip(zipReport(t, "report.csv", csv))
	if err != nil {
		t.Fatalf("parseBingReportZip: %v", err)
	}
	if columns[0] != "CampaignName" {
		t.Errorf("columns[0] = %q, want the BOM stripped", columns[0])
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want the blank line skipped", rows)
	}
	encoded, err := bingReportRows(columns, rows)
	if err != nil {
		t.Fatalf("bingReportRows: %v", err)
	}
	var first map[string]string
	if err := json.Unmarshal(encoded[0], &first); err != nil {
		t.Fatal(err)
	}
	if first["Spend"] != "12.34" {
		t.Errorf("Spend = %q", first["Spend"])
	}
	var second map[string]string
	_ = json.Unmarshal(encoded[1], &second)
	// "--" means "not computed". Coercing it to a number would report a missing
	// figure as zero.
	if second["Conversions"] != "--" {
		t.Errorf("Conversions = %q, want the sentinel preserved", second["Conversions"])
	}
}

func TestParseBingReportZip_Empty(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseBingReportZip(buf.Bytes()); err == nil {
		t.Error("an archive with no entries should be an error, not silent emptiness")
	}
	if _, _, err := parseBingReportZip([]byte("not a zip")); err == nil {
		t.Error("a non-archive download should be an error")
	}
}

// bingReportServer stands in for the reporting service: it accepts a submit,
// answers `pending` for pendingPolls polls, then serves a report.
func bingReportServer(t *testing.T, pendingPolls int, csv string) *httptest.Server {
	t.Helper()
	polls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/GenerateReport/Submit"):
			_, _ = w.Write([]byte(`{"ReportRequestId":"req-1"}`))
		case strings.HasSuffix(r.URL.Path, "/GenerateReport/Poll"):
			polls++
			if polls <= pendingPolls {
				_, _ = w.Write([]byte(`{"ReportRequestStatus":{"Status":"Pending"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ReportRequestStatus":{"Status":"Success","ReportDownloadUrl":"` + srv.URL + `/download"}}`))
		case strings.HasSuffix(r.URL.Path, "/download"):
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipReport(t, "report.csv", csv))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	return srv
}

func TestAwaitBingReport_ReturnsWhenReady(t *testing.T) {
	bingReportPollInterval = time.Millisecond
	t.Cleanup(func() { bingReportPollInterval = 2 * time.Second })

	srv := bingReportServer(t, 2, "CampaignName,Spend\nBrand,1.00\n")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	status, ready, err := awaitBingReport(t.Context(), c, "123456", "req-1", time.Second)
	if err != nil || !ready {
		t.Fatalf("awaitBingReport = (%+v, %v, %v)", status, ready, err)
	}
	if status.DownloadURL == "" {
		t.Error("a ready report should carry a download URL")
	}
}

func TestAwaitBingReport_DeadlineHandsBack(t *testing.T) {
	bingReportPollInterval = time.Millisecond
	t.Cleanup(func() { bingReportPollInterval = 2 * time.Second })

	// Never finishes: the deadline must return "not ready" rather than an
	// error, because the report is still perfectly valid — just slow.
	srv := bingReportServer(t, 1000, "")
	defer srv.Close()
	c := newTestBingClient(t, srv)

	_, ready, err := awaitBingReport(t.Context(), c, "123456", "req-1", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("a slow report is not an error: %v", err)
	}
	if ready {
		t.Error("ready = true for a report that never completed")
	}
}

func TestAwaitBingReport_ServiceFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ReportRequestStatus":{"Status":"Error"}}`))
	}))
	defer srv.Close()
	c := newTestBingClient(t, srv)

	if _, _, err := awaitBingReport(t.Context(), c, "123456", "req-1", time.Second); err == nil {
		t.Error("a report the service failed to generate must surface as an error")
	}
}

func TestFetchBingReportRows_EmptyURLMeansNoData(t *testing.T) {
	srv := bingJSONServer(t, map[string]string{"/": `{}`})
	defer srv.Close()
	c := newTestBingClient(t, srv)

	// Documented behaviour: a successful report with no matching data comes
	// back with an empty URL. That is an answer, not a failure.
	columns, rows, err := fetchBingReportRows(t.Context(), c, bingReportStatus{Status: bingReportSuccess})
	if err != nil {
		t.Fatalf("fetchBingReportRows: %v", err)
	}
	if len(columns) != 0 || len(rows) != 0 {
		t.Errorf("expected an empty result, got %v / %v", columns, rows)
	}
}

func TestBingReportJobStore_RoundTrip(t *testing.T) {
	useTempState(t)

	id, err := newBingJobID()
	if err != nil {
		t.Fatal(err)
	}
	if !validBingJobID(id) {
		t.Fatalf("generated handle %q fails its own validator", id)
	}
	job := &bingReportJob{
		ID: id, Tool: "campaign_performance", ReportRequestID: "req-1",
		AccountID: "123456", DateRange: "2026-07-11 to 2026-08-09",
		SubmittedAt: time.Now().UTC(),
	}
	if err := saveBingReportJob(job); err != nil {
		t.Fatalf("saveBingReportJob: %v", err)
	}
	loaded, err := loadBingReportJob(id)
	if err != nil {
		t.Fatalf("loadBingReportJob: %v", err)
	}
	if loaded.ReportRequestID != "req-1" || loaded.AccountID != "123456" {
		t.Errorf("loaded = %+v", loaded)
	}
	deleteBingReportJob(id)
	if _, err := loadBingReportJob(id); err == nil {
		t.Error("a deleted handle should not load")
	}
}

func TestBingReportJobStore_RejectsMalformedHandles(t *testing.T) {
	useTempState(t)
	// The handle becomes part of a file path, so it is validated before it can
	// touch the filesystem — the rule confirm tokens follow.
	for _, bad := range []string{"", "job_", "job_XYZ12345", "../../etc/passwd", "job_0011223344"} {
		if validBingJobID(bad) {
			t.Errorf("validBingJobID(%q) = true", bad)
		}
		if _, err := loadBingReportJob(bad); err == nil {
			t.Errorf("loadBingReportJob(%q) should fail", bad)
		}
	}
}

func TestBingReportJobStore_ExpiredHandle(t *testing.T) {
	useTempState(t)
	id, err := newBingJobID()
	if err != nil {
		t.Fatal(err)
	}
	// Microsoft discards a queued report after a day, so a handle older than
	// that can only produce a confusing failure downstream.
	job := &bingReportJob{ID: id, ReportRequestID: "req-1", SubmittedAt: time.Now().Add(-25 * time.Hour)}
	if err := saveBingReportJob(job); err != nil {
		t.Fatal(err)
	}
	_, err = loadBingReportJob(id)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired handle error = %v", err)
	}
}
