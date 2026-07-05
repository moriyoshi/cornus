package api

import (
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

func TestKnativeSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    *KnativeSpec
		wantErr string // "" => valid
	}{
		{"nil", nil, ""},
		{"empty", &KnativeSpec{}, ""},
		{"full-ok", &KnativeSpec{Enabled: true, MinScale: intp(1), MaxScale: intp(5), Target: intp(80), Concurrency: intp(100), Class: "kpa", Metric: "concurrency", TimeoutSeconds: intp(300), Port: 8080}, ""},
		{"bad-class", &KnativeSpec{Class: "wat"}, "class"},
		{"bad-metric", &KnativeSpec{Metric: "wat"}, "metric"},
		{"cpu-needs-hpa", &KnativeSpec{Metric: "cpu", Class: "kpa"}, "requires class hpa"},
		{"cpu-hpa-ok", &KnativeSpec{Metric: "cpu", Class: "hpa"}, ""},
		{"neg-min", &KnativeSpec{MinScale: intp(-1)}, "minScale"},
		{"neg-max", &KnativeSpec{MaxScale: intp(-2)}, "maxScale"},
		{"min-gt-max", &KnativeSpec{MinScale: intp(5), MaxScale: intp(2)}, "must not exceed"},
		{"min-le-unbounded-max", &KnativeSpec{MinScale: intp(5), MaxScale: intp(0)}, ""}, // max 0 = unlimited
		{"neg-target", &KnativeSpec{Target: intp(-1)}, "target"},
		{"neg-concurrency", &KnativeSpec{Concurrency: intp(-1)}, "concurrency"},
		{"neg-timeout", &KnativeSpec{TimeoutSeconds: intp(-1)}, "timeoutSeconds"},
		{"neg-port", &KnativeSpec{Port: -1}, "port"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Validate() = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}
