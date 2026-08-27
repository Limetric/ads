package main

import (
	"context"
	"fmt"
	"io"
)

// bingDoctor is the Bing platform's Platform.Doctor hook: it prints the
// resolved credential summary, then (unless offline) probes the live API.
func bingDoctor(ctx context.Context, out io.Writer, offline bool) (liveResult, error) {
	cfg, err := loadBingConfig(configPath)
	if err != nil {
		return liveUnconfigured, err
	}
	// Resolve before reporting so what doctor prints is what a real command
	// would use.
	refreshToken, err := cfg.resolveRefreshToken()
	if err != nil {
		return liveUnconfigured, err
	}
	store := describeTokenStore(bingTokenPolicy.Platform)
	st := newStyles(out)
	field := func(label, value string) { fmt.Fprintf(out, "%s%s\n", st.field(label, doctorFieldWidth), value) }
	field("environment", cfg.Environment)
	field("api host", bingCampaignService.url(cfg, ""))
	field("developer token", bingDeveloperTokenReport(st, cfg))
	field("client id", st.presence(cfg.ClientID))
	field("client secret", bingClientSecretReport(st, cfg))
	field("token store", store.location())
	field("saved sign-in", store.describe(bingTokenPolicy))
	field("manager (customer)", bingManagerReport(st, cfg))
	field("default account", st.optional(cfg.DefaultAccountID))
	if err := cfg.validate(refreshToken); err != nil {
		return liveUnconfigured, err
	}
	if offline {
		return liveOffline, nil
	}
	fmt.Fprintln(out)
	return runBingDoctorLive(ctx, out, st, cfg)
}

// bingDeveloperTokenReport says where the developer token came from. Reporting
// the sandbox token as merely "set" would hide the most common sandbox mistake
// — running against production with it, which fails as error 105.
func bingDeveloperTokenReport(st styles, cfg *BingConfig) string {
	switch {
	case cfg.DeveloperToken == "":
		return st.presence("")
	case cfg.DeveloperToken == bingSandboxDeveloperToken && cfg.Environment == bingEnvSandbox:
		return st.success("set") + " " + st.muted("(the universal sandbox token)")
	case cfg.DeveloperToken == bingSandboxDeveloperToken:
		return st.warning("set to the SANDBOX token, but the environment is " + cfg.Environment + " — production needs its own developer token")
	default:
		return st.success("set")
	}
}

// bingManagerReport describes the manager account. Unset is not "missing":
// the client reads it from the ad account on first use, so saying "(none)"
// would report a working setup as an incomplete one.
func bingManagerReport(st styles, cfg *BingConfig) string {
	if cfg.CustomerID == "" {
		return st.muted("(not set — discovered from the account on first use; set BING_ADS_CUSTOMER_ID to pin it)")
	}
	return cfg.CustomerID
}

// bingClientSecretReport describes the client secret, which is optional and
// usually absent: `ads login bing` registers a public client and uses PKCE.
func bingClientSecretReport(st styles, cfg *BingConfig) string {
	if cfg.ClientSecret == "" {
		return st.muted("(none — public client, PKCE)")
	}
	return st.success("set") + " " + st.muted("(web application client)")
}

// runBingDoctorLive makes real API calls to prove the setup works, printing a
// line per probe. It runs two probes because they fail independently:
//
//  1. an account list, which needs only the sign-in and the developer token, so
//     it confirms credentials are valid and shows what they reach.
//  2. a campaign read on the default account — what every read command does. It
//     is the one that catches a default account the sign-in cannot actually
//     access, which probe 1 has no opinion about.
func runBingDoctorLive(ctx context.Context, out io.Writer, st styles, cfg *BingConfig) (liveResult, error) {
	client, err := NewBingClient(ctx, cfg)
	if err != nil {
		return reportProbe(out, st, "client", err), err
	}

	accounts, err := client.ListAccounts(ctx, false)
	if err != nil {
		return reportProbe(out, st, "accessible accounts", err), err
	}
	probeOK(out, st, "accessible accounts", "%d (%s)", len(accounts), bingAccountSummary(accounts))

	if cfg.DefaultAccountID == "" {
		probeSkipped(out, st, "live query", fmt.Sprintf("no default account set — `ads config %s set-account <id>`", bingPlatformName))
		return liveOK, nil
	}
	campaigns, err := client.ListCampaigns(ctx, cfg.DefaultAccountID)
	if err != nil {
		return reportProbe(out, st, "live query", err), err
	}
	probeOK(out, st, "live query", "%d campaign(s) in account %s", len(campaigns), cfg.DefaultAccountID)
	return liveOK, nil
}

// bingAccountSummary renders the reachable accounts compactly, and caps the
// list: a manager account can hold hundreds, and doctor is a status report.
func bingAccountSummary(accounts []BingAccountInfo) string {
	const max = 5
	if len(accounts) == 0 {
		return "none — this sign-in cannot reach any ad account"
	}
	out := ""
	for i, a := range accounts {
		if i == max {
			out += fmt.Sprintf(", … and %d more", len(accounts)-max)
			break
		}
		if i > 0 {
			out += ", "
		}
		out += a.ID
		if a.Name != "" {
			out += " " + a.Name
		}
	}
	return out
}
