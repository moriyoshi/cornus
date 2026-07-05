package caretaker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/credential"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"

	_ "cornus/pkg/creddelivery/generic"
)

// fakeCredOpen returns an opener whose every stream speaks the caretaker->caller
// credential protocol: it consumes the tag byte + session/name lines + request,
// then answers with cred. opens counts how many streams were opened (to observe
// caching).
func fakeCredOpen(cred credential.Credential, opens *int32) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		atomic.AddInt32(opens, 1)
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			var tag [1]byte
			if _, err := io.ReadFull(b, tag[:]); err != nil {
				return
			}
			if _, err := wire.ReadLine(b); err != nil { // session
				return
			}
			if _, err := wire.ReadLine(b); err != nil { // name
				return
			}
			var req deploywire.CredentialRequest
			if err := json.NewDecoder(b).Decode(&req); err != nil {
				return
			}
			_ = json.NewEncoder(b).Encode(deploywire.CredentialResponse{Values: cred.Values, Expiration: cred.Expiration})
		}()
		return a, nil
	}
}

func TestCredFetcherCaches(t *testing.T) {
	var opens int32
	f := &credFetcher{
		open: fakeCredOpen(credential.Credential{Values: map[string]string{"k": "v"}}, &opens),
		role: CredentialRole{Name: "db", Session: "s"},
		ttl:  time.Hour,
	}
	for i := 0; i < 3; i++ {
		got, err := f.get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.Values["k"] != "v" {
			t.Fatalf("value = %v", got.Values)
		}
	}
	if n := atomic.LoadInt32(&opens); n != 1 {
		t.Fatalf("opened %d streams, want 1 (cached)", n)
	}
}

func TestCredExpiryDrivesRefetch(t *testing.T) {
	var opens int32
	// A short-lived credential (expires ~now) must be re-fetched despite a long TTL.
	f := &credFetcher{
		open: fakeCredOpen(credential.Credential{Values: map[string]string{"k": "v"}, Expiration: time.Now().Add(time.Second)}, &opens),
		role: CredentialRole{Name: "db"},
		ttl:  time.Hour,
	}
	f.get(context.Background())
	f.get(context.Background()) // expiry - skew is already in the past -> not cached
	if n := atomic.LoadInt32(&opens); n < 2 {
		t.Fatalf("opened %d streams, want >=2 (short expiry forces refetch)", n)
	}
}

func TestServeCredFile(t *testing.T) {
	var opens int32
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	f := &credFetcher{
		open: fakeCredOpen(credential.Credential{Values: map[string]string{"AccessKeyId": "AKIA"}}, &opens),
		role: CredentialRole{Name: "db"},
		ttl:  time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveCredFile(ctx, CredentialDelivery{Kind: "file", Path: path, Format: "json"}, f)

	waitFor(t, func() bool { _, err := os.Stat(path); return err == nil })
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "AKIA") {
		t.Fatalf("file content = %q", b)
	}
}

func TestServeCredEndpoint(t *testing.T) {
	var opens int32
	f := &credFetcher{
		open: fakeCredOpen(credential.Credential{Values: map[string]string{"token": "abc"}}, &opens),
		role: CredentialRole{Name: "db"},
		ttl:  time.Hour,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // serveCredEndpoint rebinds addr itself

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveCredEndpoint(ctx, CredentialDelivery{Kind: "endpoint", Provider: "generic", Addr: addr}, f)

	var resp *http.Response
	waitFor(t, func() bool {
		r, err := http.Get("http://" + addr + "/credentials/db")
		if err != nil {
			return false
		}
		resp = r
		return true
	})
	defer resp.Body.Close()
	var got credential.Credential
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Values["token"] != "abc" {
		t.Fatalf("endpoint body = %v", got.Values)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestServeCredFileAppliesOwnership is the caretaker half of the kubernetes
// non-root bug. WriteFile writes mode 0600 as THIS process's user; the app
// container runs as spec.User. Without the chown a `user:` deployment cannot
// read its own credential, and the only symptom is the app's "permission denied".
//
// Unprivileged, chowning to a uid this process does not own fails with EPERM —
// which is exactly what makes the assertion possible without root: the delivery
// must FAIL rather than leave a file the workload cannot read. A silent success
// here IS the bug.
func TestServeCredFileAppliesOwnership(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a chown to any uid succeeds, so this cannot observe the attempt")
	}
	var opens int32
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	f := &credFetcher{
		open: fakeCredOpen(credential.Credential{Values: map[string]string{"AccessKeyId": "AKIA"}}, &opens),
		role: CredentialRole{Name: "db"},
		ttl:  time.Hour,
	}
	// The context is BOUNDED because of what the neutralized case does: with the
	// chown removed, the write succeeds and serveCredFile enters its refresh loop
	// and never returns. An unbounded context turns the regression into a hung
	// test, which reports nothing; a deadline turns it into a nil error and the
	// message below.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// 12345 is not this process's uid, so the chown is refused.
	err := serveCredFile(ctx,
		CredentialDelivery{Kind: "file", Path: path, Format: "json", UID: 12345, GID: 12345}, f)
	if err == nil {
		t.Fatal("serveCredFile succeeded without applying the requested ownership; the workload " +
			"would get a 0600 file owned by the caretaker and could not read it")
	}
	if !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("error %q does not name the ownership failure", err)
	}
}

// TestServeCredFileLeavesOwnershipAloneWhenUnset: zero means the server had
// nothing to say (no user, or a name only the image resolves), and the file must
// be written as-is rather than chowned to root.
func TestServeCredFileLeavesOwnershipAloneWhenUnset(t *testing.T) {
	var opens int32
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	f := &credFetcher{
		open: fakeCredOpen(credential.Credential{Values: map[string]string{"AccessKeyId": "AKIA"}}, &opens),
		role: CredentialRole{Name: "db"},
		ttl:  time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveCredFile(ctx, CredentialDelivery{Kind: "file", Path: path, Format: "json"}, f)
	waitFor(t, func() bool { _, err := os.Stat(path); return err == nil })
}
