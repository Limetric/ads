package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// doctorOffline backs the `--offline` flag: by default doctor makes real API
// calls to verify the setup can query; --offline skips them and only checks
// that credentials resolve (fast, deterministic, no network — for CI/offline).
var doctorOffline bool

// doctorCmd reports whether the setup works. By default it probes the API so
// "ready" means real queries succeed, not just that the credential strings are
// present. --offline reduces it to the cheap config-only check.
//
// The checks themselves belong to each platform (Platform.Doctor); this command
// only sequences them, prints the status line, and picks the exit code.
var doctorCmd = &cobra.Command{
	Use:   "doctor [platform]",
	Short: "Check that an ad platform's setup works (config + live API check)",
	Long:  "Verify that credentials resolve and that real queries succeed.\n\nWith no argument every configured platform is checked in turn; pass a platform\nname (e.g. `ads doctor google`) to check just one.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targets := platforms()
		var skipped []string
		if len(args) == 1 {
			p, err := lookupPlatform(args[0])
			if err != nil {
				return err
			}
			// Named explicitly: check it whether or not it is set up. "I haven't
			// configured this yet" is exactly what the user is asking about.
			targets = []*Platform{p}
		} else {
			targets, skipped = configuredPlatforms(targets)
		}
		if len(targets) == 0 {
			return errors.New("no ad platforms are compiled into this binary")
		}

		out := cmd.OutOrStdout()
		st := newStyles(out)
		defer func() {
			for _, name := range skipped {
				fmt.Fprintf(out, "\n%s\n", st.muted(fmt.Sprintf("skipped %s — not configured. Run `ads doctor %s` to see what it needs.", name, name)))
			}
		}()
		var worst *platformVerdict
		for i, p := range targets {
			if i > 0 {
				fmt.Fprintln(out)
			}
			if len(targets) > 1 {
				fmt.Fprintln(out, st.header(fmt.Sprintf("=== %s (%s) ===", p.Title, p.Name)))
			}
			res, err := p.Doctor(cmd.Context(), out, doctorOffline)
			fmt.Fprint(out, statusLine(st, res, err))
			if worst == nil || res.worseThan(worst.result) {
				worst = &platformVerdict{result: res, err: err}
			}
		}
		return worst.exit()
	},
}

// configuredPlatforms splits a platform list into the ones the user has set up
// and the names of the ones they haven't, so a plain `ads doctor` reports on
// the networks in use instead of failing over a platform nobody signed in to.
//
// When nothing is configured every platform is returned: a brand-new user runs
// `ads doctor` precisely to be told what to set up, and an empty report would
// answer nothing.
func configuredPlatforms(all []*Platform) (targets []*Platform, skipped []string) {
	for _, p := range all {
		if p.configured() {
			targets = append(targets, p)
		} else {
			skipped = append(skipped, p.Name)
		}
	}
	if len(targets) == 0 {
		return all, nil
	}
	return targets, skipped
}

// platformVerdict pairs a platform's doctor outcome with the error that
// explains it, so the command can exit on the worst result across platforms.
type platformVerdict struct {
	result liveResult
	err    error
}

// exit turns a verdict into the command's return value. Each result maps to a
// distinct exit code, and only liveUnconfigured surfaces its error to the user
// (the live probes already printed their own diagnostics).
func (v *platformVerdict) exit() error {
	switch v.result {
	case liveOK, liveOffline:
		return nil
	case liveUnconfigured:
		return v.err
	case liveInconclusive:
		return &exitErr{code: 2, err: v.err}
	default: // liveFailed
		return &exitErr{code: 1, err: v.err}
	}
}

// liveResult is the outcome of a platform's doctor check.
type liveResult int

const (
	liveOK           liveResult = iota // the API answered and real queries work
	liveOffline                        // --offline: credentials resolve, API not probed
	liveInconclusive                   // couldn't reach the API (transport/5xx) — setup unconfirmed, not broken
	liveFailed                         // the API definitively rejected us (4xx) — setup is broken
	liveUnconfigured                   // credentials didn't even resolve
)

// worseThan orders results by severity so a multi-platform run exits on its
// worst outcome. The iota order is already severity order.
func (r liveResult) worseThan(other liveResult) bool { return r > other }

// statusLine renders the verdict a platform's doctor reached. The verdict word
// carries the colour — green ready, yellow inconclusive, red not ready — so the
// outcome is legible before the sentence explaining it is read.
func statusLine(st styles, res liveResult, err error) string {
	line := func(verdict, rest string) string {
		return fmt.Sprintf("\n%s %s%s\n", st.muted("status:"), verdict, rest)
	}
	switch res {
	case liveOK:
		return line(st.success("ready"), " (live check passed)")
	case liveOffline:
		return line(st.success("ready"), " — credentials resolve (offline check). Run `ads doctor` to verify against the API.")
	case liveUnconfigured:
		return line(st.failure("NOT READY"), fmt.Sprintf(" — %v", err))
	case liveInconclusive:
		return line(st.warning("INCONCLUSIVE"), " — credentials resolve, but the API couldn't be reached (network/transient). Setup unconfirmed, not necessarily broken.")
	default: // liveFailed
		return line(st.failure("NOT READY"), " — the API rejected the request (see above)")
	}
}

// definitiveAPIError is implemented by a platform's API error type when it can
// tell a definitive rejection (4xx — the request or credentials are wrong) from
// a transient one. It keeps liveVerdictFor free of any platform's error types.
type definitiveAPIError interface {
	isClientError() bool
}

// liveVerdictFor classifies a live-probe error. A 4xx from the platform's API is
// definitive — the request or credentials are wrong (liveFailed). So is a 4xx
// from the OAuth token endpoint (oauth2.RetrieveError): invalid_grant means
// the refresh token is revoked or mistyped, which used to be misreported as
// "inconclusive — not necessarily broken" (issue #11). Anything else — a 5xx,
// a connection failure — means we simply couldn't get a verdict
// (liveInconclusive), which must not be reported as a broken setup.
func liveVerdictFor(err error) liveResult {
	if err == nil {
		return liveOK
	}
	var apiErr definitiveAPIError
	if errors.As(err, &apiErr) && apiErr.isClientError() {
		return liveFailed
	}
	var oauthErr *oauth2.RetrieveError
	if errors.As(err, &oauthErr) && oauthErr.Response != nil {
		switch oauthErr.Response.StatusCode {
		// Definitive credential failures. 429/5xx from the token endpoint
		// stay inconclusive — rate limiting is not a broken setup.
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
			return liveFailed
		}
	}
	return liveInconclusive
}

// doctorFieldWidth is the column doctor's credential summary lines its values
// up in; probeLabelWidth is the column the probe markers (✓, ✗, ?) start in, so
// they line up down the report whichever way each probe went.
const (
	doctorFieldWidth = 20
	probeLabelWidth  = 22
)

// probeOK prints a passing probe line: a green ✓ and what the probe found.
func probeOK(out io.Writer, st styles, label, format string, args ...any) {
	fmt.Fprintf(out, "%s%s %s\n", st.field(label, probeLabelWidth), st.success("✓"), fmt.Sprintf(format, args...))
}

// probeSkipped prints a probe that had no reason to run — not a failure, so it
// is dimmed rather than marked.
func probeSkipped(out io.Writer, st styles, label, reason string) {
	fmt.Fprintf(out, "%s%s\n", st.field(label, probeLabelWidth), st.muted("skipped ("+reason+")"))
}

// reportProbe prints a failed probe line — a red ✗ for a definitive failure, a
// yellow ? for an inconclusive one — and returns the classification. label is
// the bare probe name; the marker column is applied here.
func reportProbe(out io.Writer, st styles, label string, err error) liveResult {
	verdict := liveVerdictFor(err)
	marker := st.warning("?")
	prefix := "could not reach the API: "
	if verdict == liveFailed {
		marker = st.failure("✗")
		prefix = ""
	}
	fmt.Fprintf(out, "%s%s %s%v\n", st.field(label, probeLabelWidth), marker, prefix, err)
	return verdict
}

func present(s string) string {
	if s == "" {
		return "MISSING"
	}
	return "set"
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
