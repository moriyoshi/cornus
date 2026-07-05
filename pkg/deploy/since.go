package deploy

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseSince parses a LogOptions.Since value into an absolute point in time.
// It is the one since-grammar shared by every backend, modeled on Docker's own
// timestamp parsing (api/types/time.GetTimestamp — dockerd is the superset
// reference, since the dockerhost backend historically deferred to it):
//
//   - "" — no lower bound: returns the zero time and no error
//   - Unix seconds, with an optional fractional part carrying up to
//     nanosecond precision: "1712345678", "1712345678.123456789"
//   - an RFC3339 / RFC3339Nano timestamp: "2026-07-07T10:00:00Z"
//   - a Go duration, interpreted relative to now (docker CLI style, meaning
//     "this long ago"): "10m", "1h30s"
//
// Mirroring Docker, "0" is the Unix epoch (i.e. everything), not a zero
// duration. Anything else — including silently ignoring garbage and returning
// all logs — is an error.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil && s != "0" {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, ok := parseUnixTimestamp(s); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid since value %q (want Unix seconds[.nanos], an RFC3339 timestamp, or a duration like \"10m\")", s)
}

// parseUnixTimestamp parses "seconds" or "seconds.fraction" (fraction is
// right-padded to, and truncated at, nanosecond precision, as dockerd does).
func parseUnixTimestamp(s string) (time.Time, bool) {
	secStr, fracStr, hasFrac := strings.Cut(s, ".")
	secs, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	var nanos int64
	if hasFrac && fracStr != "" {
		if len(fracStr) > 9 {
			fracStr = fracStr[:9]
		}
		frac, err := strconv.ParseUint(fracStr, 10, 64) // digits only: no sign, no second dot
		if err != nil {
			return time.Time{}, false
		}
		nanos = int64(frac)
		for i := len(fracStr); i < 9; i++ {
			nanos *= 10
		}
	}
	return time.Unix(secs, nanos), true
}
