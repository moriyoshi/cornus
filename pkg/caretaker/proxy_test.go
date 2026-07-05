package caretaker

import (
	"context"
	"fmt"
	"testing"
)

// TestAllowSetPartialDNSFailurePreservesPeer verifies that when some allowed
// names resolve and one transiently fails (e.g. a cluster-DNS SERVFAIL), the
// failing name's previously resolved IPs are preserved in the allow-set rather
// than dropped — a healthy peer must not be denied on a single-name blip.
func TestAllowSetPartialDNSFailurePreservesPeer(t *testing.T) {
	as := newAllowSet([]string{"peerA", "peerB"})

	// First cycle: both resolve.
	as.lookupHost = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "peerA":
			return []string{"10.0.0.1"}, nil
		case "peerB":
			return []string{"10.0.0.2"}, nil
		}
		return nil, fmt.Errorf("unexpected host %q", host)
	}
	as.refresh(context.Background())
	if !as.allowed("10.0.0.1") || !as.allowed("10.0.0.2") {
		t.Fatalf("after first refresh: want both peer IPs allowed, got %v", as.snapshot())
	}

	// Second cycle: peerA resolves, peerB transiently fails. peerB's prior IP
	// must survive.
	as.lookupHost = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "peerA":
			return []string{"10.0.0.1"}, nil
		case "peerB":
			return nil, fmt.Errorf("SERVFAIL")
		}
		return nil, fmt.Errorf("unexpected host %q", host)
	}
	as.refresh(context.Background())
	if !as.allowed("10.0.0.1") {
		t.Fatalf("peerA IP dropped after peerB failure: %v", as.snapshot())
	}
	if !as.allowed("10.0.0.2") {
		t.Fatalf("peerB IP dropped on transient DNS failure (fail-static violated): %v", as.snapshot())
	}

	// Third cycle: peerB recovers with a new IP; the stale preserved IP is
	// replaced by the freshly resolved one.
	as.lookupHost = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "peerA":
			return []string{"10.0.0.1"}, nil
		case "peerB":
			return []string{"10.0.0.3"}, nil
		}
		return nil, fmt.Errorf("unexpected host %q", host)
	}
	as.refresh(context.Background())
	if as.allowed("10.0.0.2") {
		t.Fatalf("stale peerB IP not replaced after recovery: %v", as.snapshot())
	}
	if !as.allowed("10.0.0.3") {
		t.Fatalf("recovered peerB IP missing: %v", as.snapshot())
	}
}
