package sshclient

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// genKeyPair returns an ed25519 client key: its ssh.Signer, its public key, and a
// PEM path on disk (unencrypted).
func genKeyPair(t *testing.T) (ssh.Signer, ssh.PublicKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return signer, signer.PublicKey(), path
}

func roundTrip(t *testing.T, d *Dialer, echoAddr string) {
	t.Helper()
	conn, err := d.DialContext(context.Background(), "tcp", echoAddr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	msg := []byte("hello-cornus")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("echo = %q, want %q", got, msg)
	}
}

// noAgentEnv clears SSH_AUTH_SOCK so the dialer relies only on the identity file.
func noAgentEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "")
}

func TestDialerRoundTrip(t *testing.T) {
	noAgentEnv(t)
	_, clientPub, keyPath := genKeyPair(t)
	_, addr := newFakeSSHServer(t, clientPub)
	echo := startEcho(t)

	d, err := Dial(context.Background(), Options{
		Addr: addr, User: "tester", IdentityFiles: []string{keyPath},
		Insecure: true, NoAgent: true, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer d.Close()

	roundTrip(t, d, echo)

	// After Close, DialContext must fail.
	_ = d.Close()
	if _, err := d.DialContext(context.Background(), "tcp", echo); err == nil {
		t.Fatal("DialContext after Close = nil error, want error")
	}
}

func TestDialerReconnectsAfterDrop(t *testing.T) {
	noAgentEnv(t)
	_, clientPub, keyPath := genKeyPair(t)
	srv, addr := newFakeSSHServer(t, clientPub)
	echo := startEcho(t)

	d, err := Dial(context.Background(), Options{
		Addr: addr, User: "tester", IdentityFiles: []string{keyPath},
		Insecure: true, NoAgent: true, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer d.Close()
	roundTrip(t, d, echo)

	before := srv.accepts.Load()
	// Force a drop; wait for the Wait() goroutine to observe it.
	srv.dropAll()
	waitFor(t, 2*time.Second, func() bool {
		d.mu.Lock()
		dead := d.client == nil
		d.mu.Unlock()
		return dead
	})

	// A new request transparently reconnects.
	roundTrip(t, d, echo)
	if got := srv.accepts.Load(); got <= before {
		t.Fatalf("accepts did not increase after reconnect: before=%d after=%d", before, got)
	}
}

func TestDialerSingleFlightReconnect(t *testing.T) {
	noAgentEnv(t)
	_, clientPub, keyPath := genKeyPair(t)
	srv, addr := newFakeSSHServer(t, clientPub)
	echo := startEcho(t)

	d, err := Dial(context.Background(), Options{
		Addr: addr, User: "tester", IdentityFiles: []string{keyPath},
		Insecure: true, NoAgent: true, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer d.Close()
	roundTrip(t, d, echo)

	before := srv.accepts.Load()
	srv.dropAll()
	waitFor(t, 2*time.Second, func() bool {
		d.mu.Lock()
		dead := d.client == nil
		d.mu.Unlock()
		return dead
	})

	// Fire many concurrent dials during the outage; they must coalesce onto ONE
	// reconnect (a single new server-side Accept).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := d.DialContext(context.Background(), "tcp", echo)
			if err == nil {
				_ = conn.Close()
			}
		}()
	}
	wg.Wait()
	if got := srv.accepts.Load() - before; got != 1 {
		t.Fatalf("concurrent reconnect produced %d new accepts, want 1", got)
	}
}

func TestDialBadHostKeyFailsFast(t *testing.T) {
	noAgentEnv(t)
	_, clientPub, keyPath := genKeyPair(t)
	_, addr := newFakeSSHServer(t, clientPub)

	// No known-hosts, no pinned key, not insecure -> host-key verification fails.
	_, err := Dial(context.Background(), Options{
		Addr: addr, User: "tester", IdentityFiles: []string{keyPath},
		NoAgent: true, Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("Dial with unverifiable host key = nil error, want error")
	}
}

func TestDialProxyCommandRejected(t *testing.T) {
	_, err := Dial(context.Background(), Options{Addr: "x:22", ProxyCommand: "nc %h %p"})
	if err == nil {
		t.Fatal("Dial with ProxyCommand = nil error, want error directing to the binary fallback")
	}
}

func TestResilientConnMarksDead(t *testing.T) {
	d := &Dialer{}
	d.gen = 5
	fake := &closeErrConn{}
	rc := &resilientConn{Conn: fake, d: d, gen: 5}
	// A generic transport error records failure (markDead is a no-op here since the
	// dialer has no live client, but the failed flag is what the wrapper guards on).
	rc.note(errors.New("connection reset by peer"))
	if !rc.failed.Load() {
		t.Fatal("resilientConn did not record failure on a transport error")
	}
	// io.EOF and net.ErrClosed are ignored.
	rc2 := &resilientConn{Conn: fake, d: d, gen: 5}
	rc2.note(io.EOF)
	rc2.note(net.ErrClosed)
	if rc2.failed.Load() {
		t.Fatal("resilientConn marked failed on a benign error")
	}
}

type closeErrConn struct{ net.Conn }

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
