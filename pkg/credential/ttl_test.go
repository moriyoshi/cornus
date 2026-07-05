package credential

import (
	"testing"
	"time"
)

// TestParseTTL moved here with ParseTTL itself, from pkg/caretaker.
func TestParseTTL(t *testing.T) {
	if ParseTTL("") != DefaultTTL || ParseTTL("bogus") != DefaultTTL {
		t.Fatal("empty/bogus TTL should fall back to the default")
	}
	if ParseTTL("30s") != 30*time.Second {
		t.Fatal("valid TTL not parsed")
	}
	// A non-positive duration parses cleanly but would mean "refresh forever",
	// so it must take the default too rather than becoming a busy loop.
	if ParseTTL("0s") != DefaultTTL || ParseTTL("-1m") != DefaultTTL {
		t.Fatal("non-positive TTL should fall back to the default")
	}
}

// TestExpiryPrefersTheEarlierDeadline is the arithmetic two independent
// consumers now share — the caretaker refreshing a file inside a pod, and the
// server refreshing one it wrote into a workload itself. It had no test before
// the extraction, which is exactly why it was worth extracting rather than
// copying.
func TestExpiryPrefersTheEarlierDeadline(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

	// No expiry on the credential: the TTL alone decides.
	if got := Expiry(now, time.Time{}, time.Hour); !got.Equal(now.Add(time.Hour)) {
		t.Errorf("no-expiry deadline = %v, want now+1h", got)
	}
	// A short-lived credential under a long TTL must drive refresh, minus skew.
	exp := now.Add(2 * time.Minute)
	if got := Expiry(now, exp, time.Hour); !got.Equal(exp.Add(-30 * time.Second)) {
		t.Errorf("short-expiry deadline = %v, want the skew-trimmed expiry %v", got, exp.Add(-30*time.Second))
	}
	// A long-lived credential under a short TTL: the TTL wins.
	if got := Expiry(now, now.Add(24*time.Hour), time.Minute); !got.Equal(now.Add(time.Minute)) {
		t.Errorf("long-expiry deadline = %v, want now+1m", got)
	}
}

// TestExpiryNeverCachesAnAlreadyExpiredValue pins the clamp. Without it a
// credential whose expiry is already inside the skew window yields a deadline in
// the PAST, and a consumer comparing now.Before(expiry) would treat it as fresh
// exactly once — serving a secret it had already decided was stale.
func TestExpiryNeverCachesAnAlreadyExpiredValue(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	for _, exp := range []time.Time{
		now.Add(-time.Hour),  // long gone
		now,                  // exactly now
		now.Add(time.Second), // inside the 30s skew window
	} {
		if got := Expiry(now, exp, time.Hour); got.Before(now) {
			t.Errorf("Expiry(now, %v) = %v, which is in the past", exp, got)
		}
	}
}
