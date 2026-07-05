package webbff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/api"
	"cornus/pkg/client"
)

// ---- scaffolding -------------------------------------------------------------

// shellsServer builds a BFF over a compose project whose single service `web` is
// spelled by the caller, so a test can vary entrypoint / command / x-cornus-shells.
// configYAML, when non-empty, becomes the CLIENT config the resolver reads.
//
// The config path is always pinned to a temp file, even when empty: a Resolver with
// no ConfigFile falls back to the platform default, so without this every
// assertion about the candidate ORDER would depend on whatever the developer
// running the tests happens to have in ~/.config/cornus/config.yaml.
func shellsServer(t *testing.T, serviceYAML, configYAML string) *Server {
	t.Helper()
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-web", Instances: []api.InstanceStatus{{Running: true}}},
		{Name: "stray", Instances: []api.InstanceStatus{{Running: true}}},
	}, nil)

	dir := t.TempDir()
	composePath := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte("services:\n  web:\n"+serviceYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if configYAML != "" {
		if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(
		Config{Files: []string{composePath}, ProjectName: "proj"},
		client.New(upstream.URL),
		upstream.URL,
		&clientconn.Resolver{ConfigFile: configPath},
		fakeAgentView{status: &AgentStatus{}},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// probeLog records every exec the discovery issued, so a test can assert on how
// MANY round trips happened and on the exact argv of each — the two things a
// result-only assertion cannot see.
type probeLog struct {
	cmds [][]string
}

// scriptedProbe installs a containerFS whose Exec answers per candidate. reply is
// keyed by the candidate's argv[0]; a key with no entry means "this candidate is
// not in the image", which the real backend reports as an error from the exec.
func scriptedProbe(s *Server, reply map[string]ExecResult, missing map[string]bool) *probeLog {
	log := &probeLog{}
	s.cfs = &fakeContainerFS{execFn: func(_ string, cmd []string) (ExecResult, error) {
		log.cmds = append(log.cmds, append([]string(nil), cmd...))
		if missing[cmd[0]] {
			return ExecResult{}, fmt.Errorf("exec: %s: no such file or directory", cmd[0])
		}
		res, ok := reply[cmd[0]]
		if !ok {
			return ExecResult{}, fmt.Errorf("exec: %s: no such file or directory", cmd[0])
		}
		return res, nil
	}}
	return log
}

// probeOutput is what a candidate shell that DID run prints: the marker, then one
// line per present path.
func probeOutput(present ...string) ExecResult {
	return ExecResult{
		Stdout:    shellProbeMarker + "\n" + strings.Join(present, "\n") + "\n",
		ExitCode:  0,
		ExitKnown: true,
	}
}

func argvs(lists ...[]string) [][]string { return lists }

// ---- the probe walk ----------------------------------------------------------

func TestDiscoverShellsStopsAtTheFirstCandidateThatRuns(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	// The candidate that answers is in the MIDDLE of the list, with a present
	// candidate after it. That placement is the whole test: were the runnable one
	// last, "stopped early" and "walked to the end" would issue the same number of
	// execs and the assertion below could not fail.
	log := scriptedProbe(s,
		map[string]ExecResult{
			"/bin/bash": probeOutput("/bin/bash", "/bin/sh"),
			"/bin/sh":   probeOutput("/bin/bash", "/bin/sh"),
		},
		map[string]bool{"/bin/zsh": true},
	)

	got, err := s.DiscoverShells(context.Background(), "proj-web",
		[]string{"/bin/zsh", "/bin/bash", "/bin/sh"})
	if err != nil {
		t.Fatalf("DiscoverShells: %v", err)
	}
	want := argvs([]string{"/bin/bash"}, []string{"/bin/sh"})
	if !reflect.DeepEqual(got.Found, want) {
		t.Errorf("Found = %v, want %v", got.Found, want)
	}
	// The count is the assertion. A shell that runs reports on EVERY candidate in
	// one go, so probing /bin/sh afterwards is a wasted round trip that the result
	// cannot show: it would be byte-identical either way.
	if len(log.cmds) != 2 {
		t.Errorf("issued %d execs, want 2 (one miss, then the answer)", len(log.cmds))
	}
}

func TestDiscoverShellsReturnsResolvedOrderNotOutputOrder(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	// The shell that ran reports the present paths in an order that is NOT the
	// candidate order. The result must still rank by the candidate list, because
	// that list IS the user's preference and the first entry is what gets launched.
	scriptedProbe(s,
		map[string]ExecResult{"/bin/bash": probeOutput("/bin/sh", "/bin/bash")},
		map[string]bool{"/bin/zsh": true},
	)

	got, err := s.DiscoverShells(context.Background(), "proj-web",
		[]string{"/bin/zsh", "/bin/bash", "/bin/sh"})
	if err != nil {
		t.Fatalf("DiscoverShells: %v", err)
	}
	want := argvs([]string{"/bin/bash"}, []string{"/bin/sh"})
	if !reflect.DeepEqual(got.Found, want) {
		t.Errorf("Found = %v, want %v (bash first: it outranks sh in the request)", got.Found, want)
	}
}

func TestDiscoverShellsIgnoresOutputWithoutTheMarker(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	log := scriptedProbe(s, map[string]ExecResult{
		// A binary at /bin/zsh that is not a shell: it exits 0 and prints something.
		// Its stdout must not be mistaken for a shell list.
		"/bin/zsh": {Stdout: "/bin/zsh\n/bin/bash\n", ExitCode: 0, ExitKnown: true},
		"/bin/sh":  probeOutput("/bin/sh"),
	}, map[string]bool{"/bin/bash": true})

	got, err := s.DiscoverShells(context.Background(), "proj-web",
		[]string{"/bin/zsh", "/bin/bash", "/bin/sh"})
	if err != nil {
		t.Fatalf("DiscoverShells: %v", err)
	}
	if want := argvs([]string{"/bin/sh"}); !reflect.DeepEqual(got.Found, want) {
		t.Errorf("Found = %v, want %v; unmarked output was trusted", got.Found, want)
	}
	if len(log.cmds) != 3 {
		t.Errorf("issued %d execs, want 3 (the walk must continue past the unmarked reply)", len(log.cmds))
	}
}

func TestDiscoverShellsRefusesAnUnknownExitStatus(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	unknown := probeOutput("/bin/zsh", "/bin/sh")
	unknown.ExitKnown = false // stdout is perfectly plausible; the status is not readable
	scriptedProbe(s, map[string]ExecResult{
		"/bin/zsh": unknown,
		"/bin/sh":  probeOutput("/bin/sh"),
	}, nil)

	got, err := s.DiscoverShells(context.Background(), "proj-web",
		[]string{"/bin/zsh", "/bin/sh"})
	if err != nil {
		t.Fatalf("DiscoverShells: %v", err)
	}
	// An unknown exit is not a negative, but it is emphatically not a positive:
	// docker reports an exec Running for a moment after its stdio closes, so
	// believing this reply would mean believing output from a command that may not
	// have finished.
	if want := argvs([]string{"/bin/sh"}); !reflect.DeepEqual(got.Found, want) {
		t.Errorf("Found = %v, want %v; an unknown exit status was trusted", got.Found, want)
	}
}

func TestDiscoverShellsRefusesANonZeroExit(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	failed := probeOutput("/bin/zsh")
	failed.ExitCode = 127
	scriptedProbe(s, map[string]ExecResult{
		"/bin/zsh": failed,
		"/bin/sh":  probeOutput("/bin/sh"),
	}, nil)

	got, err := s.DiscoverShells(context.Background(), "proj-web", []string{"/bin/zsh", "/bin/sh"})
	if err != nil {
		t.Fatalf("DiscoverShells: %v", err)
	}
	if want := argvs([]string{"/bin/sh"}); !reflect.DeepEqual(got.Found, want) {
		t.Errorf("Found = %v, want %v; a non-zero exit was trusted", got.Found, want)
	}
}

func TestDiscoverShellsReportsNoShellRatherThanAnError(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	scriptedProbe(s, nil, map[string]bool{"/bin/zsh": true, "/bin/sh": true})

	got, err := s.DiscoverShells(context.Background(), "proj-web", []string{"/bin/zsh", "/bin/sh"})
	// "This image has no shell" is a FACT the caller acts on (it asks the user for a
	// command). Reporting it as an error would collapse it into "something went
	// wrong", which is the dead end this whole feature exists to remove.
	if err != nil {
		t.Fatalf("DiscoverShells returned an error for a shell-less image: %v", err)
	}
	if len(got.Found) != 0 {
		t.Errorf("Found = %v, want empty", got.Found)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("Candidates = %v, want the two probed entries", got.Candidates)
	}
}

// The candidate list is partly attacker-reachable: it comes from a browser against
// a BFF with no authentication. A candidate must therefore only ever be an
// ARGUMENT, never text spliced into the script the container runs.
func TestShellProbeNeverInterpolatesACandidateIntoTheScript(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	log := scriptedProbe(s, nil, nil) // every candidate misses; we only want the argv

	const evil = `/bin/x; rm -rf /`
	if _, err := s.DiscoverShells(context.Background(), "proj-web", []string{evil}); err != nil {
		t.Fatalf("DiscoverShells: %v", err)
	}
	if len(log.cmds) == 0 {
		t.Fatal("no exec was issued")
	}
	for _, cmd := range log.cmds {
		i := indexOf(cmd, "-c")
		if i < 0 || i+1 >= len(cmd) {
			t.Fatalf("exec %q has no -c SCRIPT pair", cmd)
		}
		// Byte-identical to the package constant. An implementation that built the
		// script with Sprintf would still contain the loop and still "work" — and
		// would still fail here, which is the point.
		if cmd[i+1] != shellProbeScript {
			t.Errorf("script argument was not the constant:\n got %q\nwant %q", cmd[i+1], shellProbeScript)
		}
		// $0 is the literal "sh"; everything after it is data.
		if cmd[i+2] != "sh" {
			t.Errorf("exec %q: argv[0] slot = %q, want \"sh\"", cmd, cmd[i+2])
		}
	}
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

// ---- candidate resolution ----------------------------------------------------

func TestResolveShellCandidatesOrdersEntrypointSpecContextClient(t *testing.T) {
	s := shellsServer(t, `    image: example/web:1
    entrypoint: ["/bin/bash", "-c", "exec app"]
    x-cornus-shells:
      - /bin/dash
`, `contexts:
  dev:
    shells:
      - /bin/zsh
current-context: dev
`)
	got := s.resolveShellCandidates("proj-web", []string{"/bin/sh", "/bin/bash"})
	want := argvs(
		[]string{"/bin/bash"}, // entrypoint: the one shell we already have evidence for
		[]string{"/bin/dash"}, // the service's own declaration
		[]string{"/bin/zsh"},  // the selected connection context
		[]string{"/bin/sh"},   // the browser's list; its /bin/bash deduped away
	)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
}

func TestResolveShellCandidatesDedupesKeepingTheFirstPosition(t *testing.T) {
	s := shellsServer(t, `    image: example/web:1
    x-cornus-shells:
      - /bin/bash
      - /bin/dash
`, "")
	got := s.resolveShellCandidates("proj-web", []string{"/bin/sh", "/bin/bash"})
	// /bin/bash must stay at the FRONT (where the service put it) rather than moving
	// to the browser's position. A dedupe that kept the LAST occurrence still yields
	// a list of the right length containing exactly the right entries — and launches
	// the wrong shell. Three entries, so the reordering cannot cancel itself out.
	want := argvs([]string{"/bin/bash"}, []string{"/bin/dash"}, []string{"/bin/sh"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
}

func TestResolveShellCandidatesUsesCommandWhenEntrypointIsNotAShell(t *testing.T) {
	s := shellsServer(t, `    image: example/web:1
    entrypoint: ["/usr/local/bin/docker-entrypoint.sh"]
    command: ["/bin/ash", "-l"]
`, "")
	got := s.resolveShellCandidates("proj-web", nil)
	// The entrypoint's BASENAME ends in .sh but the program is not a shell, so it
	// must not be offered; the command's /bin/ash is the real one.
	want := argvs([]string{"/bin/ash"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
}

func TestResolveShellCandidatesSkipsTheSpecForANonProjectWorkload(t *testing.T) {
	s := shellsServer(t, `    image: example/web:1
    entrypoint: ["/bin/bash"]
    x-cornus-shells:
      - /bin/dash
`, "")
	// "stray" is deployed but is not part of the loaded compose project, so the BFF
	// has no plan for it — and no way to read one back from the server. It must fall
	// through to the caller's list rather than borrow another service's answer.
	got := s.resolveShellCandidates("stray", []string{"/bin/sh"})
	want := argvs([]string{"/bin/sh"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
}

func TestResolveShellCandidatesSplitsMultiWordEntries(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	got := s.resolveShellCandidates("proj-web", []string{"/bin/busybox sh", "  ", "/bin/sh"})
	want := argvs([]string{"/bin/busybox", "sh"}, []string{"/bin/sh"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
}

// A candidate the shell-words parser cannot read (here: an unbalanced quote) is
// DROPPED, not passed through half-parsed and not turned into an error. The list
// is a free-text setting a user edits by hand, so one typo must not take the whole
// terminal down with it.
func TestResolveShellCandidatesDropsAnUnparsableEntry(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	got := s.resolveShellCandidates("proj-web", []string{`/bin/"oops`, "/bin/sh"})
	want := argvs([]string{"/bin/sh"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
}

func TestResolveShellCandidatesCapsTheList(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	client := make([]string, maxShellCandidates+10)
	for i := range client {
		client[i] = fmt.Sprintf("/bin/sh%d", i)
	}
	if got := len(s.resolveShellCandidates("proj-web", client)); got != maxShellCandidates {
		t.Errorf("resolved %d candidates, want the cap of %d", got, maxShellCandidates)
	}
}

// TestShellFromArgv moved to pkg/shells (TestFromArgv) with the function itself.

func TestParseShellProbe(t *testing.T) {
	candidates := argvs([]string{"/bin/zsh"}, []string{"/bin/busybox", "sh"}, []string{"/bin/sh"})
	cases := []struct {
		name   string
		stdout string
		want   [][]string
		ok     bool
	}{
		{"marker only", shellProbeMarker + "\n", [][]string{}, true},
		{
			"present subset", shellProbeMarker + "\n/bin/sh\n/bin/busybox\n",
			argvs([]string{"/bin/busybox", "sh"}, []string{"/bin/sh"}), true,
		},
		// A path the caller never asked about is not smuggled into the answer.
		{"unknown line", shellProbeMarker + "\n/bin/fish\n", [][]string{}, true},
		{"crlf", shellProbeMarker + "\r\n/bin/sh\r\n", argvs([]string{"/bin/sh"}), true},
		{"no marker", "/bin/sh\n", nil, false},
		{"empty", "", nil, false},
		{"marker not first", "warning: something\n" + shellProbeMarker + "\n", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseShellProbe(tc.stdout, candidates)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("found = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---- caching -----------------------------------------------------------------

func TestDiscoverShellsCachesPerWorkloadAndResolvedList(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	log := scriptedProbe(s, map[string]ExecResult{"/bin/sh": probeOutput("/bin/sh")}, nil)

	if _, err := s.DiscoverShells(context.Background(), "proj-web", []string{"/bin/sh"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := len(log.cmds)
	if _, err := s.DiscoverShells(context.Background(), "proj-web", []string{"/bin/sh"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(log.cmds) != first {
		t.Errorf("the repeat issued %d more execs, want 0", len(log.cmds)-first)
	}

	// A DIFFERENT list must re-probe. Keying on the workload alone would answer a
	// question nobody asked — with a well-formed list, so nothing downstream could
	// tell.
	if _, err := s.DiscoverShells(context.Background(), "proj-web", []string{"/bin/bash", "/bin/sh"}); err != nil {
		t.Fatalf("third: %v", err)
	}
	if len(log.cmds) == first {
		t.Error("a changed candidate list reused the cached answer")
	}
}

func TestDiscoverShellsReProbesAfterTheTTL(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	log := scriptedProbe(s, map[string]ExecResult{"/bin/sh": probeOutput("/bin/sh")}, nil)

	if _, err := s.DiscoverShells(context.Background(), "proj-web", []string{"/bin/sh"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := len(log.cmds)
	// Age the entry past the TTL. A workload can be redeployed onto a different
	// image under the same name, so a cached answer must expire rather than outlive
	// the container it describes.
	s.shellsMu.Lock()
	for k, p := range s.shellsKnown {
		p.at = time.Now().Add(-shellProbeTTL - time.Second)
		s.shellsKnown[k] = p
	}
	s.shellsMu.Unlock()

	if _, err := s.DiscoverShells(context.Background(), "proj-web", []string{"/bin/sh"}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(log.cmds) == first {
		t.Error("a stale cache entry was reused past the TTL")
	}
}

// ---- HTTP surface ------------------------------------------------------------

func TestShellsEndpointReturnsCandidatesAndFound(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	scriptedProbe(s, map[string]ExecResult{"/bin/sh": probeOutput("/bin/sh")},
		map[string]bool{"/bin/busybox": true})

	rec := doReq(t, s, "POST", "/.cornus/web/workloads/proj-web/shells",
		`{"candidates":["/bin/busybox sh","/bin/sh"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got shellsResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantCandidates := argvs([]string{"/bin/busybox", "sh"}, []string{"/bin/sh"})
	if !reflect.DeepEqual(got.Candidates, wantCandidates) {
		t.Errorf("candidates = %v, want %v", got.Candidates, wantCandidates)
	}
	if want := argvs([]string{"/bin/sh"}); !reflect.DeepEqual(got.Found, want) {
		t.Errorf("found = %v, want %v", got.Found, want)
	}
}

func TestShellsEndpointBoundsCandidateCount(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	scriptedProbe(s, nil, nil)

	list := make([]string, maxShellCandidates+1)
	for i := range list {
		list[i] = fmt.Sprintf("/bin/sh%d", i)
	}
	body, _ := json.Marshal(discoverShellsRequest{Candidates: list})
	rec := doReq(t, s, "POST", "/.cornus/web/workloads/proj-web/shells", string(body))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (each absent candidate costs an exec round trip)", rec.Code)
	}
}

func TestShellsEndpointRequiresARunningWorkload(t *testing.T) {
	upstream := fakeCornusServer(t, []api.DeployStatus{
		{Name: "proj-web", Instances: []api.InstanceStatus{{Running: false}}},
	}, nil)
	dir := t.TempDir()
	composePath := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte("services:\n  web:\n    image: example/web:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := New(
		Config{Files: []string{composePath}, ProjectName: "proj"},
		client.New(upstream.URL), upstream.URL,
		&clientconn.Resolver{ConfigFile: filepath.Join(dir, "config.yaml")},
		fakeAgentView{status: &AgentStatus{}},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	scriptedProbe(s, nil, nil)

	rec := doReq(t, s, "POST", "/.cornus/web/workloads/proj-web/shells", `{"candidates":["/bin/sh"]}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for a stopped workload", rec.Code)
	}
}

// The 404 branch is reached only when the SERVER errors for the name. It is not the
// common case: dockerhost's Status lists containers by label, so an unknown name
// yields an empty status with no error and comes back 409 through the row above —
// which is what e2e/scenarios/web.star measures against a live daemon. Both are
// refusals, and neither may be confused with an empty `found`.
func TestShellsEndpointIsNotFoundWhenTheServerRejectsTheName(t *testing.T) {
	s := shellsServer(t, "    image: example/web:1\n", "")
	scriptedProbe(s, nil, nil)

	rec := doReq(t, s, "POST", "/.cornus/web/workloads/nope/shells", `{"candidates":["/bin/sh"]}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"found"`) {
		t.Errorf("a refusal must not look like an empty answer: %s", rec.Body.String())
	}
}

// ---- the create-terminal command line ----------------------------------------

func TestCreateTermRequestSplitsCmdlineWithShellwords(t *testing.T) {
	cases := []struct {
		name string
		req  createTermRequest
		want []string
	}{
		// The case this field exists for: wrapping the typed string into a
		// one-element Cmd asks to execute a file whose NAME contains a space.
		{"multi word", createTermRequest{Cmdline: "/bin/busybox sh"}, []string{"/bin/busybox", "sh"}},
		{"quoted", createTermRequest{Cmdline: `/bin/sh -c "echo hi"`}, []string{"/bin/sh", "-c", "echo hi"}},
		{"single word", createTermRequest{Cmdline: "/bin/bash"}, []string{"/bin/bash"}},
		{"padded", createTermRequest{Cmdline: "  /bin/bash  "}, []string{"/bin/bash"}},
		{"empty", createTermRequest{}, nil},
		{"blank", createTermRequest{Cmdline: "   "}, nil},
		// An explicit argv is already split; it must not be re-parsed or overridden.
		{"cmd wins", createTermRequest{Cmd: []string{"/bin/zsh"}, Cmdline: "/bin/bash"}, []string{"/bin/zsh"}},
		{"cmd with a spacey path", createTermRequest{Cmd: []string{"/opt/my shell"}}, []string{"/opt/my shell"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.cmd(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("cmd() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A create with neither Cmd nor Cmdline still falls back to /bin/sh, so a caller
// that knows nothing about discovery (the mock server, a hand-rolled request) is
// unaffected by this change.
func TestCreateTermWithoutACommandStillDefaults(t *testing.T) {
	m := newTermManager(&fakeExec{})
	ts, err := m.Create(context.Background(), "proj-web", createTermRequest{Workload: "proj-web"}.cmd(), "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if want := []string{"/bin/sh"}; !reflect.DeepEqual(ts.cmd, want) {
		t.Errorf("cmd = %q, want %q", ts.cmd, want)
	}
}
