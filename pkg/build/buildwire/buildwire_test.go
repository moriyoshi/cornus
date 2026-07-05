package buildwire

import (
	"context"
	"io"
	gofs "io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tonistiigi/fsutil"

	"cornus/pkg/build/buildprog"
	"cornus/pkg/wire"
)

// testServer runs fn for each attached build session over an httptest WebSocket.
func testServer(t *testing.T, fn func(s *ServerSession)) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/attach", func(w http.ResponseWriter, r *http.Request) {
		s, err := Attach(w, r)
		if err != nil {
			t.Errorf("Attach: %v", err)
			return
		}
		defer s.Close()
		fn(s)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/attach"
}

// readFS reads an fsutil.FS into a path->content map.
func readFS(t *testing.T, fsys fsutil.FS) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fsys.Walk(context.Background(), "", func(p string, d gofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rc, err := fsys.Open(p)
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

func TestRemoteFilesAndSecretsOverWire(t *testing.T) {
	// Caller-side directories and secret.
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "a.txt", "hello")
	mustWrite(t, ctxDir, "sub/b.txt", "world")
	dataDir := t.TempDir()
	mustWrite(t, dataDir, "c.txt", "named-bind")

	url := testServer(t, func(s *ServerSession) {
		pw := s.Progress()
		for k, v := range readFS(t, s.Mounts()["context"]) {
			pw.Log("context:%s=%s\n", k, v)
		}
		for k, v := range readFS(t, s.Mounts()["data"]) {
			pw.Log("data:%s=%s\n", k, v)
		}
		secret, err := s.Secrets().GetSecret(context.Background(), "tok")
		if err != nil {
			pw.Log("secret-error:%v\n", err)
		} else {
			pw.Log("secret:tok=%s\n", secret)
		}
		_ = s.Done(&Result{ImageDigest: "sha256:test"}, nil)
	})

	var progress strings.Builder
	res, err := Serve(context.Background(), url, BuildSpec{
		Target:         "localhost:5000/app:v1",
		DockerfileName: "Dockerfile",
		NamedContexts:  []string{"data"},
		SecretIDs:      []string{"tok"},
	}, ServeOpts{
		ContextDir:    ctxDir,
		DockerfileDir: ctxDir,
		NamedContexts: map[string]string{"data": dataDir},
		Secrets:       map[string][]byte{"tok": []byte("s3cr3t")},
	}, buildprog.NewSink(&progress, buildprog.Plain, false), nil, wire.ClientTransport{})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if res.ImageDigest != "sha256:test" {
		t.Fatalf("result = %+v", res)
	}

	got := progress.String()
	for _, want := range []string{
		"context:a.txt=hello",
		"context:sub/b.txt=world",
		"data:c.txt=named-bind",
		"secret:tok=s3cr3t",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress missing %q; got:\n%s", want, got)
		}
	}
}

func TestSecretNotFound(t *testing.T) {
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "x", "y")
	url := testServer(t, func(s *ServerSession) {
		pw := s.Progress()
		_, err := s.Secrets().GetSecret(context.Background(), "missing")
		pw.Log("err=%v\n", err)
		_ = s.Done(&Result{}, nil)
	})
	var progress strings.Builder
	_, err := Serve(context.Background(), url, BuildSpec{SecretIDs: []string{"present"}},
		ServeOpts{ContextDir: ctxDir, DockerfileDir: ctxDir, Secrets: map[string][]byte{"present": []byte("v")}},
		buildprog.NewSink(&progress, buildprog.Plain, false), nil, wire.ClientTransport{})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !strings.Contains(progress.String(), "not found") {
		t.Fatalf("expected not-found error, got: %s", progress.String())
	}
}

// TestDoneFrameFlushedBeforeClose is a regression test for the batched-pipelined
// send race: the server writes the final Done frame and then Close() tears down
// the session. On the async send path the Done frame is only queued when send()
// returns, so closing the session immediately used to race the flush and the
// caller read EOF ("buildwire: control stream: EOF") even though the build
// succeeded server-side. The server here sends no progress events, so Close()
// fires right on the heels of Done() — the tightest window. Many iterations catch
// the race probabilistically; with the fix (control stream half-closed before the
// session) every iteration must deliver the result.
func TestDoneFrameFlushedBeforeClose(t *testing.T) {
	ctxDir := t.TempDir()
	mustWrite(t, ctxDir, "x", "y")
	url := testServer(t, func(s *ServerSession) {
		// Queue a burst of progress frames right before Done so the send loop is
		// guaranteed to be behind (frames still in the scheduler) when the harness
		// runs defer s.Close() the instant this returns. Without the fix, Session
		// Close() closes the underlying conn without draining, truncating the tail
		// — including the Done frame — so the caller reads EOF. With the fix, the
		// control stream is half-closed first, flushing the whole FIFO.
		pw := s.Progress()
		payload := strings.Repeat("x", 4096)
		for j := 0; j < 64; j++ {
			pw.Log("progress %d %s\n", j, payload)
		}
		_ = s.Done(&Result{ImageDigest: "sha256:flushed"}, nil)
	})
	for i := 0; i < 50; i++ {
		res, err := Serve(context.Background(), url, BuildSpec{},
			ServeOpts{ContextDir: ctxDir, DockerfileDir: ctxDir},
			buildprog.NewSink(io.Discard, buildprog.Plain, false), nil, wire.ClientTransport{})
		if err != nil {
			t.Fatalf("iteration %d: Serve: %v", i, err)
		}
		if res.ImageDigest != "sha256:flushed" {
			t.Fatalf("iteration %d: result = %+v", i, res)
		}
	}
}

func mustWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var _ = sort.Strings
