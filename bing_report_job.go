package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A report job outlives the process that started it. `ads bing campaigns` may
// hand back a handle from a one-shot CLI run and `ads bing report fetch` may
// pick it up from an MCP server minutes later, so the handle is a file under
// stateDir() — the same reasoning that puts confirm tokens there.

// bingReportJobTTL is how long a job handle is worth keeping. Microsoft holds a
// report request ID for a maximum of one day and then discards the file, so a
// handle older than that cannot be fetched no matter what ads remembers.
const bingReportJobTTL = 24 * time.Hour

// bingReportJobPrefix is the file prefix in the state directory.
const bingReportJobPrefix = "bing-report-"

// bingReportJob is a submitted report ads is holding a handle for.
type bingReportJob struct {
	// ID is the handle shown to the user (`job_8f3c1a2b`).
	ID string `json:"id"`
	// Tool is the tool that submitted the report, so the fetch can say what it
	// was and re-apply the same column ordering.
	Tool string `json:"tool"`
	// ReportRequestID is Microsoft's identifier for the queued report.
	ReportRequestID string `json:"report_request_id"`
	AccountID       string `json:"account_id"`
	// Columns is the requested column order. The CSV carries its own header,
	// but a table rendered in request order is the one the user asked for.
	Columns     []string  `json:"columns,omitempty"`
	DateRange   string    `json:"date_range,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// expired reports whether the job is past the point where Microsoft would still
// have the report.
func (j *bingReportJob) expired() bool {
	return time.Since(j.SubmittedAt) > bingReportJobTTL
}

// newBingJobID mints a handle. The `job_` prefix is what makes it obvious in a
// terminal that the string is a handle to fetch and not an account number.
func newBingJobID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "job_" + hex.EncodeToString(b), nil
}

// validBingJobID reports whether s has the exact shape newBingJobID generates.
// The handle is caller-supplied input that becomes part of a file path, so it
// is checked before it can touch the filesystem — the same rule confirm tokens
// follow.
func validBingJobID(s string) bool {
	rest, ok := strings.CutPrefix(s, "job_")
	if !ok || len(rest) != 8 {
		return false
	}
	for _, r := range rest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// bingReportJobPath validates a handle and returns the file that holds it.
func bingReportJobPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("no report job handle provided")
	}
	if !validBingJobID(id) {
		return "", fmt.Errorf("malformed report job handle %q — expected the `job_…` handle from the command that queued the report", id)
	}
	dir, err := stateDir()
	if err != nil {
		return "", fmt.Errorf("report job store unavailable: %w", err)
	}
	return filepath.Join(dir, bingReportJobPrefix+id+".json"), nil
}

// saveBingReportJob persists a job handle.
//
// Failing to save is fatal to the call rather than cosmetic: the report is
// already queued, and handing back a handle that cannot be fetched would
// promise data the user has no way to collect — the same rule that makes an
// unstageable confirm token an error (issue #6).
func saveBingReportJob(job *bingReportJob) error {
	path, err := bingReportJobPath(job.ID)
	if err != nil {
		return err
	}
	sweepExpiredBingReportJobs(filepath.Dir(path))
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report job: %w", err)
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("save report job %q: %w", job.ID, err)
	}
	return nil
}

// loadBingReportJob reads a job handle back.
func loadBingReportJob(id string) (*bingReportJob, error) {
	path, err := bingReportJobPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unknown report job %q — handles are kept for %s after the report is queued", strings.TrimSpace(id), bingReportJobTTL)
	}
	var job bingReportJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("corrupt report job %q: %w", strings.TrimSpace(id), err)
	}
	if job.expired() {
		_ = os.Remove(path)
		return nil, fmt.Errorf("report job %q has expired — Microsoft keeps a queued report for at most %s; re-run the command to submit a new one", job.ID, bingReportJobTTL)
	}
	return &job, nil
}

// deleteBingReportJob removes a handle once its rows have been collected.
// Best-effort: a handle left behind expires on its own.
func deleteBingReportJob(id string) {
	if path, err := bingReportJobPath(id); err == nil {
		_ = os.Remove(path)
	}
}

// sweepExpiredBingReportJobs removes handles past their TTL so abandoned jobs
// don't accumulate. Best-effort.
func sweepExpiredBingReportJobs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, bingReportJobPrefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > bingReportJobTTL {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
