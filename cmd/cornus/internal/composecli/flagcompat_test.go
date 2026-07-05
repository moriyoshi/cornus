package composecli

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"cornus/pkg/compose"
)

// mustContain fails unless every fragment appears in got. Warning/notice text is
// the whole deliverable for the flags cornus accepts but cannot honor, so the
// assertions are on the words the user actually reads: what was ignored, why,
// and what to do instead.
func mustContain(t *testing.T, got string, fragments ...string) {
	t.Helper()
	for _, f := range fragments {
		if !strings.Contains(got, f) {
			t.Errorf("output %q does not contain %q", got, f)
		}
	}
}

func TestWarnShutdownTimeoutNamesTheAlternative(t *testing.T) {
	var buf bytes.Buffer
	warnShutdownTimeout(testDriver(&buf), 30, "down")
	mustContain(t, buf.String(),
		"-t/--timeout",
		"cannot be honored on down",
		"no per-call shutdown timeout",
		"stop_grace_period: 30s",
	)
}

func TestWarnShutdownTimeoutSilentWhenUnset(t *testing.T) {
	var buf bytes.Buffer
	warnShutdownTimeout(testDriver(&buf), timeoutUnset, "stop")
	if buf.Len() != 0 {
		t.Fatalf("no flag passed, want no output, got %q", buf.String())
	}
}

// A user who explicitly asks for an immediate stop (-t 0) is still told the flag
// does nothing: 0 is a value, not the absence of one.
func TestWarnShutdownTimeoutZeroStillWarns(t *testing.T) {
	var buf bytes.Buffer
	warnShutdownTimeout(testDriver(&buf), 0, "up")
	mustContain(t, buf.String(), "stop_grace_period: 0s")
}

func TestWarnRemoveImages(t *testing.T) {
	var buf bytes.Buffer
	warnRemoveImages(testDriver(&buf), "all")
	mustContain(t, buf.String(),
		"--rmi=all",
		"removes nothing",
		"no image-removal API",
		"cornus storage",
	)
}

func TestWarnRemoveImagesSilentWhenUnset(t *testing.T) {
	var buf bytes.Buffer
	warnRemoveImages(testDriver(&buf), "")
	if buf.Len() != 0 {
		t.Fatalf("no flag passed, want no output, got %q", buf.String())
	}
}

// The --no-attach divergence is only worth a warning when the command line is
// genuinely ambiguous: --no-attach WITH a service list means one thing here and
// another in docker compose.
func TestWarnNoAttachDivergence(t *testing.T) {
	var buf bytes.Buffer
	warnNoAttachDivergence(testDriver(&buf), []string{"web"})
	mustContain(t, buf.String(), "--no-attach is a project-wide switch", "only [web]", "docker compose")

	buf.Reset()
	warnNoAttachDivergence(testDriver(&buf), nil)
	if buf.Len() != 0 {
		t.Fatalf("no service list, want no output, got %q", buf.String())
	}
}

func TestNoteAlwaysPushNamesTheRegistry(t *testing.T) {
	var buf bytes.Buffer
	noteAlwaysPush(testDriver(&buf), "registry.example:5000")
	mustContain(t, buf.String(), "--push is already the default", "registry.example:5000")
}

// depsRuntime builds a runtime whose services form the dependency graph
// described by deps (service -> its depends_on targets), with every service
// planned and rt.order in the given order.
func depsRuntime(order []string, deps map[string][]string) *runtime {
	svcs := map[string]compose.ServiceDocument{}
	plans := map[string]compose.ServicePlan{}
	for _, name := range order {
		var on compose.DependsOn
		for _, dep := range deps[name] {
			on = append(on, compose.Dependency{Service: dep, Condition: compose.DependsOnStarted, Required: true})
		}
		svcs[name] = compose.ServiceDocument{DependsOn: on}
		plans[name] = compose.ServicePlan{Service: name, Resource: "proj-" + name}
	}
	return &runtime{
		project: compose.NewProject(&compose.ProjectDocument{Services: svcs}).View(nil),
		plans:   plans,
		order:   order,
	}
}

func TestExpandDependenciesTransitiveAndOrdered(t *testing.T) {
	// order is dependency order: cache and db come before api, api before web.
	rt := depsRuntime([]string{"cache", "db", "api", "web"}, map[string][]string{
		"api": {"db", "cache"},
		"web": {"api"},
	})
	selected, added := expandDependencies(rt, []string{"web"})
	if got, want := strings.Join(selected, ","), "cache,db,api,web"; got != want {
		t.Errorf("selected = %q, want %q (dependency order, not discovery order)", got, want)
	}
	if got, want := strings.Join(added, ","), "cache,db,api"; got != want {
		t.Errorf("added = %q, want %q", got, want)
	}
}

func TestExpandDependenciesNoOpWithoutDependencies(t *testing.T) {
	rt := depsRuntime([]string{"db", "web"}, nil)
	selected, added := expandDependencies(rt, []string{"web"})
	if got, want := strings.Join(selected, ","), "web"; got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want none", added)
	}
}

// A dependency that is not part of the active selection (excluded by a profile,
// or simply not in the project) must not be resurrected: rt.plans is the
// authority on what this run can deploy.
func TestExpandDependenciesSkipsUnplannedServices(t *testing.T) {
	rt := depsRuntime([]string{"web"}, map[string][]string{"web": {"absent"}})
	selected, added := expandDependencies(rt, []string{"web"})
	if got, want := strings.Join(selected, ","), "web"; got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want none (the dependency has no plan)", added)
	}
}

// A dependency cycle must not hang the expansion (compose rejects cycles at
// planning time, but this walk must be safe regardless of what reaches it).
func TestExpandDependenciesTerminatesOnCycle(t *testing.T) {
	rt := depsRuntime([]string{"a", "b"}, map[string][]string{"a": {"b"}, "b": {"a"}})
	selected, _ := expandDependencies(rt, []string{"a"})
	if got, want := strings.Join(selected, ","), "a,b"; got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
}

// The whole-project case is left alone: every service is selected already, so
// expansion can neither add nor reorder anything.
func TestExpandDependenciesWholeProjectUnchanged(t *testing.T) {
	rt := depsRuntime([]string{"db", "web"}, map[string][]string{"web": {"db"}})
	selected, added := expandDependencies(rt, []string{"db", "web"})
	if got, want := strings.Join(selected, ","), "db,web"; got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want none", added)
	}
}

// --force-recreate stamps a token into every selected service's labels so the
// spec the server receives differs from the live one. On kubernetes — the one
// backend whose Apply is a patch rather than an unconditional recreate — compose
// labels land in the pod template's annotations, so this is what turns an
// otherwise identical Apply into a rollout.
func TestForceRecreateStampsEverySelectedService(t *testing.T) {
	rt := depsRuntime([]string{"db", "web"}, nil)
	rt.baseDir = t.TempDir()

	if err := (&UpCmd{}).applyServiceOverrides(rt, []string{"db", "web"}); err != nil {
		t.Fatalf("applyServiceOverrides: %v", err)
	}
	for _, n := range []string{"db", "web"} {
		if _, ok := rt.plans[n].Spec.Labels[forceRecreateLabel]; ok {
			t.Fatalf("%s carries the force-recreate label without the flag", n)
		}
	}

	if err := (&UpCmd{ForceRecreate: true}).applyServiceOverrides(rt, []string{"db", "web"}); err != nil {
		t.Fatalf("applyServiceOverrides: %v", err)
	}
	tokens := map[string]string{}
	for _, n := range []string{"db", "web"} {
		got := rt.plans[n].Spec.Labels[forceRecreateLabel]
		if got == "" {
			t.Fatalf("%s: no %s label after --force-recreate", n, forceRecreateLabel)
		}
		tokens[n] = got
	}
	// One token per invocation, so a project's services recreate as a unit.
	if tokens["db"] != tokens["web"] {
		t.Fatalf("services got different tokens (%q vs %q); want one per up", tokens["db"], tokens["web"])
	}
}

// The stamp must not leak into a service's existing labels map, which the
// compose plan owns and a --watch reload re-reads.
func TestForceRecreatePreservesExistingLabels(t *testing.T) {
	rt := depsRuntime([]string{"web"}, nil)
	rt.baseDir = t.TempDir()
	original := map[string]string{"team": "infra"}
	plan := rt.plans["web"]
	plan.Spec.Labels = original
	rt.plans["web"] = plan

	if err := (&UpCmd{ForceRecreate: true}).applyServiceOverrides(rt, []string{"web"}); err != nil {
		t.Fatalf("applyServiceOverrides: %v", err)
	}
	if got := rt.plans["web"].Spec.Labels["team"]; got != "infra" {
		t.Fatalf("existing label lost: team = %q", got)
	}
	if _, leaked := original[forceRecreateLabel]; leaked {
		t.Fatal("the force-recreate token was written into the plan's own labels map")
	}
}

// A bad --rmi value is a typo in the command line, so it is rejected before any
// connection is attempted — unlike a valid one, which warns and proceeds.
func TestDownRejectsUnknownRMIValue(t *testing.T) {
	err := (&DownCmd{RMI: "bogus"}).Run(nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "want local or all") {
		t.Fatalf("Run() = %v, want a rejection naming the accepted values", err)
	}
}

// `compose logs -f web` reads "web" as a compose file (the group owns -f/--file
// and kong cannot shadow it). The resulting not-found error must explain the
// flag rather than leave the user wondering what "open web" meant.
func TestExplainFileFlagErrorNamesTheFlag(t *testing.T) {
	notFound := &fs.PathError{Op: "open", Path: "web", Err: fs.ErrNotExist}
	got := explainFileFlagError([]string{"web"}, notFound)
	if got == nil {
		t.Fatal("explainFileFlagError swallowed the error")
	}
	mustContain(t, got.Error(), "open web", "-f/--file names the COMPOSE FILE", "logs --follow")
	if !errors.Is(got, fs.ErrNotExist) {
		t.Fatal("the wrapped error must still match fs.ErrNotExist")
	}
}

// A genuine path typo keeps its plain error: the hint is for values that were
// never meant to be files.
func TestExplainFileFlagErrorLeavesRealPathsAlone(t *testing.T) {
	for _, path := range []string{"compose.yaml", "deploy/compose.yml", "./stack"} {
		err := explainFileFlagError([]string{path}, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist})
		if strings.Contains(err.Error(), "--follow") {
			t.Errorf("%q: got the -f hint on what looks like a real path: %v", path, err)
		}
	}
}

func TestExplainFileFlagErrorPassesThroughOtherErrors(t *testing.T) {
	boom := errors.New("yaml: line 3: mapping values are not allowed")
	if got := explainFileFlagError([]string{"web"}, boom); got != boom {
		t.Fatalf("explainFileFlagError rewrote an unrelated error: %v", got)
	}
	if got := explainFileFlagError([]string{"web"}, nil); got != nil {
		t.Fatalf("explainFileFlagError(nil) = %v, want nil", got)
	}
}
