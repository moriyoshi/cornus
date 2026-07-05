package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	gofs "io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"cornus/pkg/api"
	"cornus/pkg/build/buildprog"
	"cornus/pkg/build/buildwire"
	"cornus/pkg/observability"
)

// fakeServer records the requests the client makes.
type fakeServer struct {
	applied          []api.DeploySpec
	deleted          []string
	actions          []string
	buildFiles       map[string]string // path -> content received over 9P
	buildTarget      string
	buildTargetStage string
	buildArgs        map[string]string
	buildCacheImport []buildwire.CacheOption
	buildSSHIDs      []string
	// The full spec plus the dockerfile-mount content, captured for the extended
	// build-key tests (pull/labels/platforms/tags/network/extra_hosts/shm_size/
	// cache_to and dockerfile_inline).
	buildSpec       buildwire.BuildSpec
	buildDockerfile string // content of the served "dockerfile" tree's Dockerfile
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/deploy", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var spec api.DeploySpec
			_ = json.NewDecoder(r.Body).Decode(&spec)
			f.applied = append(f.applied, spec)
			_ = json.NewEncoder(w).Encode(api.DeployStatus{
				Name: spec.Name, Image: spec.Image,
				Instances: []api.InstanceStatus{{ID: "x", State: "running", Running: true}},
			})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]api.DeployStatus{{Name: "shop-web", Image: "img"}})
		}
	})
	mux.HandleFunc("/.cornus/v1/deploy/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/")
		name, action, hasAction := strings.Cut(rest, "/")
		switch {
		case hasAction:
			f.actions = append(f.actions, action+":"+name)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			f.deleted = append(f.deleted, name)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(api.DeployStatus{Name: name})
		}
	})
	mux.HandleFunc("/.cornus/v1/build/attach", func(w http.ResponseWriter, r *http.Request) {
		s, err := buildwire.Attach(w, r)
		if err != nil {
			return
		}
		defer s.Close()
		f.buildTarget = s.Spec.Target
		f.buildTargetStage = s.Spec.TargetStage
		f.buildArgs = s.Spec.BuildArgs
		f.buildCacheImport = s.Spec.CacheImports
		f.buildSSHIDs = s.Spec.SSHIDs
		f.buildSpec = s.Spec
		// Capture the served Dockerfile from the "dockerfile" mount (covers the
		// inline-dockerfile path, which stages a synthetic Dockerfile tree).
		if dfFS := s.Mounts()["dockerfile"]; dfFS != nil {
			if rc, err := dfFS.Open(s.Spec.DockerfileName); err == nil {
				b, _ := io.ReadAll(rc)
				rc.Close()
				f.buildDockerfile = string(b)
			}
		}
		f.buildFiles = map[string]string{}
		fsys := s.Mounts()["context"]
		_ = fsys.Walk(context.Background(), "", func(p string, d gofs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rc, err := fsys.Open(p)
			if err != nil {
				return err
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			f.buildFiles[p] = string(b)
			return nil
		})
		s.Progress().Log("BUILD OK\n")
		_ = s.Done(&buildwire.Result{ImageDigest: "sha256:test"}, nil)
	})
	return mux
}

func TestClientDeployLifecycle(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()

	st, err := c.Deploy(ctx, api.DeploySpec{Name: "shop-web", Image: "img:v1"})
	if err != nil || st.Name != "shop-web" {
		t.Fatalf("Deploy = %+v, %v", st, err)
	}
	if len(f.applied) != 1 || f.applied[0].Image != "img:v1" {
		t.Fatalf("applied = %+v", f.applied)
	}

	list, err := c.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}

	for _, a := range []string{"start", "stop", "restart"} {
		if err := c.Action(ctx, "shop-web", a); err != nil {
			t.Fatalf("Action %s: %v", a, err)
		}
	}
	if strings.Join(f.actions, ",") != "start:shop-web,stop:shop-web,restart:shop-web" {
		t.Fatalf("actions = %v", f.actions)
	}

	if err := c.Delete(ctx, "shop-web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "shop-web" {
		t.Fatalf("deleted = %v", f.deleted)
	}
}

// TestClientDeployError confirms a non-200 apply surfaces the server's
// {"error": ...} message (the detached `cornus deploy --detach` path relies on
// this to report a failed stateless apply).
func TestClientDeployError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "apply exploded"})
	}))
	defer srv.Close()
	c := New(srv.URL)

	_, err := c.Deploy(context.Background(), api.DeploySpec{Name: "shop-web", Image: "img:v1"})
	if err == nil {
		t.Fatal("Deploy on 500 = nil error, want error")
	}
	if !strings.Contains(err.Error(), "apply exploded") || !strings.Contains(err.Error(), "500") {
		t.Errorf("Deploy error = %q, want the status and the server's error message", err)
	}
}

// A ws:// or wss:// base — the spelling WebSocket-heavy surfaces (deploy-attach,
// the e2e harness) pass around — must work for plain HTTP methods too.
func TestClientWSBaseNormalized(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	c := New("ws://" + strings.TrimPrefix(srv.URL, "http://"))
	if _, err := c.Deploy(context.Background(), api.DeploySpec{Name: "shop-web", Image: "img:v1"}); err != nil {
		t.Fatalf("Deploy over a ws:// base: %v", err)
	}
	if got := New("wss://example.test/").base; got != "https://example.test" {
		t.Errorf("wss base normalized to %q, want https://example.test", got)
	}
}

// trailerStreamHandler streams body, flushes (forcing chunked encoding), then
// sets the X-Cornus-Stream-Error trailer to trailerErr — the server-side shape
// of a backend that fails after output has begun. An empty trailerErr models a
// clean stream. extraHeader lets the archive test carry the path-stat header.
func trailerStreamHandler(body, trailerErr string, extraHeader map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", api.StreamErrorTrailer)
		for k, v := range extraHeader {
			w.Header().Set(k, v)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		w.(http.Flusher).Flush()
		if trailerErr != "" {
			w.Header().Set(api.StreamErrorTrailer, trailerErr)
		}
	})
}

// TestClientStreamErrorTrailer confirms the streaming client methods drain the
// body and then surface a non-empty X-Cornus-Stream-Error trailer as an error
// (partial output stays delivered), and return nil when the trailer is unset.
// Trailers only round-trip over a real connection, hence httptest.NewServer.
func TestClientStreamErrorTrailer(t *testing.T) {
	ctx := context.Background()

	t.Run("logs error", func(t *testing.T) {
		srv := httptest.NewServer(trailerStreamHandler("partial-frames", "backend died mid-stream", nil))
		defer srv.Close()
		var buf bytes.Buffer
		err := New(srv.URL).Logs(ctx, "web", api.LogOptions{}, &buf)
		if err == nil {
			t.Fatal("Logs with stream-error trailer = nil, want error")
		}
		if !strings.Contains(err.Error(), "stream error after partial output") ||
			!strings.Contains(err.Error(), "backend died mid-stream") {
			t.Fatalf("Logs error = %q, want wrapped trailer message", err)
		}
		if buf.String() != "partial-frames" {
			t.Fatalf("partial body = %q, want %q", buf.String(), "partial-frames")
		}
	})

	t.Run("logs clean", func(t *testing.T) {
		srv := httptest.NewServer(trailerStreamHandler("all-frames", "", nil))
		defer srv.Close()
		var buf bytes.Buffer
		if err := New(srv.URL).Logs(ctx, "web", api.LogOptions{}, &buf); err != nil {
			t.Fatalf("Logs on clean stream = %v, want nil", err)
		}
		if buf.String() != "all-frames" {
			t.Fatalf("body = %q", buf.String())
		}
	})

	t.Run("stats error", func(t *testing.T) {
		srv := httptest.NewServer(trailerStreamHandler(`{"read":"now"}`, "collector died", nil))
		defer srv.Close()
		var buf bytes.Buffer
		err := New(srv.URL).Stats(ctx, "web", api.StatsOptions{Stream: true}, &buf)
		if err == nil || !strings.Contains(err.Error(), "collector died") {
			t.Fatalf("Stats error = %v, want the trailer message", err)
		}
		if buf.String() != `{"read":"now"}` {
			t.Fatalf("partial body = %q", buf.String())
		}
	})

	t.Run("copyfrom error", func(t *testing.T) {
		stat, err := api.EncodePathStat(api.PathStat{Name: "/data", Size: 3})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(trailerStreamHandler("TAR", "copy interrupted",
			map[string]string{api.PathStatHeader: stat}))
		defer srv.Close()
		var buf bytes.Buffer
		_, err = New(srv.URL).CopyFrom(ctx, "web", "/data", &buf)
		if err == nil || !strings.Contains(err.Error(), "copy interrupted") {
			t.Fatalf("CopyFrom error = %v, want the trailer message", err)
		}
		if buf.String() != "TAR" {
			t.Fatalf("partial tar = %q", buf.String())
		}
	})
}

// TestLocalMountSources confirms the caller-local (9P-served, session-bound)
// mount sources are picked out of a spec and server-host named-volume sources
// are not.
func TestLocalMountSources(t *testing.T) {
	spec := api.DeploySpec{
		Name: "shop-web",
		Mounts: []api.Mount{
			{Source: "/srv/data", Target: "/data"},
			{Source: "named-vol", Target: "/cache"},
			{Source: "./local", Target: "/app"},
			{Source: "~/home", Target: "/home/app"},
		},
	}
	got := LocalMountSources(spec)
	want := []string{"/srv/data", "./local", "~/home"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("LocalMountSources = %v, want %v", got, want)
	}

	if got := LocalMountSources(api.DeploySpec{Mounts: []api.Mount{{Source: "vol", Target: "/v"}}}); got != nil {
		t.Errorf("LocalMountSources(named only) = %v, want nil", got)
	}
}

func TestClientBuild(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL)

	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "app.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	var progress bytes.Buffer
	err := c.Build(context.Background(), BuildRequest{
		ContextDir: ctxDir,
		Tag:        "localhost:5000/shop-app:latest",
		Args:       map[string]string{"VERSION": "1.2.3"},
		Push:       true,
	}, buildprog.NewSink(&progress, buildprog.Plain, false))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f.buildFiles["Dockerfile"] != "FROM scratch\n" || f.buildFiles["app.txt"] != "hi" {
		t.Fatalf("build files received over 9P = %v", f.buildFiles)
	}
	if f.buildTarget != "localhost:5000/shop-app:latest" {
		t.Fatalf("build target = %q", f.buildTarget)
	}
	if f.buildArgs["VERSION"] != "1.2.3" {
		t.Fatalf("build args = %v", f.buildArgs)
	}
	if !strings.Contains(progress.String(), "BUILD OK") {
		t.Fatalf("progress = %q", progress.String())
	}
}

// TestClientBuildTargetAndCacheFrom verifies build.target rides the wire as
// BuildSpec.TargetStage and that cache_from refs are folded into type=registry
// cache imports (the existing --cache-from plumbing).
func TestClientBuildTargetAndCacheFrom(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL)

	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := c.Build(context.Background(), BuildRequest{
		ContextDir: ctxDir,
		Tag:        "localhost:5000/app:latest",
		Target:     "builder",
		CacheFrom:  []string{"reg/app:cache", ""},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f.buildTargetStage != "builder" {
		t.Fatalf("target stage = %q want %q", f.buildTargetStage, "builder")
	}
	// The empty cache_from entry is dropped; the ref becomes a registry import.
	if len(f.buildCacheImport) != 1 {
		t.Fatalf("cache imports = %+v want 1 entry", f.buildCacheImport)
	}
	ci := f.buildCacheImport[0]
	if ci.Type != "registry" || ci.Attrs["ref"] != "reg/app:cache" {
		t.Fatalf("cache import = %+v want registry ref=reg/app:cache", ci)
	}
}

// TestClientBuildSSH verifies BuildRequest.SSH ids ride the wire as
// BuildSpec.SSHIDs (sorted), so the server opens agent tunnels for them.
func TestClientBuildSSH(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL)

	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := c.Build(context.Background(), BuildRequest{
		ContextDir: ctxDir,
		Tag:        "localhost:5000/app:latest",
		SSH:        map[string]string{"mykey": "/tmp/a.sock", "default": "/tmp/b.sock"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Join(f.buildSSHIDs, ",") != "default,mykey" {
		t.Fatalf("ssh ids = %v want [default mykey]", f.buildSSHIDs)
	}
}

// TestClientBuildKeys verifies the extended compose build keys ride the wire:
// pull/labels/platforms/tags/network/extra_hosts/shm_size land on the BuildSpec,
// and cache_to specs are parsed into CacheExports (a buildx spec and a bare ref).
func TestClientBuildKeys(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL)

	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := c.Build(context.Background(), BuildRequest{
		ContextDir: ctxDir,
		Tag:        "localhost:5000/app:latest",
		Pull:       true,
		Labels:     map[string]string{"com.example.k": "v"},
		Platforms:  []string{"linux/amd64", "linux/arm64"},
		Tags:       []string{"reg/app:1.0"},
		Network:    "host",
		ExtraHosts: []string{"db:10.0.0.1"},
		ShmSize:    134217728,
		CacheTo:    []string{"type=registry,ref=reg/app:cache,mode=max", "reg/app:bare"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	sp := f.buildSpec
	if !sp.Pull {
		t.Error("Pull did not ride the wire")
	}
	if sp.Labels["com.example.k"] != "v" {
		t.Errorf("Labels = %v", sp.Labels)
	}
	if strings.Join(sp.Platforms, ",") != "linux/amd64,linux/arm64" {
		t.Errorf("Platforms = %v", sp.Platforms)
	}
	if strings.Join(sp.Tags, ",") != "reg/app:1.0" {
		t.Errorf("Tags = %v", sp.Tags)
	}
	if sp.Network != "host" {
		t.Errorf("Network = %q", sp.Network)
	}
	if strings.Join(sp.ExtraHosts, ",") != "db:10.0.0.1" {
		t.Errorf("ExtraHosts = %v", sp.ExtraHosts)
	}
	if sp.ShmSize != 134217728 {
		t.Errorf("ShmSize = %d", sp.ShmSize)
	}
	if len(sp.CacheExports) != 2 {
		t.Fatalf("CacheExports = %+v want 2", sp.CacheExports)
	}
	if e := sp.CacheExports[0]; e.Type != "registry" || e.Attrs["ref"] != "reg/app:cache" || e.Attrs["mode"] != "max" {
		t.Errorf("CacheExports[0] = %+v", e)
	}
	// A bare ref (no "=") defaults to type=registry,ref=<ref>.
	if e := sp.CacheExports[1]; e.Type != "registry" || e.Attrs["ref"] != "reg/app:bare" {
		t.Errorf("CacheExports[1] = %+v", e)
	}
}

// TestClientBuildDockerfileInline verifies build.dockerfile_inline supersedes the
// context Dockerfile: the served "dockerfile" tree carries the inline body under
// a synthetic "Dockerfile", regardless of req.Dockerfile.
func TestClientBuildDockerfileInline(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := New(srv.URL)

	ctxDir := t.TempDir()
	// A stale on-disk Dockerfile that must NOT be used when inline is set.
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inline := "FROM alpine\nRUN echo inline\n"
	err := c.Build(context.Background(), BuildRequest{
		ContextDir:       ctxDir,
		Tag:              "localhost:5000/app:latest",
		Dockerfile:       "Dockerfile",
		DockerfileInline: inline,
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if f.buildSpec.DockerfileName != "Dockerfile" {
		t.Errorf("DockerfileName = %q want Dockerfile", f.buildSpec.DockerfileName)
	}
	if f.buildDockerfile != inline {
		t.Errorf("served Dockerfile = %q want %q", f.buildDockerfile, inline)
	}
}

func TestClientHost(t *testing.T) {
	if h := New("http://localhost:5000").Host(); h != "localhost:5000" {
		t.Fatalf("Host = %q", h)
	}
}

// TestClientAuthHeader checks that WithToken sets Authorization: Bearer on plain
// /.cornus/v1/* requests, and that no token means no header.
func TestClientAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]api.DeployStatus{})
	}))
	defer srv.Close()

	// With a token.
	if _, err := New(srv.URL, WithToken("abc123")).List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotAuth != "Bearer abc123" {
		t.Fatalf("Authorization = %q, want Bearer abc123", gotAuth)
	}

	// Without a token: ensure the env default does not leak in.
	t.Setenv("CORNUS_TOKEN", "")
	gotAuth = "sentinel"
	if _, err := New(srv.URL).List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
}

// TestClientTLSConfig checks WithTLSConfig makes the REST transport trust a
// server whose CA is not in the system store: the request succeeds with the CA
// configured and fails without it.
func TestClientTLSConfig(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]api.DeployStatus{})
	}))
	defer srv.Close()

	// Without the server's CA the handshake must fail.
	if _, err := New(srv.URL).List(context.Background()); err == nil {
		t.Fatal("List over untrusted TLS = nil error, want verification failure")
	}

	// Trusting the server's certificate via WithTLSConfig succeeds.
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	if _, err := New(srv.URL, WithTLSConfig(&tls.Config{RootCAs: pool})).List(context.Background()); err != nil {
		t.Fatalf("List with CA trusted: %v", err)
	}
}

// TestClientTransportInstrumented checks that New wraps the HTTP transport in
// otelhttp so every REST request gets a client span and W3C trace-context
// injection (the client->server half of distributed tracing), on both the
// plain-HTTP and custom-TLS paths.
func TestClientTransportInstrumented(t *testing.T) {
	if _, ok := New("http://example.test").http.Transport.(*otelhttp.Transport); !ok {
		t.Errorf("plain transport = %T, want *otelhttp.Transport", New("http://example.test").http.Transport)
	}
	c := New("https://example.test", WithTLSConfig(&tls.Config{}))
	if _, ok := c.http.Transport.(*otelhttp.Transport); !ok {
		t.Errorf("TLS transport = %T, want *otelhttp.Transport", c.http.Transport)
	}
}

// TestBaseTransportPreservesDefaults checks that the underlying transport (wrapped
// by otelhttp in New) keeps its proxy support and HTTP/2 attempt — it is cloned
// from http.DefaultTransport — rather than dropping them via a bare transport,
// and applies a custom TLS config when given one.
func TestBaseTransportPreservesDefaults(t *testing.T) {
	tr := baseTransport(&tls.Config{}, nil)
	if tr.Proxy == nil {
		t.Error("Transport.Proxy is nil; HTTP(S)_PROXY support was dropped")
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("Transport.ForceAttemptHTTP2 is false; HTTP/2 was disabled")
	}
	if tr.TLSClientConfig == nil {
		t.Error("Transport.TLSClientConfig not set")
	}
}

// withTracing installs a real (always-sample) tracer provider and the W3C
// TraceContext propagator for the duration of a test, restoring the previous
// globals on cleanup. It returns a tracer to open a root span with, so a client
// call has a valid span context to inject.
func withTracing(t *testing.T) trace.Tracer {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return tp.Tracer("test")
}

// TestClientInjectsTraceContext is the core client->server propagation check: a
// REST call made under an active span carries a W3C traceparent naming that
// span's trace, so the server's handler span becomes a child of the client's.
func TestClientInjectsTraceContext(t *testing.T) {
	tracer := withTracing(t)

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		_ = json.NewEncoder(w).Encode([]api.DeployStatus{})
	}))
	defer srv.Close()

	ctx, span := tracer.Start(context.Background(), "root")
	defer span.End()
	if _, err := New(srv.URL).List(ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotTraceparent == "" {
		t.Fatal("server received no traceparent header; trace context was not injected")
	}
	wantTrace := span.SpanContext().TraceID().String()
	if !strings.Contains(gotTraceparent, wantTrace) {
		t.Errorf("traceparent %q does not carry the client trace id %q", gotTraceparent, wantTrace)
	}
}

// TestDialHeaderCarriesTraceAndAuth checks the WebSocket-dial header builder: it
// injects the active span's trace context, adds bearer auth when a token is set,
// and returns nil when there is nothing to send (no token, no span context).
func TestDialHeaderCarriesTraceAndAuth(t *testing.T) {
	tracer := withTracing(t)
	ctx, span := tracer.Start(context.Background(), "root")
	defer span.End()

	h := New("http://x", WithToken("tok")).dialHeader(ctx)
	if h.Get("traceparent") == "" {
		t.Error("dialHeader missing traceparent under an active span")
	}
	if got := h.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tok")
	}

	// No token and no span context (context.Background has no span) -> nil header,
	// matching the wire dial helpers' nil-means-no-header contract.
	if got := New("http://x").dialHeader(context.Background()); got != nil {
		t.Errorf("dialHeader with no token and no span = %v, want nil", got)
	}
}

// TestInjectHTTPDisabled confirms the injector is a zero-cost no-op when no
// propagator/span is active: an empty header, so dialHeader collapses to nil.
func TestInjectHTTPDisabled(t *testing.T) {
	if h := observability.InjectHTTP(context.Background()); len(h) != 0 {
		t.Errorf("InjectHTTP with no active span = %v, want empty", h)
	}
}

// TestReadPortForwardAckCtxCancel confirms the udp ack read unblocks when ctx is
// cancelled, rather than hanging forever on a server that upgraded the WS but
// never sent the ack line.
func TestReadPortForwardAckCtxCancel(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- readPortForwardAck(ctx, c1) }()

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("readPortForwardAck on cancelled ctx = nil, want error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readPortForwardAck did not return after ctx cancel (hung)")
	}
}

// TestReadPortForwardAck exercises the happy path and an error-carrying ack over
// an in-memory pipe. A successful read must also clear the read deadline so the
// datagram stream that follows is unaffected.
func TestReadPortForwardAck(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		go func() {
			b, _ := json.Marshal(api.PortForwardAck{})
			_, _ = c2.Write(append(b, '\n'))
		}()
		if err := readPortForwardAck(context.Background(), c1); err != nil {
			t.Fatalf("readPortForwardAck = %v, want nil", err)
		}
		// The deadline was cleared: a subsequent short-deadline read should hit
		// its own timeout, not an already-elapsed one from the ack read.
		_ = c1.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		one := make([]byte, 1)
		_, err := c1.Read(one)
		if err == nil {
			t.Fatal("expected a timeout on the idle pipe read")
		}
		if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
			t.Fatalf("post-ack read err = %v, want a timeout (deadline was left dirty?)", err)
		}
	})

	t.Run("ack error", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		go func() {
			b, _ := json.Marshal(api.PortForwardAck{Error: "backend cannot forward udp"})
			_, _ = c2.Write(append(b, '\n'))
		}()
		err := readPortForwardAck(context.Background(), c1)
		if err == nil || !strings.Contains(err.Error(), "backend cannot forward udp") {
			t.Fatalf("readPortForwardAck = %v, want the ack error", err)
		}
	})
}

// TestClientTokenFromEnv checks CORNUS_TOKEN is the default token source.
func TestClientTokenFromEnv(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]api.DeployStatus{})
	}))
	defer srv.Close()

	t.Setenv("CORNUS_TOKEN", "env-tok")
	if _, err := New(srv.URL).List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotAuth != "Bearer env-tok" {
		t.Fatalf("Authorization = %q, want Bearer env-tok", gotAuth)
	}
}
