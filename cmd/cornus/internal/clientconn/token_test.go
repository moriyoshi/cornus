package clientconn

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cornus/pkg/client"
	"cornus/pkg/clientconfig"
	"cornus/pkg/sshclient"
)

// TestResolveWithCarriesEnvTokenWithoutContext is the regression test for a real
// defect: with NO connection context selected, ResolveWith returned early and
// dropped the environment token entirely, so every resolver-based command in a
// zero-config setup sent no credential.
//
// It was found empirically — against an auth-enabled server,
// `cornus build --builder ws://.../.cornus/v1/build/attach` failed its WebSocket
// handshake with a 401 while `cornus deploy --server ...` authenticated fine.
// The asymmetry is the tell: pkg/client defaults its own token straight from
// CORNUS_TOKEN and never consults the resolver, so only the resolver-based
// commands lost it.
//
// ResolveWith documents "Token precedence: CORNUS_TOKEN env > a cluster-minted
// kube-auth token > the profile's static token", so an env token must survive
// the no-context path too.
func TestResolveWithCarriesEnvTokenWithoutContext(t *testing.T) {
	// An empty config file: no contexts, no current-context, so cc == nil.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := clientconfig.Save(path, &clientconfig.File{}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	r := &Resolver{ConfigFile: path}

	cn, err := r.ResolveWith("http://127.0.0.1:5000", "sekret")
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if cn.Token != "sekret" {
		t.Fatalf("Token = %q, want %q — the env token was dropped on the no-context path", cn.Token, "sekret")
	}
	if cn.Endpoint != "http://127.0.0.1:5000" {
		t.Fatalf("Endpoint = %q, want the explicit server", cn.Endpoint)
	}
}

func TestReadOnlyResolverConsumesCachedSSHSessionWithoutPrivateKey(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	// Pin the file backend: without it the shared credential store would use the
	// OS keyring where one is reachable, and this test would seed the developer's
	// real one.
	t.Setenv("CORNUS_TOKEN_CACHE", "file")
	const endpoint = "http://127.0.0.1:5000"
	const fingerprint = "SHA256:cached-key"
	// Seed the cache under the same stable identity the resolver derives, NOT the
	// dialed endpoint — see sessionCacheIdentity for why those differ.
	identity := sessionCacheIdentity("", &clientconfig.Context{Server: endpoint})
	if err := sshclient.WriteSession(identity, fingerprint, "api", "cached-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := clientconfig.Save(path, &clientconfig.File{CurrentContext: "prof", Contexts: map[string]*clientconfig.Context{
		"prof": {Server: endpoint, KeyAuth: &clientconfig.KeyAuth{IdentityFile: "/unreadable/private-key", KeyFingerprint: fingerprint}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cn, err := (&Resolver{ConfigFile: path, SSHKeyCacheReadOnly: true}).Resolve("")
	if err != nil {
		t.Fatalf("read-only Resolve: %v", err)
	}
	if cn.Token != "cached-token" {
		t.Fatalf("Token = %q, want cached-token", cn.Token)
	}
}

func TestReadOnlyResolverDoesNotMintOnCacheMiss(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	// Pin the file backend: without it the shared credential store would use the
	// OS keyring where one is reachable, and this test would seed the developer's
	// real one.
	t.Setenv("CORNUS_TOKEN_CACHE", "file")
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := clientconfig.Save(path, &clientconfig.File{CurrentContext: "prof", Contexts: map[string]*clientconfig.Context{
		"prof": {Server: "http://127.0.0.1:5000", KeyAuth: &clientconfig.KeyAuth{IdentityFile: "/unreadable/private-key", KeyFingerprint: "SHA256:key"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Resolver{ConfigFile: path, SSHKeyCacheReadOnly: true}).Resolve("")
	if err == nil || !strings.Contains(err.Error(), "run a foreground") {
		t.Fatalf("cache-miss error = %v, want foreground instruction", err)
	}
}

func TestResolveWithRejectsKeyAuthAndKubeAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := clientconfig.Save(path, &clientconfig.File{CurrentContext: "prof", Contexts: map[string]*clientconfig.Context{
		"prof": {Server: "http://127.0.0.1:5000", KeyAuth: &clientconfig.KeyAuth{IdentityFile: "/key"}, KubeAuth: &clientconfig.KubeAuth{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Resolver{ConfigFile: path}).ResolveWith("", "env-token")
	if err == nil || !strings.Contains(err.Error(), "key-auth and kube-auth") {
		t.Fatalf("ResolveWith error = %v, want hard key-auth/kube-auth conflict even with env token", err)
	}
}

func TestResolveWithEnvTokenOutranksKeyAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := clientconfig.Save(path, &clientconfig.File{CurrentContext: "prof", Contexts: map[string]*clientconfig.Context{
		"prof": {Server: "http://127.0.0.1:5000", KeyAuth: &clientconfig.KeyAuth{IdentityFile: "/does/not/exist"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cn, err := (&Resolver{ConfigFile: path}).ResolveWith("", "env-token")
	if err != nil {
		t.Fatalf("ResolveWith attempted key auth despite env override: %v", err)
	}
	if cn.Token != "env-token" {
		t.Fatalf("Token = %q, want env-token", cn.Token)
	}
}

// TestResolveWithNoContextNoToken pins the neighbouring case, so the fix above
// cannot be "achieved" by inventing a credential from nowhere.
func TestResolveWithNoContextNoToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := clientconfig.Save(path, &clientconfig.File{}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	r := &Resolver{ConfigFile: path}

	cn, err := r.ResolveWith("http://127.0.0.1:5000", "")
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if cn.Token != "" {
		t.Fatalf("Token = %q, want empty when no token is available anywhere", cn.Token)
	}
}

// TestResolveWithEnvTokenOutranksProfile keeps the documented precedence honest
// on the path that DOES have a context, so the two paths agree.
func TestResolveWithEnvTokenOutranksProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := clientconfig.Save(path, &clientconfig.File{
		CurrentContext: "prof",
		Contexts: map[string]*clientconfig.Context{
			"prof": {Server: "http://127.0.0.1:5000", Token: "from-profile"},
		},
	})
	if err != nil {
		t.Fatalf("save config: %v", err)
	}
	r := &Resolver{ConfigFile: path}

	cn, err := r.ResolveWith("", "from-env")
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if cn.Token != "from-env" {
		t.Fatalf("Token = %q, want the env token to outrank the profile's", cn.Token)
	}

	// With no env token the profile's own credential is used.
	cn, err = r.ResolveWith("", "")
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if cn.Token != "from-profile" {
		t.Fatalf("Token = %q, want the profile token when the env is unset", cn.Token)
	}
}

// TestSessionCacheIdentityMirrorsEndpointPrecedence pins the cache identity to the
// PROFILE rather than the dialed address, and in the same order ResolveWith picks
// the endpoint. The port-forward case is the one that motivated it: that endpoint
// carries a fresh random localhost port every run, so an endpoint-keyed cache
// missed on every invocation — the foreground CLI re-minted each time and the
// read-only agent failed permanently instead of reusing the foreground's session.
func TestSessionCacheIdentityMirrorsEndpointPrecedence(t *testing.T) {
	pf := &clientconfig.PortForward{KubeContext: "kind", Namespace: "ns", Service: "cornus", RemotePort: 5000}
	st := &clientconfig.SSHTunnel{Addr: "box", RemoteAddr: "127.0.0.1:5000"}

	// Explicit --server outranks everything, matching ResolveWith.
	if got := sessionCacheIdentity("http://explicit:5000", &clientconfig.Context{Server: "http://profile:5000", PortForward: pf}); got != "server|http://explicit:5000" {
		t.Fatalf("explicit server identity = %q", got)
	}
	if got := sessionCacheIdentity("", &clientconfig.Context{Server: "http://profile:5000", PortForward: pf}); got != "server|http://profile:5000" {
		t.Fatalf("profile server identity = %q", got)
	}

	// The port-forward identity comes from the Service coordinates, so it is
	// identical across runs even though each run dials a different local port.
	first := sessionCacheIdentity("", &clientconfig.Context{PortForward: pf})
	second := sessionCacheIdentity("", &clientconfig.Context{PortForward: pf})
	if first == "" || first != second {
		t.Fatalf("port-forward identity unstable: %q vs %q", first, second)
	}
	if strings.Contains(first, "127.0.0.1") {
		t.Fatalf("port-forward identity leaked a dialed address: %q", first)
	}
	// A different Service must not share a session.
	other := *pf
	other.Service = "other"
	if same := sessionCacheIdentity("", &clientconfig.Context{PortForward: &other}); same == first {
		t.Fatalf("distinct services share identity %q", same)
	}

	if got := sessionCacheIdentity("", &clientconfig.Context{SSHTunnel: st}); got != "ssh-tunnel|box|127.0.0.1:5000" {
		t.Fatalf("ssh-tunnel identity = %q", got)
	}
	// An ssh-tunnel with no explicit remote address still resolves to the default
	// the dialer uses, so the identity cannot depend on whether it was written out.
	if got := sessionCacheIdentity("", &clientconfig.Context{SSHTunnel: &clientconfig.SSHTunnel{Addr: "box"}}); got != "ssh-tunnel|box|127.0.0.1:5000" {
		t.Fatalf("ssh-tunnel default remote identity = %q", got)
	}
	if got := sessionCacheIdentity("", &clientconfig.Context{}); got != "" {
		t.Fatalf("identity with no endpoint source = %q, want empty", got)
	}
}

// TestProviderAdoptsFresherSharedSession covers the renewal path that keeps a
// long-lived client alive: when the held session has aged out, the provider must
// pick up one another process already minted rather than sign again. newClient
// panics so any minting attempt fails the test loudly.
func TestProviderAdoptsFresherSharedSession(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	// Pin the file backend: without it the shared credential store would use the
	// OS keyring where one is reachable, and this test would seed the developer's
	// real one.
	t.Setenv("CORNUS_TOKEN_CACHE", "file")
	const identity = "server|http://127.0.0.1:5000"
	const fingerprint = "SHA256:shared"
	fresh := time.Now().Add(time.Hour)
	if err := sshclient.WriteSession(identity, fingerprint, "api", "fresh-token", fresh); err != nil {
		t.Fatal(err)
	}
	p := &sshKeyTokenProvider{
		identity: identity, fingerprint: fingerprint, scope: "api",
		newClient: func() *client.Client { panic("must not mint when a fresh shared session exists") },
	}
	p.seed("stale-token", time.Now().Add(-time.Minute))
	got, err := p.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "fresh-token" {
		t.Fatalf("Token = %q, want fresh-token", got)
	}
}

// TestProviderKeepsUnexpiredSessionWithoutTouchingTheCache is the other half: a
// still-valid session is returned from memory, so the common request path costs no
// file read and no signature.
func TestProviderKeepsUnexpiredSessionWithoutTouchingTheCache(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	// Pin the file backend: without it the shared credential store would use the
	// OS keyring where one is reachable, and this test would seed the developer's
	// real one.
	t.Setenv("CORNUS_TOKEN_CACHE", "file")
	p := &sshKeyTokenProvider{
		identity: "server|http://127.0.0.1:5000", fingerprint: "SHA256:live", scope: "api",
		newClient: func() *client.Client { panic("must not mint while the held session is live") },
	}
	p.seed("live-token", time.Now().Add(time.Hour))
	got, err := p.Token()
	if err != nil || got != "live-token" {
		t.Fatalf("Token = %q, %v; want live-token", got, err)
	}
}
