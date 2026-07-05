package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"cornus/pkg/api"
)

func TestRoutePattern(t *testing.T) {
	cases := map[string]string{
		"/healthz":                         "/healthz",
		"/readyz":                          "/readyz",
		"/.cornus/v1/build":                "/.cornus/v1/build",
		"/.cornus/v1/deploy":               "/.cornus/v1/deploy",
		"/.cornus/v1/deploy/attach":        "/.cornus/v1/deploy/attach",
		"/.cornus/v1/caretaker/attach":     "/.cornus/v1/caretaker/attach",
		"/.cornus/v1/deploy/web":           "/.cornus/v1/deploy/{name}",
		"/.cornus/v1/deploy/web/restart":   "/.cornus/v1/deploy/{name}/restart",
		"/.cornus/v1/deploy/exec/abc123":   "/.cornus/v1/deploy/exec/{id}",
		"/v2/":                             "/v2/",
		"/v2/_catalog":                     "/v2/_catalog",
		"/v2/lib/app/blobs/sha256:dead":    "/v2/{name}/blobs/{digest}",
		"/v2/lib/app/blobs/uploads/":       "/v2/{name}/blobs/uploads",
		"/v2/lib/app/blobs/uploads/uuid-1": "/v2/{name}/blobs/uploads/{uuid}",
		"/v2/lib/app/manifests/v1":         "/v2/{name}/manifests/{reference}",
		"/v2/lib/app/tags/list":            "/v2/{name}/tags/list",
	}
	for in, want := range cases {
		if got := routePattern(in); got != want {
			t.Errorf("routePattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestServerEmitsSpans drives a deploy request through the otelhttp-wrapped
// handler with a recording TracerProvider installed globally, and asserts both
// the HTTP server span (named by the route template) and the cornus deploy
// span are recorded. Hermetic: no collector, no network.
func TestServerEmitsSpans(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/web:v1"}
	body, _ := json.Marshal(spec)
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := tp.ForceFlush(t.Context()); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sr.Ended() {
		names[s.Name()] = true
	}
	if !names["POST /.cornus/v1/deploy"] {
		t.Errorf("missing HTTP server span %q; got %v", "POST /.cornus/v1/deploy", names)
	}
	if !names["cornus.deploy.apply"] {
		t.Errorf("missing deploy span %q; got %v", "cornus.deploy.apply", names)
	}
}
