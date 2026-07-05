package deploy

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	rfc := time.Date(2026, 7, 6, 10, 30, 0, 0, time.UTC)
	cases := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		// Empty: no lower bound.
		{in: "", want: time.Time{}},
		// Unix seconds, with and without a fractional part.
		{in: "1712345678", want: time.Unix(1712345678, 0)},
		{in: "1712345678.123456789", want: time.Unix(1712345678, 123456789)},
		{in: "1712345678.5", want: time.Unix(1712345678, 500000000)},
		{in: "1712345678.", want: time.Unix(1712345678, 0)},
		{in: "1712345678.1234567891", want: time.Unix(1712345678, 123456789)}, // truncated at nanos
		{in: "-1", want: time.Unix(-1, 0)},
		// "0" is the epoch (docker GetTimestamp), not a zero duration.
		{in: "0", want: time.Unix(0, 0)},
		// RFC3339 / RFC3339Nano.
		{in: "2026-07-06T10:30:00Z", want: rfc},
		{in: "2026-07-06T10:30:00.000000001Z", want: rfc.Add(time.Nanosecond)},
		{in: "2026-07-06T12:30:00+02:00", want: rfc}, // same instant as 10:30Z
		// Durations, relative to now ("this long ago").
		{in: "10m", want: now.Add(-10 * time.Minute)},
		{in: "1h30m", want: now.Add(-90 * time.Minute)},
		{in: "1s", want: now.Add(-time.Second)},
		// Garbage is an error, never "all logs".
		{in: "garbage", wantErr: true},
		{in: "not-a-time", wantErr: true},
		{in: "10 minutes", wantErr: true},
		{in: "12.34.56", wantErr: true},
		{in: "1712345678.xyz", wantErr: true},
		{in: "1712345678.-5", wantErr: true},
		{in: ".5", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseSince(c.in, now)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseSince(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSince(%q): unexpected error: %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("ParseSince(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
