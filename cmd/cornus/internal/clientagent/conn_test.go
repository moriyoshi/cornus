package clientagent

import (
	"testing"

	"cornus/pkg/clientconduit"
	"cornus/pkg/socks5"
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

// The conduit identity hashes the RAW socks5 fields, but the engine substitutes
// its own defaults for the empty spellings. Without canonicalization, `--conduit
// socks5` and `--conduit socks5://.shared:1080` describe one proxy under two keys —
// and the second frontend to arrive forks a conduit that can only fail to bind.
func TestCanonicalConduitCfgAppliesEngineDefaults(t *testing.T) {
	got := canonicalConduitCfg(ConduitCfg{Mode: clientconduit.ModeSocks5})
	if got.Socks5Listen != socks5.DefaultListen {
		t.Errorf("Socks5Listen = %q, want socks5.Start's substitution %q", got.Socks5Listen, socks5.DefaultListen)
	}
	if got.Socks5Suffix != socks5.DefaultSuffix {
		t.Errorf("Socks5Suffix = %q, want NewSuffixRouter's substitution %q", got.Socks5Suffix, socks5.DefaultSuffix)
	}
	if got.Socks5BareServiceNames == nil || !*got.Socks5BareServiceNames {
		t.Errorf("Socks5BareServiceNames = %v, want an explicit true (NewRouter enables it)", got.Socks5BareServiceNames)
	}

	// Explicit rules REPLACE the suffix default, so the suffix reaches nothing the
	// proxy does and must not split the identity.
	rules := ConduitCfg{
		Mode:          clientconduit.ModeSocks5,
		Socks5Suffix:  ".ignored.internal",
		Socks5Resolve: []socks5.Rule{{Pattern: "^x:(.*)$", Replace: `y:\1`}},
	}
	if got := canonicalConduitCfg(rules); got.Socks5Suffix != "" {
		t.Errorf("Socks5Suffix = %q with explicit rules, want it dropped (it is inert)", got.Socks5Suffix)
	}

	// Nothing here may leak onto the other modes, whose identities never depended
	// on these fields.
	if got := canonicalConduitCfg(ConduitCfg{Mode: clientconduit.ModePortForward}); got.Socks5Listen != "" || got.Socks5Suffix != "" || got.Socks5BareServiceNames != nil {
		t.Errorf("port-forward config gained socks5 defaults: %+v", got)
	}
}

// The behavioral half, and the one that catches a canonicalization applied in the
// wrong place: equivalent SPELLINGS must key ONE conduit, not two.
func TestConduitKeyOfIgnoresEquivalentSpellings(t *testing.T) {
	on := true
	base := ConduitCfg{Mode: clientconduit.ModeSocks5}
	spelled := ConduitCfg{
		Mode:                   clientconduit.ModeSocks5,
		Socks5Listen:           socks5.DefaultListen,
		Socks5Suffix:           socks5.DefaultSuffix,
		Socks5BareServiceNames: &on,
	}
	if conduitKeyOf(base, "s") != conduitKeyOf(spelled, "s") {
		t.Fatal("the empty and explicit spellings of the socks5 defaults key two different conduits; the second would fork and then fail to bind")
	}

	// And settings that differ at the SAME ADDRESS now key ONE conduit, because the
	// address is what a conduit is: two of them cannot both bind it, so they were
	// never two conduits — they were one, plus a collision. The incumbent's settings
	// govern and the later caller is warned.
	other := spelled
	other.Socks5Suffix = ".other.internal"
	if conduitKeyOf(spelled, "s") != conduitKeyOf(other, "s") {
		t.Fatal("two configurations naming one address key two conduits; the second would fork and then fail to bind")
	}

	// A different address is a different conduit, which is the only thing that
	// should split them.
	elsewhere := spelled
	elsewhere.Socks5Listen = "127.0.0.1:1081"
	if conduitKeyOf(spelled, "s") == conduitKeyOf(elsewhere, "s") {
		t.Fatal("two different addresses key one conduit")
	}

	// An EPHEMERAL socks5 conduit is private in the sense that matters — no other
	// PROCESS can find it, since it is never advertised — but two frontends in THIS
	// process with identical settings still share one bound port. A browser has one
	// proxy setting, so giving each its own random port would leave it able to reach
	// only one of them.
	eph := spelled
	eph.Socks5Listen = "127.0.0.1:0"
	if conduitKeyOf(eph, "one") != conduitKeyOf(eph, "two") {
		t.Fatal("two frontends with identical ephemeral settings key two conduits; they must share one bound port")
	}

	// Unless the configuration asked for a private proxy, which is per session.
	local := eph
	local.Socks5SessionLocal = true
	if conduitKeyOf(local, "one") == conduitKeyOf(local, "two") {
		t.Fatal("two session-local conduits share one; each session must get its own")
	}
}
