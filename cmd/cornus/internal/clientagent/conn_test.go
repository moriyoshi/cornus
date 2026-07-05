package clientagent

import (
	"testing"

	"cornus/pkg/clientconduit"
)

// TestConduitKeyOfSessionLocalIsolation covers the sharing boundary: a shared
// conduit keys on its config alone (so sessions join one proxy), while a
// session-local one folds in the session id (so each gets its own).
func TestConduitKeyOfSessionLocalIsolation(t *testing.T) {
	shared := ConduitCfg{Mode: clientconduit.ModeSocks5, Socks5Listen: "127.0.0.1:1080"}
	local := ConduitCfg{Mode: clientconduit.ModeSocks5, Socks5Listen: "127.0.0.1:0", Socks5SessionLocal: true}

	// Shared: the session id does not affect the key, so two projects share one proxy.
	if conduitKeyOf(shared, "projA") != conduitKeyOf(shared, "projB") {
		t.Errorf("shared conduit key must not depend on the session id")
	}

	// Session-local: different sessions get different keys (their own proxies)...
	if conduitKeyOf(local, "projA") == conduitKeyOf(local, "projB") {
		t.Errorf("session-local conduit keys must differ per session")
	}
	// ...but the same session resolves to the same key (a re-up reuses its proxy).
	if conduitKeyOf(local, "projA") != conduitKeyOf(local, "projA") {
		t.Errorf("session-local conduit key must be stable for one session")
	}

	// A shared and a session-local conduit never collide, so they coexist.
	if conduitKeyOf(shared, "projA") == conduitKeyOf(local, "projA") {
		t.Errorf("shared and session-local keys must differ so both proxies can coexist")
	}
}
