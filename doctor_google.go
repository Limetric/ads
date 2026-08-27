package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// googleSignInReport describes the sign-in that is actually in effect, for a
// report printed *after* resolution.
//
// Normally that is the one in the store. But when the store cannot be written,
// resolution deliberately falls back to a deprecated environment or config-file
// value and the setup still works — a supported case for read-only containers.
// Describing only the store would then print "none saved" directly above a
// "ready" verdict, which reads as a bug in ads rather than a report about the
// user's setup.
func googleSignInReport(cfg *GoogleConfig, store tokenStoreStatus) string {
	if store.Token == nil && store.ReadErr == nil && cfg.RefreshToken != "" {
		origin := "a deprecated source"
		if seeds := presentSeeds(cfg.refreshTokenSeeds()); len(seeds) > 0 {
			origin = seeds[0].Origin
		}
		return fmt.Sprintf("not saved — using %s for this run (deprecated; the token store could not be written)", origin)
	}
	return store.describe(googleTokenPolicy)
}

// googleDoctor is the Google platform's Platform.Doctor hook: it prints the
// resolved credential summary, then (unless offline) probes the live API.
func googleDoctor(ctx context.Context, out io.Writer, offline bool) (liveResult, error) {
	cfg, err := loadGoogleConfig(configPath)
	if err != nil {
		return liveUnconfigured, err
	}
	// Resolve before reporting so what doctor prints is what a real command
	// would use — including migrating a deprecated seed, which is a setup step
	// the user should see happen here rather than on their next query.
	if err := cfg.resolveRefreshToken(); err != nil {
		return liveUnconfigured, err
	}
	store := describeTokenStore(googleTokenPolicy.Platform)
	st := newStyles(out)
	field := func(label, value string) { fmt.Fprintf(out, "%s%s\n", st.field(label, doctorFieldWidth), value) }
	field("base URL", cfg.BaseURL)
	field("developer token", st.presence(cfg.DeveloperToken))
	field("client id", st.presence(cfg.ClientID))
	field("client secret", st.presence(cfg.ClientSecret))
	field("token store", store.location())
	field("saved sign-in", googleSignInReport(cfg, store))
	field("login customer id", st.optional(cfg.LoginCustomerID))
	field("default customer", st.optional(cfg.DefaultCustomerID))
	if err := cfg.validate(); err != nil {
		return liveUnconfigured, err
	}
	if offline {
		return liveOffline, nil
	}
	fmt.Fprintln(out)
	return runGoogleDoctorLive(ctx, out, st, cfg)
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
func runGoogleDoctorLive(ctx context.Context, out io.Writer, st styles, cfg *GoogleConfig) (liveResult, error) {
	client, err := NewClient(ctx, cfg)
	if err != nil {
		return reportProbe(out, st, "client", err), err
	}

	ids, err := client.ListAccessibleCustomers(ctx)
	if err != nil {
		return reportProbe(out, st, "accessible accounts", err), err
	}
	dashed := make([]string, len(ids))
	for i, id := range ids {
		dashed[i] = dashCustomerID(id)
	}
	probeOK(out, st, "accessible accounts", "%d (%s)", len(ids), strings.Join(dashed, ", "))

	if cfg.LoginCustomerID == "" {
		probeSkipped(out, st, "live query", "no login_customer_id set")
		return liveOK, nil
	}
	res, err := runAccounts(ctx, client, AccountsArgs{})
	if err != nil {
		return reportProbe(out, st, "live query", err), err
	}
	probeOK(out, st, "live query", "%d account(s) reachable under %s", len(res.CustomerIDs), dashCustomerID(cfg.LoginCustomerID))
	return liveOK, nil
}
