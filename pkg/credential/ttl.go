package credential

import "time"

// DefaultTTL bounds how long a fetched credential is reused when it carries no
// expiry and its consumer sets none — short enough that a rotated upstream
// credential is picked up promptly, long enough to avoid re-minting per request.
const DefaultTTL = 5 * time.Minute

// expirySkew is trimmed off a credential's own expiry so it is re-minted before
// it actually expires (clock skew + propagation).
const expirySkew = 30 * time.Second

// ParseTTL reads a consumer's TTL hint, falling back to DefaultTTL for an empty,
// malformed, or non-positive value. It does not error: a bad TTL is a reason to
// refresh on the default cadence, not a reason to stop serving a credential.
func ParseTTL(s string) time.Duration {
	if s == "" {
		return DefaultTTL
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return DefaultTTL
}

// Expiry is the earliest of the credential's own (skew-trimmed) expiry and
// now+ttl, so a short-lived upstream credential drives refresh even under a long
// TTL. A deadline already in the past returns now, so a value past its skewed
// expiry is never cached.
//
// This lives here, next to the Credential it interprets, because two independent
// consumers need to agree on it: the caretaker refreshing a file inside a pod,
// and the server refreshing one it wrote into a workload itself. Two copies of
// this arithmetic would be two answers to "when is this credential stale", and
// the disagreement would only ever show up as a workload holding a secret the
// other path had already replaced.
func Expiry(now, exp time.Time, ttl time.Duration) time.Time {
	deadline := now.Add(ttl)
	if !exp.IsZero() {
		if e := exp.Add(-expirySkew); e.Before(deadline) {
			deadline = e
		}
	}
	if deadline.Before(now) {
		return now
	}
	return deadline
}
