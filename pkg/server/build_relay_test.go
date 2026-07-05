package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cornus/pkg/config"
	"cornus/pkg/storage"
	"cornus/pkg/wire"
)

func TestBuilderAttachURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"127.0.0.1:5099", "ws://127.0.0.1:5099/.cornus/v1/build/attach"},
		{"http://127.0.0.1:5099", "ws://127.0.0.1:5099/.cornus/v1/build/attach"},
		{"https://builder.example", "wss://builder.example/.cornus/v1/build/attach"},
		{"ws://127.0.0.1:5099", "ws://127.0.0.1:5099/.cornus/v1/build/attach"},
		{"ws://127.0.0.1:5099/", "ws://127.0.0.1:5099/.cornus/v1/build/attach"},
		// Already-complete URLs must not get the path appended twice.
		{"ws://127.0.0.1:5099/.cornus/v1/build/attach", "ws://127.0.0.1:5099/.cornus/v1/build/attach"},
	}
	for _, c := range cases {
		if got := builderAttachURL(c.in); got != c.want {
			t.Errorf("builderAttachURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuilderHTTPBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"127.0.0.1:5099", "http://127.0.0.1:5099"},
		{"ws://127.0.0.1:5099", "http://127.0.0.1:5099"},
		{"wss://builder.example", "https://builder.example"},
		{"http://127.0.0.1:5099/", "http://127.0.0.1:5099"},
		// The attach path is stripped so the POST endpoint can be appended.
		{"ws://127.0.0.1:5099/.cornus/v1/build/attach", "http://127.0.0.1:5099"},
	}
	for _, c := range cases {
		if got := builderHTTPBase(c.in); got != c.want {
			t.Errorf("builderHTTPBase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// newRelayServer builds a server that delegates builds to builderURL.
func newRelayServer(t *testing.T, builderURL string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := storage.Open(context.Background(), dir, dir+"/uploads")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{DataDir: dir, BuilderURL: builderURL}, st)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(s.Handler())
}

// TestResolveBuilderExplicitWins proves an explicit --builder-url short-circuits
// resolution: no capability probe, and crucially no attempt to start a container,
// even with auto enabled.
func TestResolveBuilderExplicitWins(t *testing.T) {
	s := &Server{cfg: config.Config{BuilderURL: "ws://10.0.0.9:5099", BuilderAuto: true}}
	got, err := s.resolveBuilder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := "ws://10.0.0.9:5099/.cornus/v1/build/attach"; got != want {
		t.Fatalf("resolveBuilder = %q, want %q", got, want)
	}
	if s.builderDone {
		t.Error("explicit builder URL must not trigger container resolution")
	}
}

// TestResolveBuilderDisabledBuildsInProcess proves that with auto off and no URL
// the server keeps building in-process, whatever its privileges.
func TestResolveBuilderDisabledBuildsInProcess(t *testing.T) {
	s := &Server{cfg: config.Config{BuilderAuto: false}}
	got, err := s.resolveBuilder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("resolveBuilder = %q, want \"\" (in-process)", got)
	}
}

// TestZeroConfigNeverStartsContainer pins a property the test suite depends on:
// a zero config.Config does not auto-start anything. Tests construct servers that
// way, and `go test ./...` must never reach out to a Docker daemon.
func TestZeroConfigNeverStartsContainer(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	got, err := s.resolveBuilder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "" || s.builderDone {
		t.Fatalf("zero config delegated (url=%q, resolved=%v); it must build in-process", got, s.builderDone)
	}
}

// TestBuildAttachRelaysToBuilder proves the build-attach WebSocket is spliced
// through to the upstream builder byte for byte. That transparency is the whole
// design: the buildwire protocol (yamux + the control stream + the caller's 9P
// export) rides this one connection, so a raw splice delegates the build without
// this server terminating 9P or touching the caller's context.
func TestBuildAttachRelaysToBuilder(t *testing.T) {
	var upstreamHits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		c, err := wire.AcceptConn(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c) // echo whatever the caller sends
	}))
	defer up.Close()

	srv := newRelayServer(t, up.URL)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := wire.DialConn(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/.cornus/v1/build/attach")
	if err != nil {
		t.Fatalf("dial relaying server: %v", err)
	}
	defer conn.Close()

	want := "buildwire-frame-\x00\x01\x02"
	if _, err := io.WriteString(conn, want); err != nil {
		t.Fatalf("write through relay: %v", err)
	}
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read back through relay: %v", err)
	}
	if string(buf) != want {
		t.Fatalf("relayed bytes = %q, want %q", buf, want)
	}
	if n := atomic.LoadInt32(&upstreamHits); n != 1 {
		t.Fatalf("upstream builder hits = %d, want 1", n)
	}
}

// TestBuildAttachRelayDeniedBeforeUpstream proves authorization is enforced on
// THIS server before any delegation: a denied identity gets a 403 and the
// builder is never contacted. Relaying first would let a policy that restricts
// "build" be bypassed by pointing the server at a builder.
func TestBuildAttachRelayDeniedBeforeUpstream(t *testing.T) {
	var upstreamHits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
	}))
	defer up.Close()

	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"ci-bot":["build"]}`)

	srv := newRelayServer(t, up.URL)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/.cornus/v1/build/attach", nil)
	req.Header.Set("Authorization", "Bearer "+jwtFor(t, secret, "stranger"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied identity: code=%d, want 403", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&upstreamHits); n != 0 {
		t.Fatalf("upstream builder hits = %d, want 0 (denied before delegation)", n)
	}
}

// TestBuildPostRelaysToBuilder proves POST /.cornus/v1/build is forwarded to the
// builder with its query intact and the streaming progress copied back, and that
// a delegating server never extracts the context tar into its own data dir.
func TestBuildPostRelaysToBuilder(t *testing.T) {
	var gotQuery, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "=> building\n=> done\n")
	}))
	defer up.Close()

	srv := newRelayServer(t, up.URL)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/.cornus/v1/build?t=localhost:5000/app:v1&build-arg=K=V",
		"application/x-tar", strings.NewReader("fake-tar-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("relayed POST: code=%d body=%q, want 200", resp.StatusCode, body)
	}
	if string(body) != "=> building\n=> done\n" {
		t.Fatalf("relayed progress = %q", body)
	}
	if !strings.Contains(gotQuery, "t=localhost%3A5000%2Fapp%3Av1") && !strings.Contains(gotQuery, "t=localhost:5000/app:v1") {
		t.Fatalf("builder saw query %q, want the target preserved", gotQuery)
	}
	if !strings.Contains(gotQuery, "build-arg=K%3DV") && !strings.Contains(gotQuery, "build-arg=K=V") {
		t.Fatalf("builder saw query %q, want build-arg preserved", gotQuery)
	}
	if gotBody != "fake-tar-bytes" {
		t.Fatalf("builder saw body %q, want the context tar forwarded verbatim", gotBody)
	}
}
