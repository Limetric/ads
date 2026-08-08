package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// dollarsToMicros converts a currency amount to micros (1 unit = 1,000,000
// micros). Google Ads money fields are expressed in micros. The product is
// rounded, not truncated: 4.10 * 1e6 is 4099999.9999999995 in float64, and
// Google Ads rejects budget micros that aren't a multiple of the currency's
// minimum unit.
func dollarsToMicros(dollars float64) int64 {
	return int64(math.Round(dollars * 1_000_000.0))
}

// microsString renders a micros amount as the decimal string the REST API
// expects for int64 money fields.
func microsString(micros int64) string {
	return strconv.FormatInt(micros, 10)
}

// WriteResult is the standard structured output for a write tool. The first
// call (no confirm token) returns Token+Preview; the confirm call returns
// Applied=true with a Detail summary.
type WriteResult struct {
	Applied bool   `json:"applied"`
	Token   string `json:"confirm_token,omitempty"`
	Preview string `json:"preview,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// ResourceNames contains resources created or changed by the confirmed
	// mutation, when Google Ads returned them.
	ResourceNames []string `json:"resource_names,omitempty"`
	// StatusAfterApply is the lifecycle status the entity will hold once
	// applied (set by create tools so agents know whether to enable it).
	StatusAfterApply string `json:"status_after_apply,omitempty"`
	// NextActionHint points the agent at the next MCP tool to call (e.g. to
	// enable an entity created in PAUSED status).
	NextActionHint *NextActionHint `json:"next_action_hint,omitempty"`
}

// withCreateStatus annotates a preview WriteResult with the resulting status
// and, when that status is PAUSED, a hint describing how to enable the entity.
func (r WriteResult) withCreateStatus(status AdStatus, pausedHint NextActionHint) WriteResult {
	r.StatusAfterApply = string(status)
	if status == AdStatusPaused {
		h := pausedHint
		r.NextActionHint = &h
	}
	return r
}

// previewResult wraps a freshly staged pending mutation as a preview WriteResult.
func previewResult(p *PendingMutation) WriteResult {
	return WriteResult{Applied: false, Token: p.Token, Preview: p.previewText()}
}

// previewMutate stages a default googleAds:mutate write and returns its preview.
func previewMutate(tool, customerID, summary string, ops []any) (WriteResult, error) {
	p, err := stageMutation(tool, customerID, summary, ops)
	if err != nil {
		return WriteResult{}, err
	}
	return previewResult(p), nil
}

// previewMutateDouble is previewMutate for writes that must take a second
// confirmation (e.g. budget increases over 50% — issue #12).
func previewMutateDouble(tool, customerID, summary string, ops []any) (WriteResult, error) {
	p, err := stageMutationDouble(tool, customerID, summary, ops)
	if err != nil {
		return WriteResult{}, err
	}
	return previewResult(p), nil
}

// applyConfirmed consumes a confirm token and applies the staged write via the
// correct dispatch, writing an audit line on both success and failure.
func applyConfirmed(ctx context.Context, c *Client, tool, confirm string) (WriteResult, error) {
	p, err := consumeMutation(confirm)
	if err != nil {
		return WriteResult{}, err
	}
	// A token is bound to the tool that staged it: confirming a remove_entity
	// preview through enable_entity must not execute the removal (issue #6).
	if p.Tool != tool {
		return WriteResult{}, fmt.Errorf("confirmation token was issued by %q, not %q — the staged operation (%s) has been discarded; re-run the original command for a fresh preview", p.Tool, tool, p.Summary)
	}
	return applyConsumed(ctx, c, p)
}

// applyConsumed finishes a consumed pending write: it re-stages destructive
// operations for their second confirmation, otherwise applies and audits.
// Shared by the per-tool confirm path (applyConfirmed) and `ads confirm`.
func applyConsumed(ctx context.Context, c *Client, p *PendingMutation) (WriteResult, error) {
	// Destructive operations take two confirmations: the first consume
	// re-stages under a fresh token instead of applying (issue #12).
	if p.RequiresDouble && !p.DoubleConfirmed {
		p2, err := restageForDoubleConfirm(p)
		if err != nil {
			return WriteResult{}, err
		}
		return WriteResult{
			Applied: false,
			Token:   p2.Token,
			Preview: p2.previewText() + "\nDESTRUCTIVE operation — a second confirmation is required. Re-run with the token above to apply.",
			Detail:  "second confirmation required",
		}, nil
	}
	outcome, err := applyPending(ctx, c, p)
	if err != nil {
		auditLog(p, false)
		return WriteResult{}, toolError(p.Tool, err)
	}
	auditLog(p, true)
	return WriteResult{
		Applied:       true,
		Detail:        p.Summary,
		ResourceNames: resourceNamesFromResults(outcome.Results),
	}, nil
}

func partialFailureError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil
	}
	var status struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("decode Google Ads partial failure: %w", err)
	}
	if status.Code == 0 && status.Message == "" && len(status.Details) == 0 {
		return nil
	}
	if status.Message == "" {
		status.Message = string(raw)
	}
	return fmt.Errorf("google ads mutation partially failed (code %d): %s", status.Code, status.Message)
}

func resourceNamesFromResults(results []json.RawMessage) []string {
	seen := make(map[string]bool)
	var names []string
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if name, ok := value["resourceName"].(string); ok && name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
			for _, nested := range value {
				walk(nested)
			}
		case []any:
			for _, nested := range value {
				walk(nested)
			}
		}
	}
	for _, result := range results {
		var value any
		if json.Unmarshal(result, &value) == nil {
			walk(value)
		}
	}
	return names
}
