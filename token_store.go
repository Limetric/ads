package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Refresh tokens live in a per-platform token store — one 0600 JSON file per
// platform under stateDir()/tokens — and not in config.toml or the environment.
//
// The reason is rotation. Google's refresh token is static, so a refresh token
// pasted into an env var works forever. Platforms like Microsoft and Pinterest
// mint a *new* refresh token on every refresh and invalidate the old one, and a
// process cannot write back to its own environment: the first run would refresh
// to RT2, the next run would present the dead RT1, and sign-in would fail. An
// env-supplied rotating token is not a degraded setup, it is a broken one.
//
// Rather than special-case rotation, every platform reads and writes the same
// store, so there is one auth path to maintain as platforms three and four
// arrive. GOOGLE_ADS_REFRESH_TOKEN and config.toml's refresh_token are still
// accepted as a one-time *seed* into the store, with a deprecation warning,
// through the 0.x line — no flag day for existing MCP host configs or CI.

// tokenStoreEnv points ads at a different store directory. Containers and CI
// mount a writable volume here; it is also how two configs that sign in as
// different users keep separate tokens.
const tokenStoreEnv = "GOADS_TOKEN_STORE"

// storedToken is one platform's persisted OAuth grant. Only the refresh token
// is kept: access tokens are short-lived and cheap to re-mint, and not writing
// one on every refresh keeps the common path read-only.
type storedToken struct {
	// RefreshToken is the long-lived grant access tokens are minted from.
	RefreshToken string `json:"refresh_token"`
	// UpdatedAt is when this value was written — the token age `doctor` reports.
	UpdatedAt time.Time `json:"updated_at"`
	// Source records where the value came from ("ads login google", a token
	// refresh, or the deprecated variable it was seeded from), so a user who
	// finds an unexpected token can tell how it got there.
	Source string `json:"source,omitempty"`
	// ClientID is the OAuth client this grant was issued to, when known. A
	// refresh token is only usable with the client that minted it, so recording
	// it lets a mismatch be named instead of surfacing as a bare invalid_grant
	// — see checkClientBinding. Empty for tokens seeded from a deprecated
	// source, where the pairing was never established.
	ClientID string `json:"client_id,omitempty"`
}

// checkClientBinding warns when the saved sign-in belongs to a different OAuth
// client than the one now configured, which makes the pair unusable.
//
// Two setups land here. One config file's sign-in reached the store and another
// config, pointing at a different OAuth client, then reads it — the store has a
// single slot per platform, so alternating `--config` profiles share it unless
// they also set GOADS_TOKEN_STORE. Or a sign-in half-committed: the client
// credentials were written and the token was not.
//
// It warns rather than fails: the store is still the best token available, and
// the request that follows will say what the provider thinks. The point is that
// the user reads this first.
func checkClientBinding(policy tokenPolicy, stored *storedToken, configuredClientID string) {
	if stored == nil || stored.ClientID == "" || configuredClientID == "" || stored.ClientID == configuredClientID {
		return
	}
	warnOnce("the saved %s sign-in was issued to OAuth client %s, but %s is configured — a refresh token only works with the client that minted it. Run `%s` to sign in with the configured client, or point %s at a store of its own.",
		policy.Platform, stored.ClientID, configuredClientID, policy.loginCommand(), tokenStoreEnv)
}

// --- store paths and files ---

// validPlatformName reports whether name is safe to use as a store filename.
// Platform names are compile-time constants, but the store path is the one
// place a name reaches the filesystem, so it is checked rather than trusted.
func validPlatformName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// tokenStoreDir resolves the store directory without creating it.
func tokenStoreDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(tokenStoreEnv)); v != "" {
		return v, nil
	}
	state, err := stateDirPath()
	if err != nil {
		return "", fmt.Errorf("no usable config directory (%v) — set HOME/XDG_CONFIG_HOME, or point %s at a writable directory", err, tokenStoreEnv)
	}
	return filepath.Join(state, "tokens"), nil
}

// tokenStorePath returns the file holding one platform's token.
func tokenStorePath(platform string) (string, error) {
	if !validPlatformName(platform) {
		return "", fmt.Errorf("invalid platform name %q", platform)
	}
	dir, err := tokenStoreDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, platform+".json"), nil
}

// readStoredToken loads a platform's saved token. A store file that does not
// exist yet is not an error — it means nobody has signed in — and neither is
// one holding an empty token; both return (nil, nil).
func readStoredToken(platform string) (*storedToken, error) {
	path, err := tokenStorePath(platform)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read token store %q: %w", path, err)
	}
	var t storedToken
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("corrupt token store %q: %w — delete the file and run `ads login %s`", path, err, platform)
	}
	if t.RefreshToken == "" {
		return nil, nil
	}
	return &t, nil
}

// writeStoredToken persists a platform's token at 0600 in a 0700 directory.
// Its errors always name the path: a store that cannot be written is a
// filesystem problem, and reporting it as one is the difference between a
// one-line fix and an auth failure nobody can explain.
func writeStoredToken(platform string, t *storedToken) error {
	path, err := tokenStorePath(platform)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token store directory %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token store %q: %w", path, err)
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write token store %q: %w", path, err)
	}
	return nil
}

// --- resolution: store first, deprecated seeds once ---

// tokenPolicy is what the shared auth path needs to know about one platform's
// refresh token. A platform supplies one (see googleTokenPolicy) instead of
// this file knowing which platforms exist.
type tokenPolicy struct {
	// Platform is the namespace name, and the store slot's filename.
	Platform string
	// Rotates is true when the platform issues a new refresh token on every
	// refresh and invalidates the old one. It decides how loudly a store we
	// cannot write fails: for a static token an unwritable store costs nothing,
	// but a rotating platform loses its only valid credential.
	Rotates bool
}

// loginCommand is the command that re-authorizes this platform, quoted into
// error messages so every auth failure ends with the fix.
func (p tokenPolicy) loginCommand() string { return "ads login " + p.Platform }

// rotationStoreError explains a store that cannot hold a rotating platform's
// credential. It is the same failure whether it is found before a migration or
// before an exchange, so it reads the same way in both.
func (p tokenPolicy) rotationStoreError(err error) error {
	return fmt.Errorf("%s issues a new refresh token on every refresh, so it must be saved, but the token store cannot be written: %w — make that path writable, or set %s to a writable directory",
		p.Platform, err, tokenStoreEnv)
}

// requireWritableStore reports why a platform's store cannot be written, or nil.
//
// Two callers have to know before they act rather than after. A rotating
// platform must not begin an exchange it cannot record — the provider kills the
// old refresh token the moment the new one is issued, so discovering the
// problem afterwards means the sign-in is already gone. And a sign-in must not
// overwrite the client half of a credential pair whose token half it cannot
// then commit.
func requireWritableStore(platform string) error {
	path, err := tokenStorePath(platform)
	if err != nil {
		return err
	}
	// The write follows symlinks, so the probe has to as well — otherwise a
	// store file linked into an unwritable directory passes here and fails
	// later, which for a rotating platform is after the token is already spent.
	if err := probeWritable(filepath.Dir(resolveWritePath(path))); err != nil {
		return fmt.Errorf("token store %q is not writable: %w", path, err)
	}
	return nil
}

// tokenSeed is a deprecated place a refresh token may still be supplied from —
// an environment variable or a config.toml key. Seeds are accepted once, copied
// into the store, and ignored from then on.
type tokenSeed struct {
	// Value is the refresh token found there, empty when the source is unset.
	Value string
	// Origin names the source in warnings ("GOOGLE_ADS_REFRESH_TOKEN").
	Origin string
	// Retire removes the value from its source once it is safely in the store,
	// so the credential ends up with exactly one home. Nil when the source
	// cannot be rewritten (a variable in the caller's environment).
	Retire func() error
}

// resolveRefreshToken returns the refresh token a platform should sign in with.
//
// The store wins whenever it holds one. Otherwise the first non-empty seed is
// migrated into the store and used, and every seed that can be retired is
// cleared. An empty return value means "not signed in" — the caller's
// credential validation reports that, since it can name the platform's setup
// command.
//
// Seeds must be in precedence order (environment before config file, matching
// how the rest of the configuration overlays). clientID is the OAuth client the
// caller is configured with, used to catch a saved sign-in that belongs to a
// different one; pass "" when it is not known.
func resolveRefreshToken(policy tokenPolicy, clientID string, seeds []tokenSeed) (string, error) {
	seeds = presentSeeds(seeds)

	path, err := tokenStorePath(policy.Platform)
	if err != nil {
		// There is nowhere to persist anything at all — no HOME in a container,
		// typically. A static token supplied by the environment still works, so
		// setups that work today keep working; a rotating one cannot.
		if policy.Rotates || len(seeds) == 0 {
			return "", fmt.Errorf("token store unavailable: %w", err)
		}
		warnOnce("token store unavailable (%v) — using %s for this run, without saving it", err, seeds[0].Origin)
		return seeds[0].Value, nil
	}

	stored, err := readStoredToken(policy.Platform)
	if err != nil {
		return "", err
	}
	if stored != nil {
		// Reading a saved token needs no write, which is what lets a signed-in
		// static platform run against a read-only store. A rotating one is the
		// opposite case: using this token is what destroys it, so the store has
		// to be writable before the token leaves this function.
		if policy.Rotates {
			if err := requireWritableStore(policy.Platform); err != nil {
				return "", policy.rotationStoreError(err)
			}
		}
		checkClientBinding(policy, stored, clientID)
		// Retried on every run, not just at migration: a retirement that failed
		// once — a config file that was read-only that day — still completes
		// once the file becomes writable, instead of leaving the credential
		// duplicated forever.
		retireDuplicates(stored.RefreshToken, seeds)
		reportIgnoredSeeds(policy, path, stored.RefreshToken, seeds)
		return stored.RefreshToken, nil
	}
	if len(seeds) == 0 {
		return "", nil
	}

	seed := seeds[0]
	// No ClientID: a seeded token carries no record of the client it was minted
	// for, and guessing the configured one would invent a binding we cannot
	// vouch for.
	err = writeStoredToken(policy.Platform, &storedToken{
		RefreshToken: seed.Value,
		UpdatedAt:    time.Now().UTC(),
		Source:       seed.Origin,
	})
	if err != nil {
		if policy.Rotates {
			return "", policy.rotationStoreError(err)
		}
		// A static refresh token survives not being saved, so don't break a
		// setup that works today over it — but say so, with the path.
		warnOnce("could not save the refresh token (%v) — using %s for this run. Set %s to a writable directory to save it.", err, seed.Origin, tokenStoreEnv)
		return seed.Value, nil
	}

	warnOnce("%s is deprecated — its value has been saved to %s and is used from now on. Remove %s; run `%s` to replace the saved sign-in.",
		seed.Origin, path, seed.Origin, policy.loginCommand())
	retireDuplicates(seed.Value, seeds)
	reportIgnoredSeeds(policy, path, seed.Value, seeds[1:])
	return seed.Value, nil
}

// retireDuplicates clears the deprecated copies that hold exactly the token
// already in the store.
//
// The equality check is the whole point. A seed holding a *different* value is
// not a stale duplicate, it is a separate credential — quite possibly the only
// copy of a working one, since the seed that won is merely the higher-precedence
// source, not the verified-good one. Deleting it would leave the user unable to
// recover by removing whichever source outranked it.
func retireDuplicates(authoritative string, seeds []tokenSeed) {
	for _, s := range seeds {
		if s.Retire == nil || s.Value != authoritative {
			continue
		}
		if err := s.Retire(); err != nil {
			warnOnce("could not remove the deprecated %s (%v) — delete it by hand; ads ignores it from now on.", s.Origin, err)
		}
	}
}

// reportIgnoredSeeds warns about deprecated sources that are still set, saying
// which case the user is in: a harmless leftover copy, or a second, different
// credential that is quietly not being used.
func reportIgnoredSeeds(policy tokenPolicy, path, authoritative string, seeds []tokenSeed) {
	for _, s := range seeds {
		switch {
		case s.Value == authoritative && s.Retire != nil:
			// retireDuplicates handles it, and warns if it could not.
		case s.Value == authoritative:
			warnOnce("%s is deprecated — its value is already saved in %s, which is what ads uses. Remove %s.", s.Origin, path, s.Origin)
		default:
			warnOnce("%s is deprecated and holds a different token than the saved %s sign-in in %s — the saved sign-in is the one ads uses. Remove %s, or run `%s` to replace the saved sign-in.",
				s.Origin, policy.Platform, path, s.Origin, policy.loginCommand())
		}
	}
}

// presentSeeds drops the sources that are unset and trims the rest, preserving
// precedence order. Trimming matters: a token pasted into a CI secret or an MCP
// host config picks up whitespace easily, and a stray newline would be saved
// into the store and then rejected by the token endpoint forever.
func presentSeeds(seeds []tokenSeed) []tokenSeed {
	present := make([]tokenSeed, 0, len(seeds))
	for _, s := range seeds {
		if s.Value = strings.TrimSpace(s.Value); s.Value != "" {
			present = append(present, s)
		}
	}
	return present
}

// --- writing rotated tokens back ---

// persistingTokenSource wraps a platform's oauth2.TokenSource and saves the
// refresh token whenever the platform hands back a new one. This is what makes
// a rotating platform survive across separate process invocations: oauth2
// caches the access token in memory, but the rotated refresh token only lives
// as long as the process unless it reaches the store.
//
// Google never trips this — its refresh responses carry no refresh_token and
// oauth2 carries the old one forward unchanged, so the common path never
// writes.
//
// The mutex serializes rotations within one process. Two processes sharing a
// store (a CLI run alongside an MCP host) could still read the same refresh
// token and exchange it concurrently, and one of them would lose the race with
// invalid_grant. That needs an interprocess lock held across read-exchange-
// write, which is worth building alongside the first platform that actually
// rotates (issues #21, #29) rather than guessing at its shape here.
type persistingTokenSource struct {
	policy   tokenPolicy
	src      oauth2.TokenSource
	clientID string // carried through rotations so the binding is not lost

	mu      sync.Mutex
	current string // the refresh token already known to be in the store
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, p.policy.authError(err)
	}
	if tok.RefreshToken == "" {
		return tok, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if tok.RefreshToken == p.current {
		return tok, nil
	}
	err = writeStoredToken(p.policy.Platform, &storedToken{
		RefreshToken: tok.RefreshToken,
		UpdatedAt:    time.Now().UTC(),
		Source:       "token refresh",
		ClientID:     p.clientID,
	})
	if err != nil {
		// The token we were handed replaces one that may already be dead, so
		// dropping it silently would break the next run with an auth error that
		// points nowhere. Fail here instead, while the path is still in hand.
		return nil, fmt.Errorf("could not save the refreshed %s sign-in: %w — the next run would fail to sign in; make that path writable, or set %s to a writable directory",
			p.policy.Platform, err, tokenStoreEnv)
	}
	p.current = tok.RefreshToken
	return tok, nil
}

// authError makes a token-endpoint rejection actionable. invalid_grant means
// the refresh token was revoked, expired, or (on a rotating platform) already
// replaced — none of which the user can fix by retrying, and all of which are
// fixed by signing in again.
//
// The original error is wrapped, so doctor's classification still sees the
// *oauth2.RetrieveError underneath and reports a broken setup rather than a
// transient one.
func (p tokenPolicy) authError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
		return fmt.Errorf("%s sign-in is no longer valid (invalid_grant — the refresh token was revoked, expired, or replaced): run `%s` to sign in again: %w",
			p.Platform, p.loginCommand(), err)
	}
	return err
}

// --- diagnostics ---

// tokenStoreStatus is what `doctor` and `config show` report about a platform's
// store slot: where it is, whether it can be written, and how old the saved
// sign-in is.
type tokenStoreStatus struct {
	Path     string
	PathErr  error
	WriteErr error
	Token    *storedToken
	ReadErr  error
}

// describeTokenStore inspects a platform's store slot. It never fails: every
// problem it finds is part of the report.
func describeTokenStore(platform string) tokenStoreStatus {
	st := tokenStoreStatus{}
	path, err := tokenStorePath(platform)
	if err != nil {
		st.PathErr = err
		return st
	}
	st.Path = path
	st.Token, st.ReadErr = readStoredToken(platform)
	// Resolved the same way the write resolves it, or the report would call a
	// store writable that the next sign-in cannot update.
	st.WriteErr = probeWritable(filepath.Dir(resolveWritePath(path)))
	return st
}

// probeWritable reports whether a store directory can actually be written.
// Permission bits alone don't answer this — read-only mounts, ACLs, and
// container user mappings all lie — so it creates a file and removes it.
//
// It does not create the directory. A directory that does not exist yet is
// writable exactly when the nearest existing ancestor is, and `config show`
// must be able to ask the question without answering it: creating the tree
// would turn a mistyped GOADS_TOKEN_STORE into a real, empty, perfectly
// writable store and report the setup as fine.
func probeWritable(dir string) error {
	existing := dir
	for {
		if fi, err := os.Stat(existing); err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%s is not a directory", existing)
			}
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return fmt.Errorf("no existing parent directory for %s", dir)
		}
		existing = parent
	}
	f, err := os.CreateTemp(existing, ".writable-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// location renders where the store is, and why it isn't usable when it isn't.
func (s tokenStoreStatus) location() string {
	switch {
	case s.PathErr != nil:
		return fmt.Sprintf("unavailable — %v", s.PathErr)
	case s.WriteErr != nil:
		return fmt.Sprintf("%s (NOT WRITABLE: %v)", s.Path, s.WriteErr)
	default:
		return s.Path
	}
}

// describe renders the saved sign-in for a human: its age and where it came
// from, never the token itself.
func (s tokenStoreStatus) describe(policy tokenPolicy) string {
	switch {
	case s.ReadErr != nil:
		return fmt.Sprintf("unreadable — %v", s.ReadErr)
	case s.Token == nil:
		return fmt.Sprintf("none saved — run `%s`", policy.loginCommand())
	case s.Token.Source != "":
		return fmt.Sprintf("saved %s via %s", humanizeAge(time.Since(s.Token.UpdatedAt)), s.Token.Source)
	default:
		return "saved " + humanizeAge(time.Since(s.Token.UpdatedAt))
	}
}

// humanizeAge renders how long ago something was written, at the coarsest unit
// that still says something useful.
func humanizeAge(d time.Duration) string {
	switch {
	case d < 0:
		return "in the future (check the system clock)"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	default:
		return plural(int(d.Hours()/24), "day") + " ago"
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// --- notices ---

// warnWriter receives deprecation and degradation notices. They go to stderr so
// they can never corrupt stdout, which carries JSON for jq pipelines on the CLI
// and the MCP protocol itself under `ads mcp`.
var warnWriter io.Writer = os.Stderr

var (
	warnedMu sync.Mutex
	warned   = map[string]bool{}
)

// warnOnce prints a notice the first time it is produced in a process. A
// deprecation the user has already been told about on this run adds nothing,
// and these paths run on every tool call under an MCP host.
func warnOnce(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	warnedMu.Lock()
	if warned[msg] {
		warnedMu.Unlock()
		return
	}
	warned[msg] = true
	w := warnWriter
	warnedMu.Unlock()
	// Only the prefix is coloured: these notices are long, and a whole
	// paragraph in yellow is harder to read than the sentence it interrupts.
	fmt.Fprintln(w, newStyles(w).warning("warning:")+" "+msg)
}
