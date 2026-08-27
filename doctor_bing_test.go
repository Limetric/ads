package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bingLiveServer fakes the two endpoints Bing's live check probes.
func bingLiveServer(t *testing.T, campaignStatus int, campaignBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, bingAccountQueryRoute):
			_, _ = w.Write([]byte(bingAccountQueryBody))
		case strings.HasSuffix(r.URL.Path, "/AccountsInfo/Query"):
			_, _ = w.Write([]byte(`{"AccountsInfo":[{"Id":"123456","Name":"Main"},{"Id":"222","Name":"Second"}]}`))
		case strings.HasSuffix(r.URL.Path, "/Campaigns/QueryByAccountId"):
			if campaignStatus != 0 && campaignStatus != http.StatusOK {
				w.WriteHeader(campaignStatus)
			}
			_, _ = w.Write([]byte(campaignBody))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

func TestRunBingDoctorLive_Healthy(t *testing.T) {
	srv := bingLiveServer(t, http.StatusOK, `{"Campaigns":[{"Id":"1"},{"Id":"2"}]}`)
	defer srv.Close()

	var out bytes.Buffer
	cfg := &BingConfig{BaseURL: srv.URL, DefaultAccountID: "123456"}
	res, err := runBingDoctorLive(context.Background(), &out, styles{}, cfg)
	if err != nil || res != liveOK {
		t.Fatalf("healthy setup: got (%v, %v), want (liveOK, nil)", res, err)
	}
	got := out.String()
	if !strings.Contains(got, "123456 Main") {
		t.Errorf("accessible accounts not listed:\n%s", got)
	}
	if !strings.Contains(got, "2 campaign(s)") {
		t.Errorf("the live query result should be reported:\n%s", got)
	}
	if strings.Contains(got, "✗") || strings.Contains(got, "?") {
		t.Errorf("healthy setup reported a problem:\n%s", got)
	}
}

func TestRunBingDoctorLive_DefinitiveRejection(t *testing.T) {
	// Error 105 is what a token minted for the other environment produces —
	// definitive, and the user has to fix it.
	srv := bingLiveServer(t, http.StatusBadRequest,
		`{"OperationErrors":[{"Code":105,"ErrorCode":"InvalidCredentials","Message":"Authentication failed."}]}`)
	defer srv.Close()

	var out bytes.Buffer
	cfg := &BingConfig{BaseURL: srv.URL, DefaultAccountID: "123456"}
	res, err := runBingDoctorLive(context.Background(), &out, styles{}, cfg)
	if err == nil {
		t.Fatal("expected the rejection to surface")
	}
	if res != liveFailed {
		t.Errorf("verdict = %v, want liveFailed for a definitive 4xx", res)
	}
	if !strings.Contains(out.String(), "✗") {
		t.Errorf("a definitive failure should be marked ✗:\n%s", out.String())
	}
}

func TestRunBingDoctorLive_ThrottleIsInconclusive(t *testing.T) {
	bingThrottleBaseDelay = 0
	t.Cleanup(func() { bingThrottleBaseDelay = 2000000000 })

	srv := bingLiveServer(t, http.StatusBadRequest,
		`{"Errors":[{"Code":117,"ErrorCode":"CallRateExceeded","Message":"slow down"}]}`)
	defer srv.Close()

	var out bytes.Buffer
	cfg := &BingConfig{BaseURL: srv.URL, DefaultAccountID: "123456"}
	res, _ := runBingDoctorLive(context.Background(), &out, styles{}, cfg)
	// Being rate limited says nothing about whether the setup works, so it must
	// not be reported as a broken configuration.
	if res != liveInconclusive {
		t.Errorf("verdict = %v, want liveInconclusive for a throttle", res)
	}
}

func TestRunBingDoctorLive_SkipsQueryWithoutDefaultAccount(t *testing.T) {
	srv := bingLiveServer(t, http.StatusOK, `{"Campaigns":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	res, err := runBingDoctorLive(context.Background(), &out, styles{}, &BingConfig{BaseURL: srv.URL})
	if err != nil || res != liveOK {
		t.Fatalf("got (%v, %v)", res, err)
	}
	if !strings.Contains(out.String(), "set-account") {
		t.Errorf("skipping the query should say how to enable it:\n%s", out.String())
	}
}

func TestBingDoctor_ReportsUnconfigured(t *testing.T) {
	useTempState(t)
	clearBingEnv(t)
	var out bytes.Buffer
	res, err := bingDoctor(context.Background(), &out, true)
	if res != liveUnconfigured || err == nil {
		t.Fatalf("an empty setup should be liveUnconfigured: (%v, %v)", res, err)
	}
	// The one thing the user needs to know is what to run next.
	if !strings.Contains(err.Error(), "ads login bing") {
		t.Errorf("error = %v", err)
	}
}

func TestBingDeveloperTokenReport_WarnsAboutTheSandboxTokenInProduction(t *testing.T) {
	// The single most common sandbox mistake: the public sandbox token against
	// production, which fails as an opaque error 105.
	got := bingDeveloperTokenReport(styles{}, &BingConfig{DeveloperToken: bingSandboxDeveloperToken, Environment: bingEnvProduction})
	if !strings.Contains(got, "SANDBOX") {
		t.Errorf("report = %q, want it to flag the mismatch", got)
	}
	ok := bingDeveloperTokenReport(styles{}, &BingConfig{DeveloperToken: bingSandboxDeveloperToken, Environment: bingEnvSandbox})
	if strings.Contains(ok, "SANDBOX token, but") {
		t.Errorf("report = %q, the sandbox token in the sandbox is correct", ok)
	}
}

func TestConfiguredPlatforms(t *testing.T) {
	configured := &Platform{Name: "configured", Configured: func() bool { return true }}
	unconfigured := &Platform{Name: "unconfigured", Configured: func() bool { return false }}
	always := &Platform{Name: "no-opinion"}

	targets, skipped := configuredPlatforms([]*Platform{configured, unconfigured, always})
	if len(targets) != 2 || len(skipped) != 1 || skipped[0] != "unconfigured" {
		t.Fatalf("targets = %v, skipped = %v", targets, skipped)
	}
	// A brand-new user runs `ads doctor` precisely to be told what to set up;
	// reporting on nothing would answer nothing.
	targets, skipped = configuredPlatforms([]*Platform{unconfigured})
	if len(targets) != 1 || len(skipped) != 0 {
		t.Errorf("with nothing configured every platform is checked: %v / %v", targets, skipped)
	}
}
