package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestAuthMethods(t *testing.T) {
	if _, err := authMethods("", nil); err == nil {
		t.Fatal("authMethods(\"\", nil) should error (no credential)")
	}
	// A password (non-PEM string) yields one method.
	if ms, err := authMethods("hunter2", nil); err != nil || len(ms) != 1 {
		t.Fatalf("authMethods(password) = %v, %v", ms, err)
	}
	// A valid private-key PEM yields one method.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(blk))
	if ms, err := authMethods(keyPEM, nil); err != nil || len(ms) != 1 {
		t.Fatalf("authMethods(key) = %v, %v", ms, err)
	}
	// A passphrase-protected key PEM must be rejected, not silently sent to
	// the server as a password (which would leak the key material).
	encBlk, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	encPEM := string(pem.EncodeToMemory(encBlk))
	if _, err := authMethods(encPEM, nil); err == nil {
		t.Fatal("authMethods(encrypted key) should error, not fall back to password auth")
	}
	// A malformed PEM-armored token is rejected rather than treated as a password.
	if _, err := authMethods("-----BEGIN OPENSSH PRIVATE KEY-----\nnot base64\n-----END OPENSSH PRIVATE KEY-----\n", nil); err == nil {
		t.Fatal("authMethods(malformed PEM) should error")
	}
}

func TestAuthMethodsAgent(t *testing.T) {
	// A forwarded agent alone (no token) yields one method — this is how a
	// passphrase-protected key becomes usable, since the agent holds the
	// decrypted key rather than cornus.
	kr := agent.NewKeyring()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := kr.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	if ms, err := authMethods("", kr); err != nil || len(ms) != 1 {
		t.Fatalf("authMethods(agent only) = %v, %v", ms, err)
	}
	// Agent plus token yields both methods (agent tried first).
	if ms, err := authMethods("hunter2", kr); err != nil || len(ms) != 2 {
		t.Fatalf("authMethods(agent+password) = %v, %v", ms, err)
	}
}

func TestHostKeyCallbackFailsClosed(t *testing.T) {
	// Nothing configured → must error rather than trust any host key.
	if _, err := hostKeyCallback(sshConfig{}); err == nil {
		t.Fatal("hostKeyCallback with no config should fail closed")
	}
	// Explicit insecure opt-in → allowed.
	if cb, err := hostKeyCallback(sshConfig{insecure: true}); err != nil || cb == nil {
		t.Fatalf("insecure hostKeyCallback = %v, %v", cb, err)
	}
	// Pinned host key → allowed.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	authorized := string(ssh.MarshalAuthorizedKey(sshPub))
	if cb, err := hostKeyCallback(sshConfig{hostKey: authorized}); err != nil || cb == nil {
		t.Fatalf("pinned hostKeyCallback = %v, %v", cb, err)
	}
	// Garbage pinned key → error.
	if _, err := hostKeyCallback(sshConfig{hostKey: "not-a-key"}); err == nil {
		t.Fatal("hostKeyCallback with a bad pinned key should error")
	}
}

func TestResolveURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	// Template mode substitutes the bound port.
	cfg := sshConfig{addr: "sish.example:22", urlTemplate: "https://{port}.tun.example"}
	if got, want := resolveURL(cfg, nil, ln), "https://"+portStr+".tun.example"; got != want {
		t.Fatalf("resolveURL(template) = %q, want %q", got, want)
	}

	// No template falls back to tcp://host:port.
	cfg = sshConfig{addr: "sish.example:22"}
	if got, want := resolveURL(cfg, nil, ln), "tcp://sish.example:"+portStr; got != want {
		t.Fatalf("resolveURL(fallback) = %q, want %q", got, want)
	}
}

func TestBoundPortFallback(t *testing.T) {
	// A nil listener falls back to the requested bind port.
	if got := boundPort(nil, "0.0.0.0:8080"); got != 8080 {
		t.Fatalf("boundPort fallback = %d, want 8080", got)
	}
}

func TestFirstURL(t *testing.T) {
	cases := map[string]string{
		"connect to https://foo.example now.": "https://foo.example",
		"tunnel: http://bar.test/path,":       "http://bar.test/path",
		"no url on this line":                 "",
	}
	for line, want := range cases {
		if got := firstURL(line); got != want {
			t.Errorf("firstURL(%q) = %q, want %q", line, got, want)
		}
	}
}
