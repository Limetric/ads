package main

import (
	"fmt"
	"io"
	"os"
)

// Terminal colour for the human-facing commands — `login`, `doctor`,
// `config show`. Machine-facing output is deliberately untouched: tool results
// print as JSON for jq, and MCP never goes through here at all.
//
// The palette is small on purpose, and matches the one pgferry uses, so the
// two tools read as a set:
//
//	header   bold cyan     section titles ("=== Google Ads (google) ===")
//	accent   bold blue     things to act on — URLs, "For CI / MCP hosts, set:"
//	muted    dim           field labels, hints, placeholders like "(none)"
//	prompt   bold          the question a prompt is asking
//	success  green         ✓, "set", a live probe that answered
//	warning  yellow        ?, a setup that works but wants attention
//	failure  red           ✗, "MISSING", a definitive rejection
//
// Colour is opt-out (NO_COLOR, TERM=dumb, --no-color), opt-in past the terminal
// check (CLICOLOR_FORCE), and otherwise applied only when the destination is a
// terminal — so a piped or redirected run, and every test writing to a
// bytes.Buffer, gets exactly the plain text it did before.

// noColor backs the global --no-color flag.
var noColor bool

// styles renders text with ANSI escapes. The zero value writes plain text,
// which is what every non-terminal destination gets.
type styles struct{ enabled bool }

// newStyles picks the styling for a destination: colour only for a terminal the
// user hasn't opted out of.
func newStyles(w io.Writer) styles {
	return styles{enabled: colorEnabled(w)}
}

// colorEnabled reports whether w should receive ANSI escapes. A writer that is
// not an *os.File (a buffer, a pipe wrapper) can't be a terminal, so it never
// does — unless CLICOLOR_FORCE says otherwise, which is how you keep the colour
// when piping into a pager that renders it (`ads doctor | less -R`).
func colorEnabled(w io.Writer) bool {
	if noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// wrap applies an escape code and resets afterwards. Empty text is left alone
// so a style never emits a stray reset around nothing.
func (s styles) wrap(code, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	return code + text + "\033[0m"
}

func (s styles) header(text string) string  { return s.wrap("\033[1;36m", text) }
func (s styles) accent(text string) string  { return s.wrap("\033[1;34m", text) }
func (s styles) muted(text string) string   { return s.wrap("\033[2m", text) }
func (s styles) prompt(text string) string  { return s.wrap("\033[1m", text) }
func (s styles) success(text string) string { return s.wrap("\033[32m", text) }
func (s styles) warning(text string) string { return s.wrap("\033[33m", text) }
func (s styles) failure(text string) string { return s.wrap("\033[31m", text) }

// field renders "label:" padded to width so a column of values lines up. The
// padding is inside the style, which is invisible either way and keeps the
// caller from having to count spaces.
func (s styles) field(label string, width int) string {
	return s.muted(fmt.Sprintf("%-*s", width, label+":"))
}

// presence renders whether a required credential resolved: green "set", red
// "MISSING". It is the styled form of present().
func (s styles) presence(v string) string {
	if v == "" {
		return s.failure(present(v))
	}
	return s.success(present(v))
}

// optional renders a value that is allowed to be unset, dimming the "(none)"
// placeholder so a column of real values still reads first.
func (s styles) optional(v string) string {
	if v == "" {
		return s.muted(orNone(v))
	}
	return v
}

// secret renders a credential for display without revealing it, dimming the
// "(not set)" placeholder. It is the styled form of redactSecret().
func (s styles) secret(v string) string {
	if v == "" {
		return s.muted(redactSecret(v))
	}
	return s.success(redactSecret(v))
}

// url highlights a link the user is expected to open or copy.
func (s styles) url(u string) string { return s.accent(u) }
