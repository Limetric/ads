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
	fmt.Fprintf(out, "environment:        %s\n", cfg.Environment)
	fmt.Fprintf(out, "api host:           %s\n", bingCampaignService.url(cfg, ""))
	fmt.Fprintf(out, "developer token:    %s\n", bingDeveloperTokenReport(cfg))
	fmt.Fprintf(out, "client id:          %s\n", present(cfg.ClientID))
	fmt.Fprintf(out, "client secret:      %s\n", bingClientSecretReport(cfg))
	fmt.Fprintf(out, "token store:        %s\n", store.location())
	fmt.Fprintf(out, "saved sign-in:      %s\n", store.describe(bingTokenPolicy))
	fmt.Fprintf(out, "manager (customer): %s\n", bingManagerReport(cfg))
	fmt.Fprintf(out, "default account:    %s\n", orNone(cfg.DefaultAccountID))
	if err := cfg.validate(refreshToken); err != nil {
		return liveUnconfigured, err
	}
	if offline {
		return liveOffline, nil
	}
	fmt.Fprintln(out)
	return runBingDoctorLive(ctx, out, cfg)
}

// bingDeveloperTokenReport says where the developer token came from. Reporting
// the sandbox token as merely "set" would hide the most common sandbox mistake
// — running against production with it, which fails as error 105.
func bingDeveloperTokenReport(cfg *BingConfig) string {
	switch {
	case cfg.DeveloperToken == "":
		return present("")
	case cfg.DeveloperToken == bingSandboxDeveloperToken && cfg.Environment == bingEnvSandbox:
		return "set (the universal sandbox token)"
	case cfg.DeveloperToken == bingSandboxDeveloperToken:
		return "set to the SANDBOX token, but the environment is " + cfg.Environment + " — production needs its own developer token"
	case cfg.Environment == bingEnvSandbox:
		// The mirror image, and the one a half-completed environment switch
		// leaves behind: a production token against sandbox hosts fails as
		// error 105, which reads like a broken sign-in.
		return "set, but it is not the universal sandbox token (" + bingSandboxDeveloperToken + ") — the sandbox rejects a production token"
	default:
		return "set"
	}
}

// bingManagerReport describes the manager account. Unset is not "missing":
// the client reads it from the ad account on first use, so saying "(none)"
// would report a working setup as an incomplete one.
func bingManagerReport(cfg *BingConfig) string {
	if cfg.CustomerID == "" {
		return "(not set — discovered from the account on first use; set BING_ADS_CUSTOMER_ID to pin it)"
	}
	return cfg.CustomerID
}

// bingClientSecretReport describes the client secret, which is optional and
// usually absent: `ads login bing` registers a public client and uses PKCE.
func bingClientSecretReport(cfg *BingConfig) string {
	if cfg.ClientSecret == "" {
		return "(none — public client, PKCE)"
	}
	return "set (web application client)"
}

// runBingDoctorLive makes real API calls to prove the setup works, printing a
// line per probe. It runs two probes because they fail independently:
//
//  1. an account list, which needs only the sign-in and the developer token, so
//     it confirms credentials are valid and shows what they reach.
//  2. a campaign read on the default account — what every read command does. It
//     is the one that catches a default account the sign-in cannot actually
//     access, which probe 1 has no opinion about.
func runBingDoctorLive(ctx context.Context, out io.Writer, cfg *BingConfig) (liveResult, error) {
	client, err := NewBingClient(ctx, cfg)
	if err != nil {
		return reportProbe(out, "client:              ", err), err
	}

	accounts, err := client.ListAccounts(ctx, false)
	if err != nil {
		return reportProbe(out, "accessible accounts: ", err), err
	}
	fmt.Fprintf(out, "accessible accounts:  ✓ %d (%s)\n", len(accounts), bingAccountSummary(accounts))

	if cfg.DefaultAccountID == "" {
		fmt.Fprintf(out, "live query:           skipped (no default account set — `ads config %s set-account <id>`)\n", bingPlatformName)
		return liveOK, nil
	}
	campaigns, err := client.ListCampaigns(ctx, cfg.DefaultAccountID)
	if err != nil {
		return reportProbe(out, "live query:          ", err), err
	}
	fmt.Fprintf(out, "live query:           ✓ %d campaign(s) in account %s\n", len(campaigns), cfg.DefaultAccountID)
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
