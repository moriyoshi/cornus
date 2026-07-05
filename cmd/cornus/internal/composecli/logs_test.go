package composecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/compose"
)

// fakeKubeLogs is a test kubeLogOpener. It records the resources it was asked for
// and returns canned output (open) or a canned setup error (openErr, forcing the
// caller to fall back to the server proxy).
type fakeKubeLogs struct {
	out     map[string]string
	openErr error
	opened  []string
}

func (f *fakeKubeLogs) Open(_ context.Context, resource string, _ api.LogOptions) (io.ReadCloser, error) {
	f.opened = append(f.opened, resource)
	if f.openErr != nil {
		return nil, f.openErr
	}
	return io.NopCloser(strings.NewReader(f.out[resource])), nil
}

// logsTestServer serves /.cornus/v1/deploy/{resource}/logs, writing the canned stdout
// and stderr for that resource as a Docker stdcopy-multiplexed stream (matching
// the real server's application/vnd.docker.raw-stream body).
func logsTestServer(t *testing.T, stdoutByRes, stderrByRes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/deploy/", func(w http.ResponseWriter, r *http.Request) {
		res := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/"), "/logs")
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		if s := stdoutByRes[res]; s != "" {
			sw := stdcopy.NewStdWriter(w, stdcopy.Stdout)
			sw.Write([]byte(s))
		}
		if s := stderrByRes[res]; s != "" {
			sw := stdcopy.NewStdWriter(w, stdcopy.Stderr)
			sw.Write([]byte(s))
		}
	})
	return httptest.NewServer(mux)
}

func runtimeForLogs(t *testing.T, base string, resources map[string]string) *runtime {
	t.Helper()
	plans := map[string]compose.ServicePlan{}
	for svc, res := range resources {
		plans[svc] = compose.ServicePlan{Service: svc, Resource: res}
	}
	return &runtime{plans: plans, client: client.New(base)}
}

func TestStreamLogsPrefixed(t *testing.T) {
	srv := logsTestServer(t,
		map[string]string{"proj-web": "web line 1\nweb line 2\n"},
		map[string]string{"proj-db": "db err\n"},
	)
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web", "db": "proj-db"})

	var out, errBuf bytes.Buffer
	err := rt.streamLogs(context.Background(), []string{"web", "db"}, api.LogOptions{}, true, &out, &errBuf)
	if err != nil {
		t.Fatalf("streamLogs: %v", err)
	}

	// Names are padded to the widest ("web") and each line is tagged. Services
	// stream concurrently, so assert on membership rather than ordering.
	gotOut := out.String()
	for _, want := range []string{"web | web line 1\n", "web | web line 2\n"} {
		if !strings.Contains(gotOut, want) {
			t.Fatalf("stdout %q missing %q", gotOut, want)
		}
	}
	if want := "db  | db err\n"; !strings.Contains(errBuf.String(), want) {
		t.Fatalf("stderr %q missing %q", errBuf.String(), want)
	}
}

func TestStreamLogsNoPrefix(t *testing.T) {
	srv := logsTestServer(t, map[string]string{"proj-web": "plain line\n"}, nil)
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})

	var out, errBuf bytes.Buffer
	if err := rt.streamLogs(context.Background(), []string{"web"}, api.LogOptions{}, false, &out, &errBuf); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	if got := out.String(); got != "plain line\n" {
		t.Fatalf("stdout = %q, want unprefixed %q", got, "plain line\n")
	}
}

// TestStreamLogsKubeDirect confirms that when a cluster profile supplies a
// kubeLogOpener, logs are streamed directly from the cluster and the server proxy
// is never contacted.
func TestStreamLogsKubeDirect(t *testing.T) {
	proxyHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHit = true
		t.Errorf("server proxy should not be contacted: %s", r.URL.Path)
	}))
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})
	kube := &fakeKubeLogs{out: map[string]string{"proj-web": "kube web line\n"}}
	rt.kubeLogs = kube

	var out, errBuf bytes.Buffer
	if err := rt.streamLogs(context.Background(), []string{"web"}, api.LogOptions{}, true, &out, &errBuf); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	if proxyHit {
		t.Fatal("server proxy was contacted despite a working direct path")
	}
	if want := "web | kube web line\n"; !strings.Contains(out.String(), want) {
		t.Fatalf("stdout %q missing %q", out.String(), want)
	}
	if len(kube.opened) != 1 || kube.opened[0] != "proj-web" {
		t.Fatalf("kube opened = %v, want [proj-web]", kube.opened)
	}
}

// TestStreamLogsKubeFallback confirms that when the direct path fails to start
// (setup error, no bytes written), logs fall back to the server proxy last resort.
func TestStreamLogsKubeFallback(t *testing.T) {
	srv := logsTestServer(t, map[string]string{"proj-web": "proxy line\n"}, nil)
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})
	kube := &fakeKubeLogs{openErr: errors.New("forbidden: no RBAC")}
	rt.kubeLogs = kube

	var out, errBuf bytes.Buffer
	if err := rt.streamLogs(context.Background(), []string{"web"}, api.LogOptions{}, true, &out, &errBuf); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	if len(kube.opened) != 1 {
		t.Fatalf("direct path should have been attempted once, got %v", kube.opened)
	}
	if want := "web | proxy line\n"; !strings.Contains(out.String(), want) {
		t.Fatalf("stdout %q missing fallback output %q", out.String(), want)
	}
}

// TestStreamLogsKubeAndProxyBothFail confirms that when the direct kube read and
// the server proxy both fail, the returned error names BOTH attempts — so a
// cluster user is not shown only a puzzling server-side error that hides the fact
// that their kubeconfig path was tried first.
func TestStreamLogsKubeAndProxyBothFail(t *testing.T) {
	// A proxy server that always 500s, so the fallback also fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})
	rt.kubeLogs = &fakeKubeLogs{openErr: errors.New("no pods for deployment")}

	var out, errBuf bytes.Buffer
	err := rt.streamLogs(context.Background(), []string{"web"}, api.LogOptions{}, true, &out, &errBuf)
	if err == nil {
		t.Fatal("expected an error when both the direct and proxy paths fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no pods for deployment") || !strings.Contains(msg, "direct pod-log read failed") {
		t.Fatalf("error should mention the direct attempt; got %q", msg)
	}
}

// followTestServer serves /.cornus/v1/deploy/{resource}/logs like a --follow stream: it
// writes an initial line, flushes it, then blocks until the request context is
// cancelled (the client closing the connection on ctx cancel), mimicking a real
// log follow that never ends on its own. This is the shape foreground `up`
// attaches to, and that its hold loop must be able to stop via context cancel.
func followTestServer(t *testing.T, line string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/deploy/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		sw := stdcopy.NewStdWriter(w, stdcopy.Stdout)
		sw.Write([]byte(line))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // block like a live follow until the client disconnects
	})
	return httptest.NewServer(mux)
}

// syncBuffer is a bytes.Buffer guarded by a mutex so a test can poll Len()/String()
// from one goroutine while streamLogs writes to it from another.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStreamLogsFollowStopsOnCancel confirms a following log stream (as foreground
// `cornus compose up` attaches to) drains its output and returns cleanly — not as
// an error — when its context is cancelled, so the up hold loop can stop the
// attach goroutine on Ctrl-C / external `down`.
func TestStreamLogsFollowStopsOnCancel(t *testing.T) {
	srv := followTestServer(t, "web up\n")
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})

	ctx, cancel := context.WithCancel(context.Background())
	// out is polled below while streamLogs's writer goroutine writes to it, so it must
	// be a mutex-guarded buffer (streamLogs's own prefixWriter mutex is unreachable
	// here). errBuf has a single writer and no concurrent reader, so a plain buffer is
	// fine.
	var out syncBuffer
	var errBuf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- rt.streamLogs(ctx, []string{"web"}, api.LogOptions{Follow: true, Tail: "all"}, true, &out, &errBuf)
	}()

	// Wait for the first (flushed) line to arrive, then cancel as the hold loop would.
	deadline := time.After(2 * time.Second)
	for out.Len() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the initial follow line")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("streamLogs on cancelled follow = %v, want clean nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streamLogs did not return after context cancel")
	}
	if got := out.String(); got != "web | web up\n" {
		t.Fatalf("stdout = %q, want the flushed follow line", got)
	}
}

// TestStreamLogsFlushesPartialLine confirms a final log entry without a trailing
// newline is still emitted (flushed) rather than left buffered.
func TestStreamLogsFlushesPartialLine(t *testing.T) {
	srv := logsTestServer(t, map[string]string{"proj-web": "no newline"}, nil)
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})

	var out, errBuf bytes.Buffer
	if err := rt.streamLogs(context.Background(), []string{"web"}, api.LogOptions{}, true, &out, &errBuf); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	if got := out.String(); got != "web | no newline" {
		t.Fatalf("stdout = %q, want flushed partial line", got)
	}
}

// seenInstances records the `instance` query parameter of every log request the
// fixture served. --all-replicas streams every replica concurrently, so the
// handler runs on several goroutines at once and the recorder must be locked.
type seenInstances struct {
	mu   sync.Mutex
	vals []string
}

func (s *seenInstances) add(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vals = append(s.vals, v)
}

// snapshot returns a copy, so callers can sort or compare it without holding the
// lock (all streams have finished by the time a test inspects it, but copying
// keeps that from being load-bearing).
func (s *seenInstances) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.vals...)
}

// replicaLogsServer serves both the status endpoint (so replicaCount can report
// how many instances a resource has) and the log stream, echoing back which
// instance was requested. It is the fixture for `logs --index`, whose whole job
// is to turn docker's 1-based index into the 0-based ordinal the backend
// addresses replicas by.
func replicaLogsServer(t *testing.T, instances int, seen *seenInstances) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/deploy/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/.cornus/v1/deploy/")
		if !strings.HasSuffix(path, "/logs") {
			st := api.DeployStatus{Name: path}
			for i := 0; i < instances; i++ {
				st.Instances = append(st.Instances, api.InstanceStatus{ID: fmt.Sprintf("%s-%d", path, i), State: "running", Running: true})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(st)
			return
		}
		inst := r.URL.Query().Get("instance")
		seen.add(inst)
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		sw := stdcopy.NewStdWriter(w, stdcopy.Stdout)
		fmt.Fprintf(sw, "from instance %q\n", inst)
	})
	return httptest.NewServer(mux)
}

// A single-target stream carries the caller's chosen ordinal through to the
// backend instead of hardcoding the first instance.
func TestStreamLogsHonorsInstance(t *testing.T) {
	var seen seenInstances
	srv := replicaLogsServer(t, 3, &seen)
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})

	var out, errBuf bytes.Buffer
	if err := rt.streamLogs(context.Background(), []string{"web"}, api.LogOptions{Instance: 2}, false, &out, &errBuf); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	if got := seen.snapshot(); len(got) != 1 || got[0] != "2" {
		t.Fatalf("instance query = %v, want exactly one request for instance 2", got)
	}
	// The prefix stays untagged: the user named the replica, so labelling every
	// line with it adds nothing (unlike --all-replicas, which must disambiguate).
	if got := out.String(); got != "from instance \"2\"\n" {
		t.Fatalf("stdout = %q, want the selected replica's output unprefixed", got)
	}
}

// The default (no --index, no --all-replicas) is unchanged: instance 0, which
// the client omits from the request entirely.
func TestStreamLogsDefaultsToFirstInstance(t *testing.T) {
	var seen seenInstances
	srv := replicaLogsServer(t, 3, &seen)
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})

	var out, errBuf bytes.Buffer
	if err := rt.streamLogs(context.Background(), []string{"web"}, api.LogOptions{}, false, &out, &errBuf); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	if got := seen.snapshot(); len(got) != 1 || got[0] != "" {
		t.Fatalf("instance query = %v, want one request with no instance parameter", got)
	}
}

// --all-replicas still fans out over every instance and tags each line.
func TestStreamLogsAllReplicasFansOut(t *testing.T) {
	var seen seenInstances
	srv := replicaLogsServer(t, 2, &seen)
	defer srv.Close()
	rt := runtimeForLogs(t, srv.URL, map[string]string{"web": "proj-web"})

	var out, errBuf bytes.Buffer
	if err := rt.streamLogsReplicas(context.Background(), []string{"web"}, api.LogOptions{}, true, true, &out, &errBuf); err != nil {
		t.Fatalf("streamLogsReplicas: %v", err)
	}
	queries := seen.snapshot()
	sort.Strings(queries)
	if len(queries) != 2 || queries[0] != "" || queries[1] != "1" {
		t.Fatalf("instance queries = %v, want instances 0 (omitted) and 1", queries)
	}
	if got := out.String(); !strings.Contains(got, "web[0]") || !strings.Contains(got, "web[1]") {
		t.Fatalf("stdout = %q, want per-replica ordinal tags", got)
	}
}

func TestLogsValidateIndex(t *testing.T) {
	cases := []struct {
		name string
		cmd  LogsCmd
		want string
	}{
		{"unset", LogsCmd{}, ""},
		{"valid", LogsCmd{Index: 1}, ""},
		{"negative", LogsCmd{Index: -1}, "1-based"},
		{"with all-replicas", LogsCmd{Index: 2, AllReplicas: true}, "pass only one"},
		{"with match", LogsCmd{Index: 2, Match: "boom"}, "not per-replica"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cmd.validateIndex()
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("validateIndex() = %v, want nil", err)
			case c.want != "" && err == nil:
				t.Fatalf("validateIndex() = nil, want an error mentioning %q", c.want)
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Fatalf("validateIndex() = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// --index cannot be answered from the observability store, which records a
// service rather than an instance. The contradiction is named, not silently
// answered with every replica's recorded output.
func TestLogsIndexRefusesStoreSource(t *testing.T) {
	rt := runtimeForLogs(t, "http://127.0.0.1:1", map[string]string{"web": "proj-web"})
	c := &LogsCmd{Index: 2, From: string(sourceStore)}
	err := c.run(context.Background(), rt, []string{"web"}, api.LogOptions{Instance: 1}, testDriver(&bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "--from=store") {
		t.Fatalf("run() = %v, want a refusal naming --from=store", err)
	}
}
