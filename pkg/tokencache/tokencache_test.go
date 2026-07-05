package tokencache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// cacheHome points the file backend's runtime directory at a temp dir, so a test
// never touches the developer's real token cache.
func cacheHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}

func live(token string) Entry  { return Entry{Token: token, Expires: time.Now().Add(time.Hour)} }
func stale(token string) Entry { return Entry{Token: token, Expires: time.Now().Add(-time.Minute)} }

func TestKeyBindsEveryInput(t *testing.T) {
	a := Key("https://a.example", "sa", "api")
	if a != Key("https://a.example", "sa", "api") {
		t.Fatal("Key is not stable for identical inputs")
	}
	// Every component must change the key. The server one is the case that
	// matters most: a context re-pointed with `config set-context` must not serve
	// the old server's token.
	for _, other := range [][]string{
		{"https://b.example", "sa", "api"},
		{"https://a.example", "other-sa", "api"},
		{"https://a.example", "sa", "registry:pull"},
	} {
		if Key(other...) == a {
			t.Fatalf("Key(%v) collides with the base key", other)
		}
	}
	// Length prefixing: without it these two would hash the same bytes.
	if Key("ab", "c") == Key("a", "bc") {
		t.Fatal(`Key("ab","c") collides with Key("a","bc") — components are not delimited`)
	}
}

func TestFileStoreRoundTripAndPermissions(t *testing.T) {
	base := cacheHome(t)
	f, err := newFileStore()
	if err != nil {
		t.Fatal(err)
	}
	key := Key("srv", "sa", "api")
	if err := f.Set(key, live("tok-1")); err != nil {
		t.Fatal(err)
	}
	got, ok := f.Get(key)
	if !ok || got.Token != "tok-1" {
		t.Fatalf("Get = (%v, %v), want tok-1", got, ok)
	}

	// A bearer token on disk: 0600 in a 0700 directory, same discipline as the
	// client config file.
	dir := filepath.Join(base, "cornus", "tokens")
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("cache dir mode = %o, want 700", perm)
	}
	fi, err := os.Stat(f.path(key))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("entry mode = %o, want 600", perm)
	}

	// Overwriting must not inherit looser permissions from a pre-existing file:
	// os.WriteFile applies its mode only on create, which is the trap
	// pkg/clientconfig documents.
	if err := os.Chmod(f.path(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(key, live("tok-2")); err != nil {
		t.Fatal(err)
	}
	fi, _ = os.Stat(f.path(key))
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode after overwrite = %o, want 600", perm)
	}

	if err := f.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Get(key); ok {
		t.Fatal("entry survived Delete")
	}
	// Deleting what is not there is success, not an error.
	if err := f.Delete(key); err != nil {
		t.Fatalf("Delete of a missing entry: %v", err)
	}
}

// TestFileStoreDiscardsUnusableEntries: an expired or corrupt entry is a miss AND
// is removed, so it is not re-read and re-rejected on every later invocation.
func TestFileStoreDiscardsUnusableEntries(t *testing.T) {
	cacheHome(t)
	f, err := newFileStore()
	if err != nil {
		t.Fatal(err)
	}

	expired := Key("srv", "sa", "expired")
	if err := f.Set(expired, stale("old")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Get(expired); ok {
		t.Fatal("an expired entry was served")
	}
	if _, err := os.Stat(f.path(expired)); !os.IsNotExist(err) {
		t.Fatal("an expired entry was left on disk")
	}

	// Inside the expiry margin: still technically valid, but too close to hand out.
	soon := Key("srv", "sa", "soon")
	if err := f.Set(soon, Entry{Token: "t", Expires: time.Now().Add(DefaultMargin / 2)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Get(soon); ok {
		t.Fatal("a token inside the expiry margin was served; it could expire in flight")
	}

	corrupt := Key("srv", "sa", "corrupt")
	if err := os.WriteFile(f.path(corrupt), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Get(corrupt); ok {
		t.Fatal("a corrupt entry was served")
	}
	if _, err := os.Stat(f.path(corrupt)); !os.IsNotExist(err) {
		t.Fatal("a corrupt entry was left on disk")
	}
}

func TestKeyringStoreRoundTrip(t *testing.T) {
	keyring.MockInit()
	var k keyringStore
	key := Key("srv", "sa", "api")

	if _, ok := k.Get(key); ok {
		t.Fatal("empty keyring returned a hit")
	}
	if err := k.Set(key, live("tok")); err != nil {
		t.Fatal(err)
	}
	got, ok := k.Get(key)
	if !ok || got.Token != "tok" {
		t.Fatalf("Get = (%v, %v)", got, ok)
	}
	if err := k.Set(key, stale("old")); err != nil {
		t.Fatal(err)
	}
	if _, ok := k.Get(key); ok {
		t.Fatal("an expired keyring entry was served")
	}
	if err := k.Delete(key); err != nil {
		t.Fatal(err)
	}
	// ErrNotFound on delete is success: the entry is gone, which is what was asked.
	if err := k.Delete(key); err != nil {
		t.Fatalf("Delete of a missing entry: %v", err)
	}
}

// TestAutoNotFoundIsAMissNotAFallback is the correctness of auto mode.
//
// keyring.ErrNotFound means the keyring WORKS and does not hold this key. If that
// were treated as "keyring broken", the next write would go to disk and the cache
// would split across two backends — where a stale file can shadow a fresh keyring
// entry, and "log me out" clears only one of them.
func TestAutoNotFoundIsAMissNotAFallback(t *testing.T) {
	cacheHome(t)
	keyring.MockInit()
	a, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := Key("srv", "sa", "api")

	if _, ok := a.Get(key); ok {
		t.Fatal("empty keyring returned a hit")
	}
	if got := a.Name(); got != "keyring" {
		t.Fatalf("after a plain miss the backend is %q, want keyring", got)
	}
	if err := a.Set(key, live("tok")); err != nil {
		t.Fatal(err)
	}
	// The write went to the keyring, not to disk.
	if raw, err := keyring.Get(keyringService, key); err != nil || raw == "" {
		t.Fatalf("entry is not in the keyring: %v", err)
	}
	f, _ := newFileStore()
	if _, err := os.Stat(f.path(key)); !os.IsNotExist(err) {
		t.Fatal("a working keyring still wrote the token to disk")
	}
}

// TestAutoFallsBackWhenKeyringIsUnusable covers the other half: any error that is
// not ErrNotFound (no session bus, unsupported platform, locked keychain) means
// the keyring cannot be used at all, and the file backend takes over.
func TestAutoFallsBackWhenKeyringIsUnusable(t *testing.T) {
	cacheHome(t)
	keyring.MockInitWithError(errors.New("dbus: no session bus"))
	var logged int
	a, err := Open(func(string, ...any) { logged++ })
	if err != nil {
		t.Fatal(err)
	}
	key := Key("srv", "sa", "api")

	if _, ok := a.Get(key); ok {
		t.Fatal("unusable keyring returned a hit")
	}
	if got := a.Name(); got != "file" {
		t.Fatalf("backend = %q, want file", got)
	}
	if logged == 0 {
		t.Fatal("the fallback was silent; an operator should be able to see which backend is in use")
	}
	if err := a.Set(key, live("tok")); err != nil {
		t.Fatal(err)
	}
	got, ok := a.Get(key)
	if !ok || got.Token != "tok" {
		t.Fatalf("Get after fallback = (%v, %v)", got, ok)
	}
	f, _ := newFileStore()
	if _, err := os.Stat(f.path(key)); err != nil {
		t.Fatalf("the fallback did not write to disk: %v", err)
	}
}

// TestAutoDeleteClearsBothBackends: an entry can have been written by an earlier
// invocation under the other backend (a laptop that gained a keyring, a
// workstation now reached over SSH). "Log me out" has to mean it everywhere.
func TestAutoDeleteClearsBothBackends(t *testing.T) {
	cacheHome(t)
	keyring.MockInit()
	key := Key("srv", "sa", "api")

	// Seed both backends directly.
	if err := (keyringStore{}).Set(key, live("from-keyring")); err != nil {
		t.Fatal(err)
	}
	f, err := newFileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Set(key, live("from-disk")); err != nil {
		t.Fatal(err)
	}

	a, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, ok := (keyringStore{}).Get(key); ok {
		t.Fatal("the keyring entry survived Delete")
	}
	if _, ok := f.Get(key); ok {
		t.Fatal("the disk entry survived Delete — a stale copy is left to shadow a fresh one")
	}
}

func TestOpenSelectsBackend(t *testing.T) {
	cacheHome(t)
	keyring.MockInit()

	for _, tc := range []struct{ env, want string }{
		{"", "keyring"},
		{"auto", "keyring"},
		{"keyring", "keyring"},
		{"file", "file"},
		{"none", "none"},
		{"NONE", "none"},
		{" file ", "file"},
	} {
		t.Setenv("CORNUS_TOKEN_CACHE", tc.env)
		s, err := Open(nil)
		if err != nil {
			t.Fatalf("CORNUS_TOKEN_CACHE=%q: %v", tc.env, err)
		}
		if got := s.Name(); got != tc.want {
			t.Fatalf("CORNUS_TOKEN_CACHE=%q: backend = %q, want %q", tc.env, got, tc.want)
		}
	}

	// An unrecognized value is a configuration error, not a silent default: a user
	// who typed CORNUS_TOKEN_CACHE=disk meant something by it.
	t.Setenv("CORNUS_TOKEN_CACHE", "disk")
	if _, err := Open(nil); err == nil {
		t.Fatal("an unknown CORNUS_TOKEN_CACHE value was accepted")
	}
}

// TestNoneCachesNothing: `none` reproduces the pre-cache behaviour exactly, which
// is the supported answer for anyone who will not have a token at rest.
func TestNoneCachesNothing(t *testing.T) {
	cacheHome(t)
	keyring.MockInit()
	t.Setenv("CORNUS_TOKEN_CACHE", "none")
	s, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := Key("srv", "sa", "api")
	if err := s.Set(key, live("tok")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(key); ok {
		t.Fatal("`none` served a cached token")
	}
	if _, ok := (keyringStore{}).Get(key); ok {
		t.Fatal("`none` wrote to the keyring")
	}
	f, _ := newFileStore()
	if _, err := os.Stat(f.path(key)); !os.IsNotExist(err) {
		t.Fatal("`none` wrote to disk")
	}
}
