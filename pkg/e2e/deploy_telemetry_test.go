package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.starlark.net/starlark"

	"cornus/pkg/api"
	"cornus/pkg/client"
)

// captureDeploy points a Harness at an httptest server that records the
// api.DeploySpec a `deploy(...)` builtin actually puts on the wire.
//
// No production seam is needed: h.client is a settable field and the tests in
// this package already call the builtins directly, so the spec can be observed
// exactly as the server would receive it — kwarg unpacking, spec building, and
// JSON encoding included. That is a stronger observation point than any
// spec-builder extraction would give, because it also proves the field survives
// serialization.
func captureDeploy(t *testing.T) (*Harness, func() api.DeploySpec) {
	t.Helper()
	var got api.DeploySpec
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"app","instances":[]}`))
	}))
	t.Cleanup(srv.Close)

	h := New(nopTarget{}, "", "", io.Discard)
	h.ctx = context.Background()
	h.registryHost = "localhost:5000" // satisfies the "call serve() first" guard
	h.client = client.New(srv.URL)
	return h, func() api.DeploySpec { return got }
}

// TestDeployTelemetryAbsentVersusExplicitEmpty executes the distinction that the
// harness's own comment calls out and that only parse-level checks covered.
//
// `telemetry=""` is NOT the same as omitting the kwarg. The empty string is the
// ENDPOINT-LESS block (`x-cornus-telemetry: {}`) — the shape that makes the server
// default the destination to its own OTLP receiver. It is only expressible because
// the kwarg is unpacked as a starlark.Value: a plain string parameter cannot tell
// "absent" from "present and empty", and reading it as absent silently turned
// observability-telemetry-mux.star's headline deploy into an ordinary one. That is
// the regression this guards, and it is invisible — the scenario still passes, it
// just stops testing the mux.
//
// Enabled is what carries "the workload asked for telemetry" when there is no
// endpoint to imply it (see api.TelemetrySpec.Active).
func TestDeployTelemetryAbsentVersusExplicitEmpty(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kwargs  []starlark.Tuple
		want    *api.TelemetrySpec
		because string
	}{
		{
			name:    "omitted",
			kwargs:  nil,
			want:    nil,
			because: "a deploy that never mentions telemetry must carry no TelemetrySpec at all",
		},
		{
			name:   "explicit empty string",
			kwargs: []starlark.Tuple{{starlark.String("telemetry"), starlark.String("")}},
			want:   &api.TelemetrySpec{Endpoint: "", Enabled: true},
			because: "telemetry=\"\" is the endpoint-less block: the server defaults the destination to " +
				"its own OTLP receiver. Collapsing it to absent turns the mux scenario into an ordinary " +
				"deploy that still passes",
		},
		{
			name:    "explicit endpoint",
			kwargs:  []starlark.Tuple{{starlark.String("telemetry"), starlark.String("otelcol:4317")}},
			want:    &api.TelemetrySpec{Endpoint: "otelcol:4317", Enabled: true},
			because: "an endpoint requests the caretaker otel role and is exported verbatim",
		},
		{
			name:    "None is absent",
			kwargs:  []starlark.Tuple{{starlark.String("telemetry"), starlark.None}},
			want:    nil,
			because: "Starlark None must read as omitted, not as an endpoint-less block",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, received := captureDeploy(t)
			args := starlark.Tuple{starlark.String("app"), starlark.String("img")}
			if _, err := h.bDeploy(nil, nil, args, tc.kwargs); err != nil {
				t.Fatalf("deploy: %v", err)
			}
			got := received().Telemetry

			// Positive control: the request reached the fake server at all, so a
			// nil-vs-nil comparison cannot pass because nothing was sent.
			if received().Name != "app" {
				t.Fatalf("the fake server received no deploy (name %q); the assertions below describe nothing",
					received().Name)
			}

			switch {
			case tc.want == nil && got != nil:
				t.Errorf("telemetry = %+v, want nil: %s", got, tc.because)
			case tc.want != nil && got == nil:
				t.Errorf("telemetry = nil, want %+v: %s", tc.want, tc.because)
			case tc.want != nil && got != nil:
				if got.Endpoint != tc.want.Endpoint || got.Enabled != tc.want.Enabled {
					t.Errorf("telemetry = {Endpoint:%q Enabled:%v}, want {Endpoint:%q Enabled:%v}: %s",
						got.Endpoint, got.Enabled, tc.want.Endpoint, tc.want.Enabled, tc.because)
				}
			}
		})
	}
}

// TestDeployTelemetryEmptyIsActive pins the consequence rather than the encoding.
// api.TelemetrySpec.Active is what every downstream decision reads — whether to
// wire the caretaker otel role, whether the server defaults the endpoint — so a
// spec that round-trips the right FIELDS but answers Active false would satisfy
// the test above and still disable the feature.
func TestDeployTelemetryEmptyIsActive(t *testing.T) {
	h, received := captureDeploy(t)
	kwargs := []starlark.Tuple{{starlark.String("telemetry"), starlark.String("")}}
	if _, err := h.bDeploy(nil, nil, starlark.Tuple{starlark.String("app"), starlark.String("img")}, kwargs); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	spec := received()
	if !spec.Telemetry.Active() {
		t.Errorf("TelemetrySpec.Active() = false for telemetry=\"\"; every downstream decision reads "+
			"Active, so the endpoint-less block would be silently ignored (%+v)", spec.Telemetry)
	}
}
