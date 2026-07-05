package composecli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/compose"
	"cornus/pkg/obsstore"
)

// fakeObsServer answers the store's log endpoint with canned entries and records
// the query it was asked, so the tests can assert both the rendering and the
// translation from flags to a query.
func fakeObsServer(t *testing.T, entries []obsstore.LogEntry, seen *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.cornus/v1/obs/logs", func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = append(*seen, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	})
	mux.HandleFunc("/.cornus/v1/obs/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(obsstore.Status{Dir: "/tmp"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// noObsServer is a server WITHOUT the store: its routes are simply absent, which
// is exactly what a build lacking the imbh tag looks like on the wire.
func noObsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv
}

// obsDriver captures stdout and stderr separately, unlike the shared testDriver
// (which discards stdout). The store path routes records between the two, so a
// test that cannot see both cannot check the routing.
func obsDriver(out, errOut *bytes.Buffer) *cliout.Driver {
	return cliout.New(cliout.Options{Stdout: out, Stderr: errOut, Output: "plain"})
}

func obsRuntime(t *testing.T, url string, out, errOut *bytes.Buffer, services ...string) *runtime {
	t.Helper()
	docs := map[string]compose.ServiceDocument{}
	plans := map[string]compose.ServicePlan{}
	for _, s := range services {
		docs[s] = compose.ServiceDocument{Image: s + ":latest"}
		plans[s] = plan(s, s+":latest")
	}
	return &runtime{
		out:     obsDriver(out, errOut),
		project: compose.NewProject(&compose.ProjectDocument{Services: docs}).View(nil),
		plans:   plans,
		client:  client.New(url),
		baseDir: ".",
	}
}

func entry(at time.Time, body, stream string) obsstore.LogEntry {
	return obsstore.LogEntry{
		Time:       at,
		Body:       body,
		Attributes: `{"cornus.stream":"` + stream + `"}`,
	}
}

// TestStoreLogsRendersLikeTheRuntime is the property that makes `--from` an
// implementation detail rather than a second command: recorded output has to
// look exactly like live output, prefixes and all.
func TestStoreLogsRendersLikeTheRuntime(t *testing.T) {
	base := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	srv := fakeObsServer(t, []obsstore.LogEntry{
		entry(base, "listening on :8080", "stdout"),
		entry(base.Add(time.Second), "ready", "stdout"),
	}, nil)

	var out, errOut bytes.Buffer
	rt := obsRuntime(t, srv.URL, &out, &errOut, "web")
	c := &LogsCmd{From: "store"}

	if err := c.storeLogs(context.Background(), rt, []string{"web"}, api.LogOptions{}, true, rt.driver()); err != nil {
		t.Fatalf("storeLogs: %v", err)
	}
	got := out.String()
	for _, want := range []string{"web | listening on :8080", "web | ready"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
}

// TestStoreLogsMergesServicesByTime covers the multi-service read. Ordering by
// when things happened, rather than by which query returned first, is the whole
// reason the store path materializes before rendering.
func TestStoreLogsMergesServicesByTime(t *testing.T) {
	base := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	// Both services get the same canned reply; the timestamps decide the order.
	srv := fakeObsServer(t, []obsstore.LogEntry{
		entry(base.Add(2*time.Second), "later", "stdout"),
		entry(base, "earlier", "stdout"),
	}, nil)

	var out, errOut bytes.Buffer
	rt := obsRuntime(t, srv.URL, &out, &errOut, "web")
	c := &LogsCmd{From: "store"}
	if err := c.storeLogs(context.Background(), rt, []string{"web"}, api.LogOptions{}, false, rt.driver()); err != nil {
		t.Fatalf("storeLogs: %v", err)
	}
	got := out.String()
	if strings.Index(got, "earlier") > strings.Index(got, "later") {
		t.Errorf("records were not ordered by time; got:\n%s", got)
	}
}

// TestStoreLogsSplitsStreams keeps stderr out of a stdout pipeline.
func TestStoreLogsSplitsStreams(t *testing.T) {
	base := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	srv := fakeObsServer(t, []obsstore.LogEntry{
		entry(base, "normal", "stdout"),
		entry(base.Add(time.Second), "boom", "stderr"),
	}, nil)

	var out, errOut bytes.Buffer
	rt := obsRuntime(t, srv.URL, &out, &errOut, "web")
	c := &LogsCmd{From: "store"}
	if err := c.storeLogs(context.Background(), rt, []string{"web"}, api.LogOptions{}, false, rt.driver()); err != nil {
		t.Fatalf("storeLogs: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "normal") {
		t.Errorf("stdout record missing from stdout; got:\n%s", got)
	}
	if got := out.String(); strings.Contains(got, "boom") {
		t.Errorf("stderr record leaked onto stdout; got:\n%s", got)
	}
	if got := errOut.String(); !strings.Contains(got, "boom") {
		t.Errorf("stderr record missing from stderr; got:\n%s", got)
	}
}

// TestStoreLogsAsksNewestFirst pins the subtle one: `--tail N` must return the
// LAST n lines. Asking oldest-first with a limit would answer a different
// question and look almost right.
func TestStoreLogsAsksNewestFirst(t *testing.T) {
	var seen []string
	srv := fakeObsServer(t, nil, &seen)

	var out, errOut bytes.Buffer
	rt := obsRuntime(t, srv.URL, &out, &errOut, "web")
	c := &LogsCmd{From: "store"}
	if err := c.storeLogs(context.Background(), rt, []string{"web"}, api.LogOptions{Tail: "10"}, false, rt.driver()); err != nil {
		t.Fatalf("storeLogs: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("issued %d queries, want 1", len(seen))
	}
	if !strings.Contains(seen[0], "newest=1") {
		t.Errorf("query %q does not ask newest-first", seen[0])
	}
	if !strings.Contains(seen[0], "limit=10") {
		t.Errorf("query %q did not carry --tail as a limit", seen[0])
	}
}

// TestStoreLogsPassesFilters proves --match and --severity actually reach the
// server rather than being accepted and ignored.
func TestStoreLogsPassesFilters(t *testing.T) {
	var seen []string
	srv := fakeObsServer(t, nil, &seen)

	var out, errOut bytes.Buffer
	rt := obsRuntime(t, srv.URL, &out, &errOut, "web")
	c := &LogsCmd{From: "store", Match: "timeout", Severity: "error"}
	if err := c.storeLogs(context.Background(), rt, []string{"web"}, api.LogOptions{Since: "1h"}, false, rt.driver()); err != nil {
		t.Fatalf("storeLogs: %v", err)
	}
	q := seen[0]
	for _, want := range []string{"match=timeout", "severity=error", "since=1h"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
}

// TestStoreLogsNamesTheRemedy is the honesty requirement: a server without the
// store must produce a message a user can act on, not a bare 404.
func TestStoreLogsNamesTheRemedy(t *testing.T) {
	srv := noObsServer(t)
	var out, errOut bytes.Buffer
	rt := obsRuntime(t, srv.URL, &out, &errOut, "web")

	c := &LogsCmd{From: "store"}
	err := c.storeLogs(context.Background(), rt, []string{"web"}, api.LogOptions{}, false, rt.driver())
	if err == nil {
		t.Fatal("reading from an absent store succeeded")
	}
	msg := err.Error()
	for _, want := range []string{"--obs", "imbh"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name the remedy %q", msg, want)
		}
	}

	// Arriving implicitly via a filter flag should also say why.
	c = &LogsCmd{From: "auto", Match: "boom"}
	err = c.storeLogs(context.Background(), rt, []string{"web"}, api.LogOptions{}, false, rt.driver())
	if err == nil || !strings.Contains(err.Error(), "--match") {
		t.Errorf("filter-implied store error = %v; it should explain that --match pulled the user here", err)
	}
}

// TestLogsSourceDispatch covers the rules that decide where a request goes,
// including the two combinations that are contradictions rather than choices.
func TestLogsSourceDispatch(t *testing.T) {
	srv := noObsServer(t)
	var out, errOut bytes.Buffer
	rt := obsRuntime(t, srv.URL, &out, &errOut, "web")
	ctx := context.Background()

	t.Run("follow with store is refused", func(t *testing.T) {
		c := &LogsCmd{From: "store"}
		err := c.run(ctx, rt, []string{"web"}, api.LogOptions{Follow: true}, rt.driver())
		if err == nil || !strings.Contains(err.Error(), "--follow") {
			t.Errorf("err = %v, want a message about --follow", err)
		}
	})

	t.Run("match with runtime is refused", func(t *testing.T) {
		c := &LogsCmd{From: "runtime", Match: "boom"}
		err := c.run(ctx, rt, []string{"web"}, api.LogOptions{}, rt.driver())
		if err == nil || !strings.Contains(err.Error(), "--from=runtime") {
			t.Errorf("err = %v, want a message about the contradiction", err)
		}
	})

	t.Run("severity implies the store", func(t *testing.T) {
		c := &LogsCmd{From: "auto", Severity: "error"}
		err := c.run(ctx, rt, []string{"web"}, api.LogOptions{}, rt.driver())
		// The fake server has no store, so this must fail with the store's
		// remedy rather than silently falling back to the runtime and
		// returning unfiltered lines.
		if err == nil || !strings.Contains(err.Error(), "imbh") {
			t.Errorf("err = %v, want the store-unavailable remedy", err)
		}
	})
}

func TestLogsStoreOnly(t *testing.T) {
	cases := []struct {
		cmd  LogsCmd
		want bool
	}{
		{LogsCmd{}, false},
		{LogsCmd{Match: "x"}, true},
		{LogsCmd{Severity: "warn"}, true},
		{LogsCmd{Tail: "10", Since: "1h"}, false},
	}
	for _, c := range cases {
		if got := c.cmd.storeOnly(); got != c.want {
			t.Errorf("storeOnly(%+v) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestEntryStream(t *testing.T) {
	cases := []struct {
		name string
		in   obsstore.LogEntry
		want string
	}{
		{"stderr", obsstore.LogEntry{Attributes: `{"cornus.stream":"stderr"}`}, "stderr"},
		{"stdout", obsstore.LogEntry{Attributes: `{"cornus.stream":"stdout"}`}, "stdout"},
		{"no attributes", obsstore.LogEntry{}, "stdout"},
		{"unparseable attributes", obsstore.LogEntry{Attributes: "{not json"}, "stdout"},
		// A record from the app's own OTLP exporter carries no stream tag. It is
		// not an error, so it must not land on stderr and corrupt a pipeline.
		{"sdk-exported record", obsstore.LogEntry{Attributes: `{"http.method":"GET"}`}, "stdout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := entryStream(c.in); got != c.want {
				t.Errorf("entryStream = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStoreTailLimit(t *testing.T) {
	cases := map[string]int{
		"":     0,
		"all":  0,
		"10":   10,
		"0":    0,
		"-3":   0,
		"lots": 0,
	}
	for in, want := range cases {
		if got := storeTailLimit(in); got != want {
			t.Errorf("storeTailLimit(%q) = %d, want %d", in, got, want)
		}
	}
}
