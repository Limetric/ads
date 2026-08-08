package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// googleDoctor is the Google platform's Platform.Doctor hook: it prints the
// resolved credential summary, then (unless offline) probes the live API.
func googleDoctor(ctx context.Context, out io.Writer, offline bool) (liveResult, error) {
	cfg, err := loadGoogleConfig(configPath)
	if err != nil {
		return liveUnconfigured, err
	}
	fmt.Fprintf(out, "base URL:           %s\n", cfg.BaseURL)
	fmt.Fprintf(out, "developer token:    %s\n", present(cfg.DeveloperToken))
	fmt.Fprintf(out, "client id:          %s\n", present(cfg.ClientID))
	fmt.Fprintf(out, "client secret:      %s\n", present(cfg.ClientSecret))
	fmt.Fprintf(out, "refresh token:      %s\n", present(cfg.RefreshToken))
	fmt.Fprintf(out, "login customer id:  %s\n", orNone(cfg.LoginCustomerID))
	fmt.Fprintf(out, "default customer:   %s\n", orNone(cfg.DefaultCustomerID))
	if err := cfg.validate(); err != nil {
		return liveUnconfigured, err
	}
	if offline {
		return liveOffline, nil
	}
	fmt.Fprintln(out)
	return runGoogleDoctorLive(ctx, out, cfg)
}

// runGoogleDoctorLive makes real API calls to prove the setup works, printing a
// line per probe (✓ ok, ✗ definitive failure, ? inconclusive). It runs two
// probes because they fail independently:
//
//  1. listAccessibleCustomers — needs only OAuth + developer token, so it
//     confirms credentials are valid and lists reachable accounts. A test-level
//     developer token still passes this.
//  2. a real customer_client search on the login customer — what every read
//     command does. Unlike probe 1 it fails when the developer token is only
//     approved for test accounts (DEVELOPER_TOKEN_NOT_APPROVED), the exact gap
//     that made plain `doctor` say "ready" for a setup that can't query.
//
// It returns the verdict of the first probe that doesn't pass, so the caller can
// set the status line and exit code.
func runGoogleDoctorLive(ctx context.Context, out io.Writer, cfg *GoogleConfig) (liveResult, error) {
	client, err := NewClient(ctx, cfg)
	if err != nil {
		return reportProbe(out, "client:              ", err), err
	}

	ids, err := client.ListAccessibleCustomers(ctx)
	if err != nil {
		return reportProbe(out, "accessible accounts: ", err), err
	}
	dashed := make([]string, len(ids))
	for i, id := range ids {
		dashed[i] = dashCustomerID(id)
	}
	fmt.Fprintf(out, "accessible accounts:  ✓ %d (%s)\n", len(ids), strings.Join(dashed, ", "))

	if cfg.LoginCustomerID == "" {
		fmt.Fprintf(out, "live query:           skipped (no login_customer_id set)\n")
		return liveOK, nil
	}
	res, err := runAccounts(ctx, client, AccountsArgs{})
	if err != nil {
		return reportProbe(out, "live query:          ", err), err
	}
	fmt.Fprintf(out, "live query:           ✓ %d account(s) reachable under %s\n", len(res.CustomerIDs), dashCustomerID(cfg.LoginCustomerID))
	return liveOK, nil
}
