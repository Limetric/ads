package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A buffer is not a terminal, so nothing that writes to one — every test in
// this package, and every piped or redirected run — may get escape codes.
func TestNewStylesDisabledForNonTerminal(t *testing.T) {
	var buf bytes.Buffer
	st := newStyles(&buf)
	if st.enabled {
		t.Fatal("styles enabled for a bytes.Buffer")
	}
	for name, got := range map[string]string{
		"header":  st.header("x"),
		"accent":  st.accent("x"),
		"muted":   st.muted("x"),
		"prompt":  st.prompt("x"),
		"success": st.success("x"),
		"warning": st.warning("x"),
		"failure": st.failure("x"),
		"url":     st.url("x"),
	} {
		if got != "x" {
			t.Errorf("%s(%q) = %q, want unchanged", name, "x", got)
		}
	}
}

// A regular file isn't a character device either — `ads doctor > report.txt`
// must leave a file with no escapes in it.
func TestColorEnabledFalseForRegularFile(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	f, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if colorEnabled(f) {
		t.Error("colorEnabled = true for a regular file")
	}
}

func TestColorEnabledOptOutAndForce(t *testing.T) {
	var buf bytes.Buffer
	tests := []struct {
		name        string
		noColorFlag bool
		noColorEnv  string
		term        string
		clicolor    string
		wantForBuf  bool
	}{
		{name: "off by default for a buffer"},
		{name: "CLICOLOR_FORCE overrides the terminal check", clicolor: "1", wantForBuf: true},
		{name: "CLICOLOR_FORCE=0 is not a force", clicolor: "0"},
		{name: "NO_COLOR beats CLICOLOR_FORCE", noColorEnv: "1", clicolor: "1"},
		{name: "--no-color beats CLICOLOR_FORCE", noColorFlag: true, clicolor: "1"},
		{name: "TERM=dumb beats CLICOLOR_FORCE", term: "dumb", clicolor: "1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColorEnv)
			t.Setenv("TERM", tc.term)
			t.Setenv("CLICOLOR_FORCE", tc.clicolor)
			saved := noColor
			noColor = tc.noColorFlag
			defer func() { noColor = saved }()
			if got := colorEnabled(&buf); got != tc.wantForBuf {
				t.Errorf("colorEnabled = %v, want %v", got, tc.wantForBuf)
			}
		})
	}
}

func TestStylesWrap(t *testing.T) {
	st := styles{enabled: true}
	if got, want := st.success("ok"), "\033[32mok\033[0m"; got != want {
		t.Errorf("success = %q, want %q", got, want)
	}
	// An empty string gets no escapes: a style must never emit a stray reset
	// around nothing (an empty value renders as an empty column, not a blot).
	if got := st.failure(""); got != "" {
		t.Errorf("failure(\"\") = %q, want \"\"", got)
	}
}

// The field helper owns the column widths the reports line up on, so it is
// checked against the exact plain-text shape those reports used to hard-code.
func TestStylesFieldPadsToWidth(t *testing.T) {
	st := styles{}
	if got, want := st.field("base URL", doctorFieldWidth), "base URL:           "; got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	if got, want := st.field("login customer id", doctorFieldWidth), "login customer id:  "; got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	if got, want := st.field("default account id", configFieldWidth), "default account id:   "; got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	// A label longer than the column still gets its colon and one value —
	// truncating a label would make the report lie about which key it names.
	if got, want := st.field("an unusually long label", 4), "an unusually long label:"; got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	// The padding lives inside the style, which must not leak a reset between
	// the label and the value.
	styled := styles{enabled: true}.field("base URL", doctorFieldWidth)
	if !strings.HasSuffix(styled, "  \033[0m") {
		t.Errorf("styled field = %q, want the padding inside the escape", styled)
	}
}

// The value helpers must agree with the plain functions they wrap, so turning
// colour off cannot change what the report says.
func TestStylesValueHelpersMatchPlainText(t *testing.T) {
	st := styles{}
	for _, tc := range []struct {
		name, got, want string
	}{
		{"presence set", st.presence("token"), present("token")},
		{"presence missing", st.presence(""), present("")},
		{"optional set", st.optional("123"), orNone("123")},
		{"optional none", st.optional(""), orNone("")},
		{"secret set", st.secret("abcdefghijkl"), redactSecret("abcdefghijkl")},
		{"secret unset", st.secret(""), redactSecret("")},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestStatusLinePlainText(t *testing.T) {
	st := styles{}
	tests := []struct {
		res  liveResult
		want string
	}{
		{liveOK, "\nstatus: ready (live check passed)\n"},
		{liveFailed, "\nstatus: NOT READY — the API rejected the request (see above)\n"},
	}
	for _, tc := range tests {
		if got := statusLine(st, tc.res, nil); got != tc.want {
			t.Errorf("statusLine(%v) = %q, want %q", tc.res, got, tc.want)
		}
	}
}

// Probe lines line their markers up in one column whichever way each probe
// went, so a report reads as a list rather than a ragged edge.
func TestProbeLinesAlign(t *testing.T) {
	var buf bytes.Buffer
	st := styles{}
	probeOK(&buf, st, "accessible accounts", "%d accounts", 3)
	reportProbe(&buf, st, "live query", &apiStatusError{status: 403, msg: "forbidden"})
	probeSkipped(&buf, st, "client", "nothing to do")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), buf.String())
	}
	for _, want := range []string{
		"accessible accounts:  ✓ 3 accounts",
		"live query:           ✗ ",
		"client:               skipped (nothing to do)",
	} {
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no line starting %q in:\n%s", want, buf.String())
		}
	}
}

func TestColorizeAuditEntryPlainAndStyled(t *testing.T) {
	const entry = "2026-08-27T10:00:00Z platform=google tool=set_campaign_budget account=1234567890 ops=1 applied=true token=abc"
	if got := colorizeAuditEntry(styles{}, entry); got != entry {
		t.Errorf("plain entry changed:\n got %q\nwant %q", got, entry)
	}
	styled := colorizeAuditEntry(styles{enabled: true}, entry)
	if !strings.Contains(styled, "\033[32mapplied=true\033[0m") {
		t.Errorf("applied=true not highlighted: %q", styled)
	}
	failed := strings.Replace(entry, "applied=true", "applied=false", 1)
	if !strings.Contains(colorizeAuditEntry(styles{enabled: true}, failed), "\033[31mapplied=false\033[0m") {
		t.Error("applied=false not highlighted")
	}
	// A line that isn't in the expected shape is history we must not mangle.
	for _, odd := range []string{"", "some free-form line", "2026-08-27T10:00:00Z"} {
		if got := colorizeAuditEntry(styles{enabled: true}, odd); got != odd {
			t.Errorf("colorizeAuditEntry(%q) = %q, want unchanged", odd, got)
		}
	}
}
