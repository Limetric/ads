package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file implements write guards, a human-readable mutation preview, and a
// confirm-token flow. The rule: no mutating call
// executes on first request. A write tool returns a preview plus a short-lived
// token; the caller re-invokes with that token to actually apply the change.
//
// The token store is file-backed (under stateDir) so it survives across the
// stateless CLI invocations a skill makes, and works the same inside a
// long-lived `ads mcp` session.

// confirmTTL bounds how long a pending confirmation is valid.
const confirmTTL = 10 * time.Minute

// PendingMutation is what a write tool stages for confirmation.
type PendingMutation struct {
	Token string `json:"token"`
	// Platform is the namespace whose API applies this write. `ads confirm`
	// reads it to build the right client, so a token staged by one platform is
	// never applied against another's API. Empty means Google: pending files
	// written before the field existed are still confirmable.
	Platform   string `json:"platform,omitempty"`
	Tool       string `json:"tool"`
	CustomerID string `json:"customer_id"`
	Summary    string `json:"summary"`
	Operations []any  `json:"operations"`
	// Dispatch selects the apply endpoint within the platform; the empty value
	// is each platform's default write path.
	Dispatch string `json:"dispatch,omitempty"`
	// ResourceNames carries full recommendation resource paths for the
	// recommendation dispatches (unused for the default mutate path).
	ResourceNames []string `json:"resource_names,omitempty"`
	// BudgetAmounts are the daily budget amounts this write would set, in the
	// account's currency. It exists so the shared spend cap can be re-checked
	// at confirm time without safety.go learning how any platform spells a
	// budget: a staged write declares the number, and the guard compares it.
	BudgetAmounts []float64 `json:"budget_amounts,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	// RequiresDouble marks destructive operations that need a second
	// confirmation (issue #12). DoubleConfirmed is set once the first confirm
	// has been consumed and the mutation re-staged under a fresh token.
	RequiresDouble  bool `json:"requires_double,omitempty"`
	DoubleConfirmed bool `json:"double_confirmed,omitempty"`
}

// pendingWrite is what a platform hands to stageWrite. It is a struct rather
// than a parameter list because platforms fill in different subsets of it, and
// a positional call would say nothing about which.
type pendingWrite struct {
	// Platform is the namespace whose API will apply this write — the one piece
	// of routing this file keeps, so `ads confirm <token>` can find the right
	// client without knowing what any platform's operations look like.
	Platform string
	// Tool is the tool staging the write; a token is bound to it.
	Tool string
	// CustomerID is the account the write acts on, in whatever form the
	// platform names accounts.
	CustomerID string
	// Summary is the human-readable description shown in the preview.
	Summary string
	// Dispatch selects the apply endpoint within the platform.
	Dispatch string
	// Operations is the platform's own operation payload.
	Operations []any
	// ResourceNames carries resource paths for dispatches that take them.
	ResourceNames []string
	// BudgetAmounts declares daily budget amounts for the shared spend cap.
	BudgetAmounts []float64
	// RequiresDouble forces a second confirmation for a write whose tool name
	// does not already imply one.
	RequiresDouble bool
}

// stageWrite persists a pending write and returns its confirm token.
func stageWrite(w pendingWrite) (*PendingMutation, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	p := &PendingMutation{
		Token:          tok,
		Platform:       w.Platform,
		Tool:           w.Tool,
		CustomerID:     w.CustomerID,
		Summary:        w.Summary,
		Operations:     w.Operations,
		Dispatch:       w.Dispatch,
		ResourceNames:  w.ResourceNames,
		BudgetAmounts:  w.BudgetAmounts,
		CreatedAt:      time.Now().UTC(),
		RequiresDouble: w.RequiresDouble || requiresDoubleConfirmation(w.Tool, nil, nil),
	}
	dir, err := stateDir()
	if err != nil {
		// Fail loudly: the token store is disk-backed only, so a token staged
		// without persistence could never be confirmed — handing one out would
		// promise an apply that must fail (issue #6).
		return nil, fmt.Errorf("confirmation store unavailable (%v) — writes need a usable config directory; set HOME/XDG_CONFIG_HOME", err)
	}
	sweepExpired(dir)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("stage confirmation: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pending-"+tok+".json"), data, 0o600); err != nil {
		return nil, fmt.Errorf("stage confirmation: %w", err)
	}
	return p, nil
}

// sweepExpired removes pending files past their TTL so abandoned previews
// don't accumulate in the state dir forever. Best-effort. Includes .claimed
// leftovers a crash between claim and remove could strand.
func sweepExpired(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "pending-") ||
			(!strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".json.claimed")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > confirmTTL {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// validToken reports whether s has the exact shape newToken generates
// (16 lowercase hex chars). Anything else is rejected before it can reach the
// filesystem — the token is caller-supplied input and must never influence
// which path is read or removed (issue #6).
func validToken(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// pendingPath validates a caller-supplied token and returns the path of its
// pending file. The shape check runs before the token can touch the
// filesystem (issue #6).
func pendingPath(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("no confirmation token provided")
	}
	if !validToken(token) {
		return "", fmt.Errorf("malformed confirmation token %q — expected the 16-character token from the preview", token)
	}
	dir, err := stateDir()
	if err != nil {
		return "", fmt.Errorf("confirmation store unavailable: %w", err)
	}
	return filepath.Join(dir, "pending-"+token+".json"), nil
}

// peekMutation loads a pending mutation by token WITHOUT consuming it, so
// pre-checks (e.g. blocked operations) can fail before the single-use token
// is irrevocably claimed.
func peekMutation(token string) (*PendingMutation, error) {
	path, err := pendingPath(strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unknown or already-used confirmation token %q", strings.TrimSpace(token))
	}
	var p PendingMutation
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("corrupt confirmation %q: %w", strings.TrimSpace(token), err)
	}
	return &p, nil
}

// consumeMutation loads and deletes a pending mutation by token, rejecting
// unknown or expired tokens.
func consumeMutation(token string) (*PendingMutation, error) {
	token = strings.TrimSpace(token)
	path, err := pendingPath(token)
	if err != nil {
		return nil, err
	}
	// Claim the pending file atomically before reading: two concurrent
	// confirms must not both apply the same staged mutation (issue #6). Only
	// the rename winner proceeds; the file stays single-use even if the apply
	// later fails.
	claimed := path + ".claimed"
	if err := os.Rename(path, claimed); err != nil {
		return nil, fmt.Errorf("unknown or already-used confirmation token %q", token)
	}
	data, err := os.ReadFile(claimed)
	_ = os.Remove(claimed)
	if err != nil {
		return nil, fmt.Errorf("read confirmation %q: %w", token, err)
	}
	var p PendingMutation
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("corrupt confirmation %q: %w", token, err)
	}
	if time.Since(p.CreatedAt) > confirmTTL {
		return nil, fmt.Errorf("confirmation token %q expired (valid for %s); re-run the command to get a fresh one", token, confirmTTL)
	}
	return &p, nil
}

// restageForDoubleConfirm re-stages a consumed destructive mutation under a
// fresh token that must be confirmed once more before it applies (issue #12).
func restageForDoubleConfirm(p *PendingMutation) (*PendingMutation, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	p.Token = tok
	p.DoubleConfirmed = true
	p.CreatedAt = time.Now().UTC()
	dir, err := stateDir()
	if err != nil {
		return nil, fmt.Errorf("confirmation store unavailable: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("stage second confirmation: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pending-"+tok+".json"), data, 0o600); err != nil {
		return nil, fmt.Errorf("stage second confirmation: %w", err)
	}
	return p, nil
}

// applyOutcome is what a platform's applier hands back once a confirmed write
// has really been executed: the raw result rows, as that platform returned
// them. Rendering them is the caller's job (see WriteResult).
type applyOutcome struct {
	Results []json.RawMessage
}

// mutationApplier executes a confirmed write against one platform's API. Each
// platform's client implements it (see (*Client).applyMutation for Google), so
// staging, confirming, double-confirmation, and auditing are shared and only
// the final API call is platform-specific.
type mutationApplier interface {
	// platformName is the namespace this client writes to. A staged write
	// records the platform that created it, and the two must agree before
	// anything is applied: tool names are not unique across platforms — both
	// networks have a set_campaign_budget — so the tool binding alone would let
	// one platform's token be handed to another platform's API.
	platformName() string
	applyMutation(ctx context.Context, p *PendingMutation) (*applyOutcome, error)
}

// platform is the namespace that staged this write. An empty field means
// Google: pending files written before the field existed are still confirmable,
// and Google was the only platform that could have written one.
func (p *PendingMutation) platform() string {
	if p.Platform == "" {
		return googlePlatformName
	}
	return p.Platform
}

// previewText renders a staged mutation for a human/agent to review.
func (p *PendingMutation) previewText() string {
	var b strings.Builder
	// "account", not "customer": the field is Google's customer ID and Bing's ad
	// account, and the neutral word is the one that is true on both.
	fmt.Fprintf(&b, "PREVIEW — %s on account %s\n", p.Tool, p.CustomerID)
	fmt.Fprintf(&b, "%s\n", p.Summary)
	fmt.Fprintf(&b, "%d operation(s) staged. Nothing has been changed yet.\n", len(p.Operations))
	fmt.Fprintf(&b, "\nTo apply, re-run with --confirm %s (or run: ads confirm %s)\n", p.Token, p.Token)
	return b.String()
}

// auditLog appends a single line describing an applied mutation. Best-effort:
// audit failures never block or fail the operation.
func auditLog(p *PendingMutation, applied bool) {
	dir, err := stateDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "audit.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	// The platform is part of the line because the tool name alone stopped
	// identifying the write once two networks had a set_campaign_budget, and an
	// account ID does not say which network it belongs to.
	platform := p.Platform
	if platform == "" {
		platform = googlePlatformName
	}
	fmt.Fprintf(f, "%s platform=%s tool=%s account=%s ops=%d applied=%t token=%s\n",
		time.Now().UTC().Format(time.RFC3339), platform, p.Tool, p.CustomerID, len(p.Operations), applied, p.Token)
}

func newToken() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// allowedMutateOps is the whitelist of top-level MutateOperation keys accepted
// by Google Ads v23's googleAds:mutate endpoint. Payloads with any key not in
// this set are rejected client-side before any HTTP traffic — this catches
// applyRecommendationOperation / dismissRecommendationOperation mistakes (those
// live on dedicated RPCs, see Client.ApplyRecommendations / DismissRecommendations).
//
// Source: Google Ads API v23 MutateOperation.operation oneof definition.
var allowedMutateOps = map[string]bool{
	"adGroupAdLabelOperation":               true,
	"adGroupAdOperation":                    true,
	"adGroupAssetOperation":                 true,
	"adGroupBidModifierOperation":           true,
	"adGroupCriterionCustomizerOperation":   true,
	"adGroupCriterionLabelOperation":        true,
	"adGroupCriterionOperation":             true,
	"adGroupCustomizerOperation":            true,
	"adGroupExtensionSettingOperation":      true,
	"adGroupFeedOperation":                  true,
	"adGroupLabelOperation":                 true,
	"adGroupOperation":                      true,
	"adOperation":                           true,
	"adParameterOperation":                  true,
	"assetGroupAssetOperation":              true,
	"assetGroupListingGroupFilterOperation": true,
	"assetGroupOperation":                   true,
	"assetGroupSignalOperation":             true,
	"assetOperation":                        true,
	"assetSetAssetOperation":                true,
	"assetSetOperation":                     true,
	"audienceOperation":                     true,
	"biddingDataExclusionOperation":         true,
	"biddingSeasonalityAdjustmentOperation": true,
	"biddingStrategyOperation":              true,
	"campaignAssetOperation":                true,
	"campaignAssetSetOperation":             true,
	"campaignBidModifierOperation":          true,
	"campaignBudgetOperation":               true,
	"campaignConversionGoalOperation":       true,
	"campaignCriterionOperation":            true,
	"campaignCustomizerOperation":           true,
	"campaignDraftOperation":                true,
	"campaignExtensionSettingOperation":     true,
	"campaignFeedOperation":                 true,
	"campaignGroupOperation":                true,
	"campaignLabelOperation":                true,
	"campaignOperation":                     true,
	"campaignSharedSetOperation":            true,
	"conversionActionOperation":             true,
	"conversionCustomVariableOperation":     true,
	"conversionGoalCampaignConfigOperation": true,
	"conversionValueRuleOperation":          true,
	"conversionValueRuleSetOperation":       true,
	"customConversionGoalOperation":         true,
	"customerAssetOperation":                true,
	"customerConversionGoalOperation":       true,
	"customerCustomizerOperation":           true,
	"customerExtensionSettingOperation":     true,
	"customerFeedOperation":                 true,
	"customerLabelOperation":                true,
	"customerNegativeCriterionOperation":    true,
	"customerOperation":                     true,
	"customizerAttributeOperation":          true,
	"experimentArmOperation":                true,
	"experimentOperation":                   true,
	"extensionFeedItemOperation":            true,
	"feedItemOperation":                     true,
	"feedItemSetLinkOperation":              true,
	"feedItemSetOperation":                  true,
	"feedItemTargetOperation":               true,
	"feedMappingOperation":                  true,
	"feedOperation":                         true,
	"keywordPlanAdGroupKeywordOperation":    true,
	"keywordPlanAdGroupOperation":           true,
	"keywordPlanCampaignKeywordOperation":   true,
	"keywordPlanCampaignOperation":          true,
	"keywordPlanOperation":                  true,
	"labelOperation":                        true,
	"remarketingActionOperation":            true,
	"sharedCriterionOperation":              true,
	"sharedSetOperation":                    true,
	"smartCampaignSettingOperation":         true,
	"userListOperation":                     true,
}

// validateMutateOps verifies every operation uses a top-level key from
// allowedMutateOps, returning an actionable error on the first offender. This
// runs before any HTTP traffic (see Client.Mutate).
func validateMutateOps(ops []any) error {
	for i, op := range ops {
		m, ok := op.(map[string]any)
		if !ok {
			return fmt.Errorf("mutate operation at index %d is not a JSON object", i)
		}
		for key := range m {
			if !allowedMutateOps[key] {
				return fmt.Errorf("unknown MutateOperation key %q at index %d: recommendation operations must use apply_recommendations / dismiss_recommendations — they are NOT valid keys on googleAds:mutate in v23", key, i)
			}
		}
	}
	return nil
}
