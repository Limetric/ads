package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Microsoft has no query language. Every metric ads can report for Bing comes
// from the Reporting service, which is asynchronous: submit a report request,
// poll for it, download a ZIP, parse the CSV inside. That takes anywhere from a
// few seconds to minutes — longer than some MCP hosts allow a single tool call
// to run.
//
// So the handlers block, but only up to a deadline. A report that finishes in
// time returns rows and feels exactly like the Google tools; one that doesn't
// returns a job handle instead of timing out, and `ads bing report fetch <job>`
// picks it up later. The handle is persisted under stateDir() because the CLI
// and the MCP server are separate processes: a handle returned by one has to be
// fetchable by the other.

// bingReportDeadline bounds how long a metric tool waits for its report before
// handing back a job. It is a var so tests can shrink it.
var bingReportDeadline = 45 * time.Second

// bingReportPollInterval is the gap between polls. Reports are queued and then
// processed, so polling faster mostly spends the per-minute call budget that
// everything else on this platform also draws on.
var bingReportPollInterval = 2 * time.Second

// Report request status values returned by PollGenerateReport.
const (
	bingReportPending = "Pending"
	bingReportSuccess = "Success"
	bingReportError   = "Error"
)

// bingReportPreset is one of the fixed report shapes ads knows how to run.
// Bing's reporting surface is enormous; these are the three that correspond to
// tools Google already has, so an agent that knows one platform can read the
// other.
type bingReportPreset struct {
	// Tool is the tool name the preset belongs to, used in job records and
	// error messages.
	Tool string
	// Type is the ReportRequest discriminator — the derived request type. A
	// bare ReportRequest is rejected: the API has no such concrete object.
	Type string
	// Columns is the default column set. Report columns are a closed
	// enumeration per report type, so these are checked against the docs rather
	// than assembled at runtime; a caller who needs different ones passes them
	// explicitly.
	Columns []string
}

// The conversion columns deserve a note: Conversions and its derived
// AllConversions were deprecated in 2022 and now report zero for accounts that
// moved to the "Qualified" family, so the presets ask for ConversionsQualified.
// Anyone who needs the historical column can pass it via columns.
var (
	bingCampaignPerformancePreset = bingReportPreset{
		Tool: "campaign_performance",
		Type: "CampaignPerformanceReportRequest",
		Columns: []string{
			"CampaignId", "CampaignName", "CampaignStatus", "CurrencyCode",
			"Impressions", "Clicks", "Ctr", "AverageCpc", "Spend",
			"ConversionsQualified", "ConversionRate", "CostPerConversion",
		},
	}
	bingKeywordPerformancePreset = bingReportPreset{
		Tool: "keyword_performance",
		Type: "KeywordPerformanceReportRequest",
		Columns: []string{
			"CampaignName", "AdGroupName", "KeywordId", "Keyword", "KeywordStatus",
			"DeliveredMatchType", "CurrencyCode",
			"Impressions", "Clicks", "Ctr", "AverageCpc", "Spend",
			"ConversionsQualified", "ConversionRate", "CostPerConversion", "QualityScore",
		},
	}
	bingAdPerformancePreset = bingReportPreset{
		Tool: "ad_performance",
		Type: "AdPerformanceReportRequest",
		Columns: []string{
			"CampaignName", "AdGroupName", "AdId", "AdTitle", "AdType", "CurrencyCode",
			"Impressions", "Clicks", "Ctr", "AverageCpc", "Spend",
			"ConversionsQualified", "ConversionRate", "CostPerConversion",
		},
	}
)

// bingReportSpec is one concrete report to run: a preset, an account, a date
// range, and any column override.
type bingReportSpec struct {
	Preset    bingReportPreset
	AccountID string
	Columns   []string
	Start     time.Time
	End       time.Time
}

// columns returns the columns to request: the caller's, or the preset's.
func (s bingReportSpec) columns() []string {
	if len(s.Columns) > 0 {
		return s.Columns
	}
	return s.Preset.Columns
}

// dateRange renders the reported period for humans and job records.
func (s bingReportSpec) dateRange() string {
	return fmt.Sprintf("%s to %s", s.Start.Format(time.DateOnly), s.End.Format(time.DateOnly))
}

// bingReportRange returns the window a metric tool covers by default: the last
// `days` complete days, ending yesterday.
//
// Today is deliberately excluded. It is always partial, and a "last 30 days"
// number that silently includes a fraction of today is the kind of figure
// someone pastes into a report. Google's LAST_30_DAYS means the same thing.
func bingReportRange(now time.Time, days int) (start, end time.Time) {
	if days < 1 {
		days = 1
	}
	end = now.AddDate(0, 0, -1)
	start = end.AddDate(0, 0, -(days - 1))
	return start, end
}

// bingDateValue renders a date the way the Reporting service wants it: separate
// integer fields, no time zone.
func bingDateValue(t time.Time) map[string]any {
	return map[string]any{"Day": t.Day(), "Month": int(t.Month()), "Year": t.Year()}
}

// requestBody builds the SubmitGenerateReport payload.
func (s bingReportSpec) requestBody() (map[string]any, error) {
	accountID, err := parseInt64ID("account_id", s.AccountID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ReportRequest": map[string]any{
			"Type": s.Preset.Type,
			// The header and footer are prose — a title, a date range, a
			// copyright line — wrapped around the data. Excluding them leaves a
			// file whose first row is the column names, which is what a parser
			// wants. Column headers stay: they are how the values are labelled.
			"ExcludeReportHeader":  true,
			"ExcludeReportFooter":  true,
			"ExcludeColumnHeaders": false,
			"Format":               "Csv",
			"FormatVersion":        "2.0",
			"ReportName":           "ads " + s.Preset.Tool,
			// Asking for complete data only would fail the request outright
			// while Microsoft is still processing recent days; ads reports what
			// exists and says which days it covers.
			"ReturnOnlyCompleteData": false,
			// Summary totals each row over the whole period, with no TimePeriod
			// column — the same shape the Google metric tools return.
			"Aggregation": "Summary",
			"Columns":     s.columns(),
			"Scope":       map[string]any{"AccountIds": []int64{accountID}},
			"Time": map[string]any{
				"CustomDateRangeStart": bingDateValue(s.Start),
				"CustomDateRangeEnd":   bingDateValue(s.End),
			},
		},
	}, nil
}

// SubmitReport queues a report and returns its request ID. The ID is valid for
// one day; after that the report has to be requested again.
func (c *BingClient) SubmitReport(ctx context.Context, spec bingReportSpec) (string, error) {
	body, err := spec.requestBody()
	if err != nil {
		return "", err
	}
	var out struct {
		ReportRequestID string `json:"ReportRequestId"`
	}
	// Submitting is not a mutation, but it is rate-limited as its own resource
	// (error 207 counts reports in flight), so it does not get the read policy's
	// appetite for retrying 5xx.
	if err := c.postWrite(ctx, bingReportService, "GenerateReport/Submit", spec.AccountID, body, &out); err != nil {
		return "", err
	}
	if out.ReportRequestID == "" {
		return "", fmt.Errorf("report was submitted but the service returned no ReportRequestId")
	}
	return out.ReportRequestID, nil
}

// bingReportStatus is one poll's answer.
type bingReportStatus struct {
	Status      string
	DownloadURL string
}

// PollReport asks whether a submitted report is ready.
func (c *BingClient) PollReport(ctx context.Context, accountID, requestID string) (bingReportStatus, error) {
	var out struct {
		ReportRequestStatus struct {
			Status            string `json:"Status"`
			ReportDownloadUrl string `json:"ReportDownloadUrl"`
		} `json:"ReportRequestStatus"`
	}
	body := map[string]any{"ReportRequestId": requestID}
	if err := c.post(ctx, bingReportService, "GenerateReport/Poll", accountID, body, &out); err != nil {
		return bingReportStatus{}, err
	}
	return bingReportStatus{
		Status:      out.ReportRequestStatus.Status,
		DownloadURL: out.ReportRequestStatus.ReportDownloadUrl,
	}, nil
}

// reportGenerationError means the service itself finished the report and failed
// it. It is deliberately distinguishable from every other way a poll can fail,
// because it is the only one that says the queued report will never produce
// rows: a throttled poll, a 5xx, or a cancelled context all leave the report
// generating server-side, and a caller that treats those as terminal would
// throw away a handle that was about to work.
type reportGenerationError struct{ requestID string }

func (e *reportGenerationError) Error() string {
	return fmt.Sprintf("the reporting service failed to generate report %s — re-run the command to submit a new one", e.requestID)
}

// awaitBingReport polls until the report is ready, the deadline passes, or the
// service reports failure. ready is false when the deadline passed with the
// report still running — the caller then hands back a job.
func awaitBingReport(ctx context.Context, c *BingClient, accountID, requestID string, deadline time.Duration) (status bingReportStatus, ready bool, err error) {
	timeout := time.NewTimer(deadline)
	defer timeout.Stop()
	for {
		status, err = c.PollReport(ctx, accountID, requestID)
		if err != nil {
			return bingReportStatus{}, false, err
		}
		switch status.Status {
		case bingReportSuccess:
			return status, true, nil
		case bingReportError:
			return status, false, &reportGenerationError{requestID: requestID}
		}
		select {
		case <-time.After(bingReportPollInterval):
		case <-timeout.C:
			return status, false, nil
		case <-ctx.Done():
			return status, false, ctx.Err()
		}
	}
}

// bingReportMaxDownload caps a report download. Reports are per-account
// summaries, so anything approaching this is a bug or a hostile URL, and
// neither should be read into memory unbounded.
const bingReportMaxDownload = 256 << 20 // 256 MiB

// DownloadReport fetches a completed report and returns the ZIP bytes.
//
// The URL is pre-signed and short-lived, so it is fetched WITHOUT ads'
// credentials: sending the developer token and bearer token to a storage host
// that does not need them would spread them further than necessary.
func (c *BingClient) DownloadReport(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := bingDownloadClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download report: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, bingReportMaxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("read report download: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download report: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if len(data) > bingReportMaxDownload {
		return nil, fmt.Errorf("report download exceeds %d bytes — narrow the date range", bingReportMaxDownload)
	}
	return data, nil
}

// bingDownloadClient is the HTTP client report downloads use.
//
// A report can be large and slow; the shared client's whole-request timeout
// would abort a healthy transfer, so only the response-header wait is bounded
// and the body is bounded by the request context.
//
// The transport is cloned from the default rather than built fresh, because a
// bare http.Transport has no Proxy function. Behind a mandatory corporate proxy
// that asymmetry is invisible until it bites: submit and poll go through the
// shared client and work, and only the download fails, on a direct connection
// it was never going to get.
func bingDownloadClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: transport}
}

// parseBingReportZip reads the single CSV inside a downloaded report and
// returns its header row and data rows.
//
// Every report download is ZIP compressed regardless of the requested format,
// so the unzip is not conditional.
func parseBingReportZip(data []byte) (columns []string, rows [][]string, err error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("open report archive: %w", err)
	}
	for _, f := range archive.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("open %q in report archive: %w", f.Name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, bingReportMaxDownload))
		rc.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("read %q from report archive: %w", f.Name, err)
		}
		return parseBingReportCSV(content)
	}
	return nil, nil, fmt.Errorf("report archive is empty")
}

// parseBingReportCSV parses a report CSV: one header row, then data.
func parseBingReportCSV(content []byte) (columns []string, rows [][]string, err error) {
	// The file is UTF-8 with a byte-order mark, which would otherwise become
	// part of the first column's name and break every lookup of it.
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(content))
	// Rows are self-describing by header, and Microsoft has been known to emit
	// a trailing blank field; fixing the count per row is not worth a failed
	// report.
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("parse report CSV: %w", err)
	}
	for _, record := range records {
		if isBlankRecord(record) {
			continue
		}
		if columns == nil {
			columns = record
			continue
		}
		rows = append(rows, record)
	}
	if columns == nil {
		return nil, nil, fmt.Errorf("report CSV has no header row")
	}
	return columns, rows, nil
}

func isBlankRecord(record []string) bool {
	for _, f := range record {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// bingReportRows turns parsed CSV into the row objects the tool results carry:
// one JSON object per row, keyed by column name.
//
// Values stay strings. A report cell can hold "--" (score not computed), a
// percentage, or a currency amount whose currency is another column, and
// guessing a number out of that would quietly turn "no data" into zero.
func bingReportRows(columns []string, records [][]string) ([]json.RawMessage, error) {
	rows := make([]json.RawMessage, 0, len(records))
	for _, record := range records {
		row := make(map[string]string, len(columns))
		for i, col := range columns {
			if i < len(record) {
				row[col] = strings.TrimSpace(record[i])
			}
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("encode report row: %w", err)
		}
		rows = append(rows, encoded)
	}
	return rows, nil
}

// fetchBingReportRows downloads and parses a completed report.
func fetchBingReportRows(ctx context.Context, c *BingClient, status bingReportStatus) (columns []string, rows []json.RawMessage, err error) {
	if status.DownloadURL == "" {
		// Documented behaviour: a successful report with no matching data comes
		// back with an empty URL. That is an answer ("nothing ran in this
		// window"), not a failure.
		return nil, nil, nil
	}
	data, err := c.DownloadReport(ctx, status.DownloadURL)
	if err != nil {
		return nil, nil, err
	}
	columns, records, err := parseBingReportZip(data)
	if err != nil {
		return nil, nil, err
	}
	rows, err = bingReportRows(columns, records)
	if err != nil {
		return nil, nil, err
	}
	return columns, rows, nil
}
