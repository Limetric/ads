package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file implements spend caps, bid-increase limits, blocked-operation
// checks, the BROAD+MANUAL_CPC trap, double-confirm heuristics, and RSA/sitelink
// character-limit validators. Guards are pure functions taking an explicit
// SafetyConfig so they are trivially testable.

// SafetyConfig holds write-guard thresholds. Values have conservative defaults
// and are overridable via environment variables.
type SafetyConfig struct {
	MaxDailyBudget    float64  // GOOGLE_ADS_MAX_DAILY_BUDGET (currency units), default 50.0
	MaxBidIncreasePct float64  // GOOGLE_ADS_MAX_BID_INCREASE_PCT, default 100
	BlockedOperations []string // GOOGLE_ADS_BLOCKED_OPS (comma-separated)
}

func defaultSafetyConfig() SafetyConfig {
	return SafetyConfig{MaxDailyBudget: 50.0, MaxBidIncreasePct: 100}
}

// loadSafetyConfig builds a SafetyConfig from the environment, falling back to
// the built-in defaults for anything unset or unparseable. Only finite,
// positive overrides are accepted: NaN would disable a cap entirely (x > NaN
// is always false) and a negative cap is meaningless (issue #12).
func loadSafetyConfig() SafetyConfig {
	cfg := defaultSafetyConfig()
	if f, ok := parseGuardValue(os.Getenv("GOOGLE_ADS_MAX_DAILY_BUDGET")); ok {
		cfg.MaxDailyBudget = f
	}
	if f, ok := parseGuardValue(os.Getenv("GOOGLE_ADS_MAX_BID_INCREASE_PCT")); ok {
		cfg.MaxBidIncreasePct = f
	}
	if v := strings.TrimSpace(os.Getenv("GOOGLE_ADS_BLOCKED_OPS")); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.BlockedOperations = append(cfg.BlockedOperations, p)
			}
		}
	}
	return cfg
}

// parseGuardValue parses a guard override from the environment, accepting only
// finite positive values.
func parseGuardValue(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, false
	}
	return f, true
}

// checkBudgetCap rejects a proposed daily budget above the configured maximum.
// The comparison is written so a NaN budget also fails.
func checkBudgetCap(dailyBudget float64, cfg SafetyConfig) error {
	if !(dailyBudget <= cfg.MaxDailyBudget) {
		return fmt.Errorf("daily budget %.2f exceeds maximum %.2f (raise GOOGLE_ADS_MAX_DAILY_BUDGET to allow)", dailyBudget, cfg.MaxDailyBudget)
	}
	return nil
}

// checkBidIncrease rejects a bid increase beyond the configured percentage. A
// non-positive current bid is treated as "no baseline" and always allowed.
func checkBidIncrease(currentBid, proposedBid float64, cfg SafetyConfig) error {
	if currentBid <= 0 {
		return nil
	}
	increasePct := ((proposedBid - currentBid) / currentBid) * 100
	if increasePct > cfg.MaxBidIncreasePct {
		return fmt.Errorf("bid increase %.0f%% exceeds maximum %.0f%%", increasePct, cfg.MaxBidIncreasePct)
	}
	return nil
}

// revalidateBudgetCaps re-checks the daily-budget cap for every staged
// campaign-budget amount in a pending mutation's operations. `ads confirm`
// runs it against the CURRENT configuration before consuming a token, so
// tightening GOOGLE_ADS_MAX_DAILY_BUDGET between preview and confirm is
// enforced on the generic confirm path just as re-running the original
// command enforces it on the per-tool path.
func revalidateBudgetCaps(ops []any, cfg SafetyConfig) error {
	for _, op := range ops {
		m, ok := op.(map[string]any)
		if !ok {
			continue
		}
		budgetOp, ok := m["campaignBudgetOperation"].(map[string]any)
		if !ok {
			continue
		}
		for _, verb := range []string{"create", "update"} {
			body, ok := budgetOp[verb].(map[string]any)
			if !ok {
				continue
			}
			micros, ok := asFloat(body["amountMicros"])
			if !ok {
				continue
			}
			if err := checkBudgetCap(micros/1_000_000.0, cfg); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkBlockedOperation rejects an operation present in the configured block list.
func checkBlockedOperation(operation string, cfg SafetyConfig) error {
	for _, b := range cfg.BlockedOperations {
		if b == operation {
			return fmt.Errorf("operation %q is blocked by configuration", operation)
		}
	}
	return nil
}

// checkBroadManualCPC blocks the budget-burning BROAD match + MANUAL_CPC combo.
func checkBroadManualCPC(matchType, biddingStrategy string) error {
	if matchType == "BROAD" && biddingStrategy == "MANUAL_CPC" {
		return fmt.Errorf("BROAD match with MANUAL_CPC is blocked — this combination burns budget. Use Smart Bidding (MAXIMIZE_CONVERSIONS, TARGET_CPA) with BROAD match, or use EXACT/PHRASE with MANUAL_CPC")
	}
	return nil
}

// requiresDoubleConfirmation reports whether an operation warrants a second
// confirmation: destructive ops (delete/remove), or budget increases over 50%.
// currentBudget/proposedBudget are optional (nil = unknown).
func requiresDoubleConfirmation(operation string, currentBudget, proposedBudget *float64) bool {
	if strings.Contains(operation, "delete") || strings.Contains(operation, "remove") {
		return true
	}
	if currentBudget != nil && proposedBudget != nil {
		if *currentBudget > 0 && ((*proposedBudget-*currentBudget)/(*currentBudget)) > 0.5 {
			return true
		}
	}
	return false
}

// charLimit returns an error if s exceeds limit characters (runes, not bytes).
func charLimit(kind, s string, limit int) error {
	if n := utf8.RuneCountInString(s); n > limit {
		return fmt.Errorf("%s %q exceeds %d character limit (%d chars)", kind, s, limit, n)
	}
	return nil
}

// validateHeadline enforces the 30-character RSA headline limit.
func validateHeadline(headline string) error { return charLimit("headline", headline, 30) }

// validateDescription enforces the 90-character RSA description limit.
func validateDescription(desc string) error { return charLimit("description", desc, 90) }

// validateSitelinkText enforces the 25-character sitelink text limit.
func validateSitelinkText(text string) error { return charLimit("sitelink text", text, 25) }

// validateSitelinkDescription enforces the 35-character sitelink description limit.
func validateSitelinkDescription(desc string) error {
	return charLimit("sitelink description", desc, 35)
}
