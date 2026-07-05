// Package tokencache stores short-lived client credentials between CLI
// invocations.
//
// It exists because the cornus CLI is a short-lived process: every command
// exits. Without somewhere to put an exchanged token, each invocation would mint
// a ServiceAccount token AND exchange it — two round trips where direct
// verification needs one — which would make token exchange a latency regression
// rather than a feature.
//
// Two backends. The OS keyring (Secret Service on Linux, Keychain on macOS,
// Credential Manager on Windows) is preferred where it works; a 0600 file under
// the user cache directory covers everywhere it does not, which includes CI, most
// containers, and a plain SSH session with no session bus. Both are real paths:
// the keyring is the better place for a secret on a workstation, and the file is
// what the automated tests can exercise without a session bus.
//
// Nothing here is required for correctness. A cache that fails, in either
// backend, must degrade to "no cached token" and let the caller mint a fresh one
// — never to an error that fails the user's command.
package tokencache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// keyringService is the service name every cornus entry is filed under, so
// `keyring.DeleteAll(keyringService)` can clear them and a user browsing their
// keyring sees one coherent group.
const keyringService = "cornus"

// DefaultMargin is how long before a token's real expiry Get stops serving it.
// Without it a token could be handed out with a millisecond of life left and
// expire in flight, turning a cache hit into a 401 the caller has to recover
// from — which is exactly the work the cache exists to avoid. Callers with their
// own renewal policy use GetRaw and apply their own.
const DefaultMargin = 60 * time.Second

// Entry is a cached credential and the moment it stops being usable.
type Entry struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
}

// live reports whether the entry has not yet expired at all. An entry that fails
// this is deleted on read: it can never become useful again, and leaving it means
// re-reading and re-rejecting it on every invocation.
func (e Entry) live(now time.Time) bool {
	return e.Token != "" && e.Expires.After(now)
}

// usableNow additionally applies the default margin.
func (e Entry) usableNow(now time.Time) bool {
	return e.Token != "" && e.Expires.After(now.Add(DefaultMargin))
}

// Store is somewhere to keep a credential between invocations.
//
// Get returns only (Entry, bool) on purpose. A missing entry, an expired one, a
// corrupt one and an unreachable backend are all the same thing to every caller
// — mint a fresh token — so surfacing an error would give callers a distinction
// none of them act on, and invite one of them to fail a command over a cache.
type Store interface {
	// Get returns an entry only while it remains valid beyond DefaultMargin.
	Get(key string) (Entry, bool)
	// GetRaw returns whatever is stored, VERBATIM: its real expiry, no margin, and
	// no expiry check of its own.
	//
	// A long-lived caller — the background agent holding a connection for hours —
	// decides for itself when to renew, and a store that had already applied
	// somebody else's notion of "too close to expiry" would hide the information it
	// needs to do that. It also means a caller can hand in its own clock and get a
	// deterministic answer. Callers that just want a usable token use Get.
	GetRaw(key string) (Entry, bool)
	Set(key string, e Entry) error
	Delete(key string) error
	// Name identifies the backend for diagnostics ("keyring", "file", "none").
	Name() string
}

// Key derives a cache key from the values that determine WHAT the credential is.
//
// It must bind every one of them. A context name alone would be wrong: contexts
// are re-pointed with `cornus config set-context`, so an entry keyed on the name
// would serve a token minted for one server on a later request to another. The
// same goes for the identity it was exchanged for and the scope it was issued
// with — one workstation can hold several profiles against one server, and a
// narrow token must not be reused where a broader one was asked for.
//
// Hashed because the key is visible in both backends (the keyring's user field, a
// filename on disk). The inputs are not secret, but a world-readable directory
// listing should not enumerate which clusters and service accounts someone uses.
func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		// Length-prefixed, so ("ab","c") and ("a","bc") cannot collide.
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return "cornus-" + hex.EncodeToString(h.Sum(nil))[:32]
}

// Open selects a backend from CORNUS_TOKEN_CACHE:
//
//	auto (default) — the keyring, falling back to a file where it is unusable
//	keyring        — the keyring only; never writes a token to disk
//	file           — the file only
//	none           — no caching, so every invocation mints afresh
//
// `none` is a supported answer, not a degraded one: it reproduces the behaviour
// cornus had before any of this, and it is what to set if a token at rest is
// unacceptable in a given environment.
func Open(logf func(format string, args ...any)) (Store, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv("CORNUS_TOKEN_CACHE"))); mode {
	case "", "auto":
		f, err := newFileStore()
		if err != nil {
			// No usable cache directory: the keyring alone is still better than
			// nothing, and a failure to cache must never fail a command.
			logf("token cache: no usable cache directory (%v); keyring only", err)
			return &autoStore{keyring: keyringStore{}, logf: logf}, nil
		}
		return &autoStore{keyring: keyringStore{}, file: f, logf: logf}, nil
	case "keyring":
		return keyringStore{}, nil
	case "file":
		return newFileStore()
	case "none", "off", "0":
		return nopStore{}, nil
	default:
		return nil, fmt.Errorf("CORNUS_TOKEN_CACHE=%q is not one of auto, keyring, file, none", mode)
	}
}

// ---- keyring ---------------------------------------------------------------

type keyringStore struct{}

func (keyringStore) Name() string { return "keyring" }

func (k keyringStore) Get(key string) (Entry, bool) {
	e, ok := k.GetRaw(key)
	if !ok {
		return Entry{}, false
	}
	if !e.live(time.Now()) {
		// Past expiry: it can never become useful, so drop it here rather than
		// re-reading and re-rejecting it on every later invocation.
		_ = k.Delete(key)
		return Entry{}, false
	}
	return e, e.usableNow(time.Now())
}

func (k keyringStore) GetRaw(key string) (Entry, bool) {
	raw, err := keyring.Get(keyringService, key)
	if err != nil {
		return Entry{}, false
	}
	var e Entry
	if json.Unmarshal([]byte(raw), &e) != nil || e.Token == "" {
		_ = k.Delete(key)
		return Entry{}, false
	}
	return e, true
}

func (keyringStore) Set(key string, e Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return keyring.Set(keyringService, key, string(raw))
}

func (keyringStore) Delete(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// usable reports whether the keyring can be reached at all, distinguishing that
// from the entry simply being absent.
//
// This distinction is the whole correctness of auto mode. keyring.ErrNotFound
// means the keyring WORKS and does not hold this key — a plain cache miss. Treating
// it as "keyring broken" would send the next write to disk, splitting the cache
// across two backends where a stale file can shadow a fresh keyring entry. Every
// other error (no session bus, unsupported platform, a locked keychain) means the
// keyring cannot be used at all, and disk is the right answer.
func (k keyringStore) usable(key string) (Entry, bool, bool) {
	raw, err := keyring.Get(keyringService, key)
	switch {
	case err == nil:
		var e Entry
		if json.Unmarshal([]byte(raw), &e) != nil || !e.live(time.Now()) {
			return Entry{}, false, true
		}
		return e, true, true
	case errors.Is(err, keyring.ErrNotFound):
		return Entry{}, false, true
	default:
		return Entry{}, false, false
	}
}

// ---- file ------------------------------------------------------------------

type fileStore struct{ dir string }

// runtimeDir is where a short-lived credential belongs, and it is deliberately
// NOT os.UserCacheDir.
//
// XDG_RUNTIME_DIR is per-user, tmpfs-backed and cleared at logout, so a token
// that outlives its usefulness does not outlive the session as a file on disk. A
// persistent cache directory would keep it across reboots for no benefit — the
// token expires in an hour either way. This mirrors what the SSH-key session
// cache has always done (pkg/sshclient, before it moved onto this package);
// CORNUS_AGENT_DIR is honoured for the same reason it is there, so an isolated
// agent gets an isolated cache.
func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "cornus")
	}
	if d := os.Getenv("CORNUS_AGENT_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "cornus")
}

func newFileStore() (*fileStore, error) {
	dir := filepath.Join(runtimeDir(), "tokens")
	// 0700: the directory listing alone reveals how many credentials a user holds,
	// and the files inside are bearer tokens.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// MkdirAll leaves a pre-existing directory's mode alone, so tighten it
	// explicitly rather than trusting whatever created it first.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &fileStore{dir: dir}, nil
}

func (f *fileStore) Name() string { return "file" }

func (f *fileStore) path(key string) string { return filepath.Join(f.dir, key+".json") }

func (f *fileStore) Get(key string) (Entry, bool) {
	e, ok := f.GetRaw(key)
	if !ok {
		return Entry{}, false
	}
	if !e.live(time.Now()) {
		// Past expiry: it can never become useful, so drop it here rather than
		// re-reading and re-rejecting it on every later invocation.
		_ = os.Remove(f.path(key))
		return Entry{}, false
	}
	return e, e.usableNow(time.Now())
}

func (f *fileStore) GetRaw(key string) (Entry, bool) {
	raw, err := os.ReadFile(f.path(key))
	if err != nil {
		return Entry{}, false
	}
	var e Entry
	if json.Unmarshal(raw, &e) != nil || e.Token == "" {
		// Corrupt: unreadable now and unreadable forever, so remove it.
		_ = os.Remove(f.path(key))
		return Entry{}, false
	}
	return e, true
}

func (f *fileStore) Set(key string, e Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	path := f.path(key)
	// Write-then-rename so a concurrent reader never sees a half-written token,
	// and chmod explicitly: os.WriteFile applies its mode only when it CREATES the
	// file, so overwriting an entry that already exists with looser permissions
	// would silently keep them. pkg/clientconfig learned the same thing.
	tmp, err := os.CreateTemp(f.dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (f *fileStore) Delete(key string) error {
	err := os.Remove(f.path(key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ---- auto ------------------------------------------------------------------

// autoStore prefers the keyring and falls back to a file where the keyring
// cannot be reached. The decision is memoized per process: it is the same answer
// every time within one invocation, and probing repeatedly would mean a D-Bus
// dial per operation.
type autoStore struct {
	keyring keyringStore
	file    *fileStore
	logf    func(string, ...any)

	decided bool
	useFile bool
}

func (a *autoStore) Name() string {
	if a.useFile {
		return "file"
	}
	return "keyring"
}

// backend picks the store to use for one operation, probing the keyring the first
// time and whenever it has not already been ruled out.
//
// The flip to file is ONE-WAY and can happen at any point in the process's life,
// not only at the first call. That matters for the background agent, which is
// long-lived and inherits its D-Bus address from the shell that spawned it: when
// that session ends the bus goes away underneath it, and a decision memoized at
// startup would leave the agent permanently convinced of a keyring it can no
// longer reach. Falling back the moment the keyring stops answering keeps it
// working with the file cache instead.
func (a *autoStore) backend(key string) Store {
	if a.useFile {
		return a.chosen()
	}
	if _, _, usable := a.keyring.usable(key); usable {
		a.decided = true
		return a.keyring
	}
	a.logf("token cache: keyring unavailable; using the file cache")
	a.decided, a.useFile = true, true
	return a.chosen()
}

func (a *autoStore) Get(key string) (Entry, bool) {
	return a.backend(key).Get(key)
}

func (a *autoStore) GetRaw(key string) (Entry, bool) {
	return a.backend(key).GetRaw(key)
}

func (a *autoStore) Set(key string, e Entry) error {
	s := a.backend(key)
	if err := s.Set(key, e); err != nil && !a.useFile {
		// The probe succeeded but the write did not: the keyring is answering
		// reads and refusing writes (a size limit, a locked collection). Fall back
		// rather than silently not caching.
		a.logf("token cache: keyring write failed (%v); using the file cache", err)
		a.useFile = true
		return a.chosen().Set(key, e)
	} else if err != nil {
		return err
	}
	return nil
}

// Delete clears BOTH backends regardless of which is in use. An entry can have
// been written by an earlier invocation under a different backend (a laptop that
// gained a keyring, a workstation now reached over SSH), and "log me out" has to
// mean it everywhere rather than leaving a copy behind in the one not currently
// selected.
func (a *autoStore) Delete(key string) error {
	var firstErr error
	if err := a.keyring.Delete(key); err != nil && !a.useFile {
		firstErr = err
	}
	if a.file != nil {
		if err := a.file.Delete(key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *autoStore) chosen() Store {
	if a.useFile && a.file != nil {
		return a.file
	}
	if a.useFile {
		return nopStore{}
	}
	return a.keyring
}

// ---- none ------------------------------------------------------------------

// None returns a store that caches nothing. It is what a caller falls back to
// when the store cannot be opened at all: caching is an optimization, and losing
// it must never fail the command it was meant to speed up.
func None() Store { return nopStore{} }

type nopStore struct{}

func (nopStore) Name() string                { return "none" }
func (nopStore) Get(string) (Entry, bool)    { return Entry{}, false }
func (nopStore) GetRaw(string) (Entry, bool) { return Entry{}, false }
func (nopStore) Set(string, Entry) error     { return nil }
func (nopStore) Delete(string) error         { return nil }
