package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// The metric tools. Each submits one report, waits up to bingReportDeadline,
// and then either returns rows or hands back a job — see bing_report.go for why
// that is the shape.

// BingPerformanceArgs is the input every metric tool takes.
type BingPerformanceArgs struct {
	AccountID string   `json:"account_id,omitempty" jsonschema:"the Microsoft Advertising account ID; omit to use the configured default account"`
	Days      int      `json:"days,omitempty" jsonschema:"number of complete days to report, ending yesterday; defaults to 30. Ignored when date_start and date_end are given"`
	DateStart string   `json:"date_start,omitempty" jsonschema:"start date YYYY-MM-DD; pair with date_end for an explicit window"`
	DateEnd   string   `json:"date_end,omitempty" jsonschema:"end date YYYY-MM-DD; pair with date_start for an explicit window"`
	Columns   []string `json:"columns,omitempty" jsonschema:"override the report columns; omit for the tool's standard set. Column names are Microsoft's, e.g. Impressions, Spend, ConversionsQualified"`
}

// bingDefaultReportDays is the window a metric tool covers when asked for none.
// It matches the Google tools' LAST_30_DAYS so the same question gives a
// comparable answer on both platforms.
const bingDefaultReportDays = 30

// BingReportJobHandle is what a tool returns instead of rows when its report is
// still running.
type BingReportJobHandle struct {
	Job string `json:"job"`
	// FetchCommand is the exact command that collects the rows — the whole
	// point of the handle is that the user does not have to work that out.
	FetchCommand string `json:"fetch_command"`
	// FetchTool is the same thing for an MCP caller.
	FetchTool       string `json:"fetch_tool"`
	ReportRequestID string `json:"report_request_id"`
	SubmittedAt     string `json:"submitted_at"`
}

// BingReportResult is the structured output of every metric tool: rows when the
// report finished in time, a job handle when it did not.
type BingReportResult struct {
	// Columns is the report's column order, for table and CSV rendering.
	Columns []string `json:"columns,omitempty"`
	// Rows is one object per report row, keyed by column name. Values are
	// strings exactly as Microsoft rendered them (a cell may hold "--" for a
	// figure that was not computed).
	Rows      []json.RawMessage `json:"rows,omitempty"`
	RowCount  int               `json:"row_count"`
	AccountID string            `json:"account_id"`
	DateRange string            `json:"date_range"`
	// Job is set when the report did not finish inside the deadline.
	Job *BingReportJobHandle `json:"job,omitempty"`
	// Message explains an empty result or a pending job in words.
	Message string `json:"message,omitempty"`
}

func (r BingReportResult) tableRows() ([]json.RawMessage, []string) {
	return r.Rows, r.Columns
}

// resolveRange turns the arguments into the window to report on: an explicit
// pair of dates, or the last N complete days.
func (a BingPerformanceArgs) resolveRange(now time.Time) (start, end time.Time, err error) {
	if a.DateStart != "" || a.DateEnd != "" {
		if a.DateStart == "" || a.DateEnd == "" {
			return time.Time{}, time.Time{}, fmt.Errorf("date_start and date_end must be given together")
		}
		start, err = time.Parse(time.DateOnly, a.DateStart)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid date_start %q — expected YYYY-MM-DD", a.DateStart)
		}
		end, err = time.Parse(time.DateOnly, a.DateEnd)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid date_end %q — expected YYYY-MM-DD", a.DateEnd)
		}
		if end.Before(start) {
			return time.Time{}, time.Time{}, fmt.Errorf("date_end %s is before date_start %s", a.DateEnd, a.DateStart)
		}
		return start, end, nil
	}
	days := a.Days
	if days == 0 {
		days = bingDefaultReportDays
	}
	if days < 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("days must be positive")
	}
	start, end = bingReportRange(now, days)
	return start, end, nil
}

// runBingReportTool is the body of every metric tool: submit, wait, and return
// rows or a job.
func runBingReportTool(ctx context.Context, c *BingClient, preset bingReportPreset, args BingPerformanceArgs) (BingReportResult, error) {
	accountID, err := c.resolveAccountID(args.AccountID)
	if err != nil {
		return BingReportResult{}, err
	}
	start, end, err := args.resolveRange(time.Now())
	if err != nil {
		return BingReportResult{}, toolError(preset.Tool, err)
	}
	spec := bingReportSpec{Preset: preset, AccountID: accountID, Columns: args.Columns, Start: start, End: end}

	requestID, err := c.SubmitReport(ctx, spec)
	if err != nil {
		return BingReportResult{}, toolError(preset.Tool, err)
	}
	status, ready, err := awaitBingReport(ctx, c, accountID, requestID, bingReportDeadline)
	if err != nil {
		return BingReportResult{}, toolError(preset.Tool, err)
	}
	if !ready {
		return bingQueuedResult(spec, requestID)
	}
	columns, rows, err := fetchBingReportRows(ctx, c, status)
	if err != nil {
		return BingReportResult{}, toolError(preset.Tool, err)
	}
	return bingRowsResult(spec.AccountID, spec.dateRange(), spec.columns(), columns, rows), nil
}

// bingQueuedResult persists a job handle and describes it.
//
// The report is already running at this point, so a handle that cannot be saved
// is a failure worth reporting: the alternative is telling the user their data
// is coming and giving them no way to collect it.
func bingQueuedResult(spec bingReportSpec, requestID string) (BingReportResult, error) {
	id, err := newBingJobID()
	if err != nil {
		return BingReportResult{}, err
	}
	job := &bingReportJob{
		ID:              id,
		Tool:            spec.Preset.Tool,
		ReportRequestID: requestID,
		AccountID:       spec.AccountID,
		Columns:         spec.columns(),
		DateRange:       spec.dateRange(),
		SubmittedAt:     time.Now().UTC(),
	}
	if err := saveBingReportJob(job); err != nil {
		return BingReportResult{}, toolError(spec.Preset.Tool, err)
	}
	return BingReportResult{
		AccountID: spec.AccountID,
		DateRange: spec.dateRange(),
		Job: &BingReportJobHandle{
			Job:             job.ID,
			FetchCommand:    fmt.Sprintf("ads %s report fetch %s", bingPlatformName, job.ID),
			FetchTool:       bingPlatformName + "_report_fetch",
			ReportRequestID: requestID,
			SubmittedAt:     job.SubmittedAt.Format(time.RFC3339),
		},
		Message: fmt.Sprintf("report queued: %s — rows not ready after %s. Microsoft is still generating it; fetch it with `ads %s report fetch %s`.",
			job.ID, bingReportDeadline, bingPlatformName, job.ID),
	}, nil
}

// bingRowsResult assembles a finished report. requested is the column order ads
// asked for and returned is the order the CSV came back in; the CSV wins,
// because it is what the values are actually keyed by.
func bingRowsResult(accountID, dateRange string, requested, returned []string, rows []json.RawMessage) BingReportResult {
	columns := returned
	if len(columns) == 0 {
		columns = requested
	}
	res := BingReportResult{
		Columns:   columns,
		Rows:      rows,
		RowCount:  len(rows),
		AccountID: accountID,
		DateRange: dateRange,
	}
	if len(rows) == 0 {
		res.Message = "no report data for this account and date range"
	}
	return res
}

// BingReportFetchArgs collects the rows of a previously queued report.
type BingReportFetchArgs struct {
	Job string `json:"job" jsonschema:"the job handle returned when the report was queued, e.g. job_8f3c1a2b"`
}

// runBingReportFetch picks up a queued report. It waits the same bounded time a
// metric tool does, so a report that finishes moments later still comes back
// with rows rather than asking the caller to poll again.
func runBingReportFetch(ctx context.Context, c *BingClient, args BingReportFetchArgs) (BingReportResult, error) {
	const tool = "report_fetch"
	job, err := loadBingReportJob(args.Job)
	if err != nil {
		return BingReportResult{}, toolError(tool, err)
	}
	status, ready, err := awaitBingReport(ctx, c, job.AccountID, job.ReportRequestID, bingReportDeadline)
	if err != nil {
		// A report the service failed to generate is finished, badly: keeping
		// its handle would only offer to fetch it again forever.
		deleteBingReportJob(job.ID)
		return BingReportResult{}, toolError(tool, err)
	}
	if !ready {
		return BingReportResult{
			AccountID: job.AccountID,
			DateRange: job.DateRange,
			Job: &BingReportJobHandle{
				Job:             job.ID,
				FetchCommand:    fmt.Sprintf("ads %s report fetch %s", bingPlatformName, job.ID),
				FetchTool:       bingPlatformName + "_report_fetch",
				ReportRequestID: job.ReportRequestID,
				SubmittedAt:     job.SubmittedAt.Format(time.RFC3339),
			},
			Message: fmt.Sprintf("report %s (%s) is still running after %s — try the same fetch again shortly",
				job.ID, job.Tool, bingReportDeadline),
		}, nil
	}
	columns, rows, err := fetchBingReportRows(ctx, c, status)
	if err != nil {
		return BingReportResult{}, toolError(tool, err)
	}
	// The rows are in hand; the handle has nothing left to offer.
	deleteBingReportJob(job.ID)
	return bingRowsResult(job.AccountID, job.DateRange, job.Columns, columns, rows), nil
}

// --- CLI front-end ---

var (
	bingCampaignPerfArgs   BingPerformanceArgs
	bingKeywordPerfArgs    BingPerformanceArgs
	bingAdPerfArgs         BingPerformanceArgs
	bingCampaignPerfFormat string
	bingKeywordPerfFormat  string
	bingAdPerfFormat       string
	bingReportFetchFormat  string
)

// bingPerformanceCmd builds one metric subcommand. The three differ only in the
// preset they run, so they are built rather than repeated.
//
// Each gets its own --format destination. Sharing one would work for a one-shot
// process and break the moment two commands run in the same one, which is
// exactly what the tests do.
func bingPerformanceCmd(use, short string, preset bingReportPreset, args *BingPerformanceArgs, format *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newBingClient(cmd.Context())
			if err != nil {
				return err
			}
			res, err := runBingReportTool(cmd.Context(), client, preset, *args)
			if err != nil {
				return err
			}
			return printBingReport(cmd, *format, res)
		},
	}
	addBingAccountFlag(cmd, &args.AccountID)
	cmd.Flags().IntVar(&args.Days, "days", 0, "number of complete days to report, ending yesterday (default 30)")
	cmd.Flags().StringVar(&args.DateStart, "date-start", "", "start date YYYY-MM-DD (use with --date-end)")
	cmd.Flags().StringVar(&args.DateEnd, "date-end", "", "end date YYYY-MM-DD (use with --date-start)")
	cmd.Flags().StringSliceVar(&args.Columns, "columns", nil, "override the report columns (Microsoft column names)")
	addFormatFlag(cmd, format)
	return cmd
}

// printBingReport renders a report result, and puts the "still running" notice
// on stderr so stdout stays valid output for a pipeline.
func printBingReport(cmd *cobra.Command, format string, res BingReportResult) error {
	if err := printResult(cmd.OutOrStdout(), format, res); err != nil {
		return err
	}
	if res.Job != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", res.Message)
	}
	return nil
}

var bingCampaignPerformanceCmd = bingPerformanceCmd(
	"campaign-performance",
	"Campaign spend, clicks, and conversions (defaults to the last 30 complete days)",
	bingCampaignPerformancePreset, &bingCampaignPerfArgs, &bingCampaignPerfFormat)

var bingKeywordPerformanceCmd = bingPerformanceCmd(
	"keyword-performance",
	"Keyword spend, clicks, conversions, and quality score (defaults to the last 30 complete days)",
	bingKeywordPerformancePreset, &bingKeywordPerfArgs, &bingKeywordPerfFormat)

var bingAdPerformanceCmd = bingPerformanceCmd(
	"ad-performance",
	"Ad-level spend, clicks, and conversions (defaults to the last 30 complete days)",
	bingAdPerformancePreset, &bingAdPerfArgs, &bingAdPerfFormat)

var bingReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Work with queued Microsoft Advertising reports",
}

var bingReportFetchCmd = &cobra.Command{
	Use:   "fetch <job>",
	Short: "Collect the rows of a report that was queued earlier",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, cmdArgs []string) error {
		client, err := newBingClient(cmd.Context())
		if err != nil {
			return err
		}
		res, err := runBingReportFetch(cmd.Context(), client, BingReportFetchArgs{Job: cmdArgs[0]})
		if err != nil {
			return err
		}
		return printBingReport(cmd, bingReportFetchFormat, res)
	},
}

func init() {
	addFormatFlag(bingReportFetchCmd, &bingReportFetchFormat)
	bingReportCmd.AddCommand(bingReportFetchCmd)
}
