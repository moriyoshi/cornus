package composecli

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/build/buildprog"
	"cornus/pkg/client"
	"cornus/pkg/compose"
)

// scriptedBuildExecutor records every Build call and can fail a chosen tag,
// standing in for *client.Client in runBuildGroups tests.
type scriptedBuildExecutor struct {
	mu      sync.Mutex
	calls   []client.BuildRequest
	failTag string
}

func (s *scriptedBuildExecutor) Build(_ context.Context, req client.BuildRequest, progress buildprog.Sink) error {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	progress.Call(buildprog.Event{Vertex: "step", Status: "done"})
	if req.Tag == s.failTag {
		return errors.New("boom")
	}
	return nil
}

func TestBuildRequestKeyIgnoresTagAndTags(t *testing.T) {
	a := client.BuildRequest{ContextDir: "/ctx", Dockerfile: "Dockerfile", Tag: "host/a:latest"}
	b := client.BuildRequest{ContextDir: "/ctx", Dockerfile: "Dockerfile", Tag: "host/b:latest", Tags: []string{"host/b:extra"}}
	ka, err := buildRequestKey(a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := buildRequestKey(b)
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatalf("expected identical keys ignoring Tag/Tags, got %q vs %q", ka, kb)
	}
}

func TestBuildRequestKeyDistinguishesConfig(t *testing.T) {
	base := client.BuildRequest{ContextDir: "/ctx", Dockerfile: "Dockerfile", Tag: "host/a:latest"}
	cases := []client.BuildRequest{
		{ContextDir: "/other", Dockerfile: "Dockerfile", Tag: "host/a:latest"},
		{ContextDir: "/ctx", Dockerfile: "other.Dockerfile", Tag: "host/a:latest"},
		{ContextDir: "/ctx", Dockerfile: "Dockerfile", Tag: "host/a:latest", Args: map[string]string{"X": "1"}},
		{ContextDir: "/ctx", Dockerfile: "Dockerfile", Tag: "host/a:latest", NoCache: true},
		{ContextDir: "/ctx", Dockerfile: "Dockerfile", Tag: "host/a:latest", Target: "builder"},
	}
	kbase, err := buildRequestKey(base)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		k, err := buildRequestKey(c)
		if err != nil {
			t.Fatal(err)
		}
		if k == kbase {
			t.Errorf("case %d: expected a distinct key from the base request, got the same", i)
		}
	}
}

func TestGroupBuildRequestsDedupesIdenticalBuilds(t *testing.T) {
	// web and worker share a build (same context/dockerfile); api is distinct.
	reqs := map[string]client.BuildRequest{
		"web":    {ContextDir: "/app", Dockerfile: "Dockerfile", Tag: "host/proj-web:latest"},
		"worker": {ContextDir: "/app", Dockerfile: "Dockerfile", Tag: "host/proj-worker:latest"},
		"api":    {ContextDir: "/api", Dockerfile: "Dockerfile", Tag: "host/proj-api:latest"},
	}
	groups, err := groupBuildRequests([]string{"web", "worker", "api"}, reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 distinct build groups, got %d", len(groups))
	}
	shared := groups[0]
	if len(shared.members) != 2 {
		t.Fatalf("expected the web/worker group to have 2 members, got %v", shared.members)
	}
	sort.Strings(shared.members)
	if shared.members[0] != "web" || shared.members[1] != "worker" {
		t.Fatalf("unexpected group members: %v", shared.members)
	}
	if shared.req.Tag != "host/proj-web:latest" {
		t.Fatalf("expected the primary (first-seen) tag as Tag, got %q", shared.req.Tag)
	}
	if len(shared.req.Tags) != 1 || shared.req.Tags[0] != "host/proj-worker:latest" {
		t.Fatalf("expected worker's tag folded into Tags, got %v", shared.req.Tags)
	}
	if shared.memberTags["web"] != "host/proj-web:latest" || shared.memberTags["worker"] != "host/proj-worker:latest" {
		t.Fatalf("unexpected memberTags: %v", shared.memberTags)
	}

	solo := groups[1]
	if len(solo.members) != 1 || solo.members[0] != "api" {
		t.Fatalf("expected api to be its own group, got %v", solo.members)
	}
}

func TestRunBuildGroupsOneCallPerGroup(t *testing.T) {
	reqs := map[string]client.BuildRequest{
		"web":    {ContextDir: "/app", Dockerfile: "Dockerfile", Tag: "host/proj-web:latest"},
		"worker": {ContextDir: "/app", Dockerfile: "Dockerfile", Tag: "host/proj-worker:latest"},
		"api":    {ContextDir: "/api", Dockerfile: "Dockerfile", Tag: "host/proj-api:latest"},
	}
	groups, err := groupBuildRequests([]string{"web", "worker", "api"}, reqs)
	if err != nil {
		t.Fatal(err)
	}
	exec := &scriptedBuildExecutor{}
	tags, err := runBuildGroups(context.Background(), exec, groups, testDriver(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("expected exactly one Build call per distinct group (2), got %d", len(exec.calls))
	}
	want := map[string]string{
		"web":    "host/proj-web:latest",
		"worker": "host/proj-worker:latest",
		"api":    "host/proj-api:latest",
	}
	for name, tag := range want {
		if tags[name] != tag {
			t.Errorf("service %s: got tag %q, want %q", name, tags[name], tag)
		}
	}
}

func TestRunBuildGroupsPropagatesError(t *testing.T) {
	reqs := map[string]client.BuildRequest{
		"web": {ContextDir: "/app", Dockerfile: "Dockerfile", Tag: "host/proj-web:latest"},
	}
	groups, err := groupBuildRequests([]string{"web"}, reqs)
	if err != nil {
		t.Fatal(err)
	}
	exec := &scriptedBuildExecutor{failTag: "host/proj-web:latest"}
	if _, err := runBuildGroups(context.Background(), exec, groups, testDriver(&bytes.Buffer{})); err == nil {
		t.Fatal("expected an error from a failing build group")
	}
}

func TestBuildConcurrencyJSONModeIsSequential(t *testing.T) {
	if n := buildConcurrency(5, cliout.ModeJSON); n != 1 {
		t.Fatalf("json mode: got concurrency %d, want 1", n)
	}
}

func TestBuildConcurrencyClampedToGroupCount(t *testing.T) {
	if n := buildConcurrency(1, cliout.ModePlain); n != 1 {
		t.Fatalf("1 group: got concurrency %d, want 1", n)
	}
	if n := buildConcurrency(0, cliout.ModePlain); n < 1 {
		t.Fatalf("0 groups: got concurrency %d, want >= 1", n)
	}
}

// buildRuntime is a runtime with the registry host already memoized, so
// resolveBuildRequest resolves tags without contacting a server.
func buildRuntime() *runtime {
	return &runtime{baseDir: "/proj", registryHostMemo: "reg.example:5000"}
}

// --pull is OR'd with the compose build.pull field, exactly like --no-cache with
// build.no_cache: the flag can turn base-image re-resolution on for a build that
// did not ask for it, and can never turn it off for one that did.
func TestResolveBuildRequestPullOverride(t *testing.T) {
	cases := []struct {
		name     string
		filePull bool
		flagPull bool
		want     bool
	}{
		{"neither", false, false, false},
		{"flag only", false, true, true},
		{"file only", true, false, true},
		{"both", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := compose.ServicePlan{
				Service:  "web",
				Resource: "proj-web",
				Build:    &compose.BuildPlan{Context: ".", Pull: c.filePull},
			}
			req, err := buildRuntime().resolveBuildRequest(context.Background(), plan, nil, buildOverrides{pull: c.flagPull})
			if err != nil {
				t.Fatalf("resolveBuildRequest: %v", err)
			}
			if req.Pull != c.want {
				t.Fatalf("Pull = %v, want %v", req.Pull, c.want)
			}
		})
	}
}

// --quiet is an invocation-level display choice, not part of the build's
// identity: two builds that differ only in --quiet must still deduplicate.
func TestBuildQuietDoesNotChangeTheRequest(t *testing.T) {
	plan := compose.ServicePlan{Service: "web", Resource: "proj-web", Build: &compose.BuildPlan{Context: "."}}
	loud, err := buildRuntime().resolveBuildRequest(context.Background(), plan, nil, buildOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := buildRuntime().resolveBuildRequest(context.Background(), plan, nil, buildOverrides{quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	kl, err := buildRequestKey(loud)
	if err != nil {
		t.Fatal(err)
	}
	kq, err := buildRequestKey(quiet)
	if err != nil {
		t.Fatal(err)
	}
	if kl != kq {
		t.Fatal("--quiet changed the build request; it must only affect rendering")
	}
}

func TestReportDriverQuietDiscardsProgress(t *testing.T) {
	var buf bytes.Buffer
	d := testDriver(&buf)

	if got := reportDriver(d, false); got != d {
		t.Fatal("without --quiet the caller's driver must be used unchanged")
	}
	quiet := reportDriver(d, true)
	if quiet == d {
		t.Fatal("--quiet must not render through the caller's driver")
	}
	quiet.Info("building web")
	quiet.Step("=> RUN go build")
	if buf.Len() != 0 {
		t.Fatalf("--quiet still wrote %q", buf.String())
	}
	// The real driver is untouched by the swap.
	d.Info("visible")
	if !strings.Contains(buf.String(), "visible") {
		t.Fatalf("the original driver stopped working: %q", buf.String())
	}
}
