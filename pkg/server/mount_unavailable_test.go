package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"cornus/pkg/deploy"
	"cornus/pkg/hostcheck"
)

// namedRemoteMountingBackend is a MountingBackend + RemoteCapable whose Name()
// is settable, so the deploy-attach router's per-backend branching can be
// exercised for a backend shape without standing up that backend (containerdhost
// is linux-only and needs a live containerd socket).
type namedRemoteMountingBackend struct {
	fakeRemoteMountingBackend
	name string
}

func (f *namedRemoteMountingBackend) Name() string { return f.name }

// namedPlainBackend is a Backend that does NOT implement MountingBackend, which
// is incus's shape.
type namedPlainBackend struct {
	fakeBackend
	name string
}

func (f *namedPlainBackend) Name() string { return f.name }

// TestClientLocalMountsUnavailableNamesTheKnob is the regression test for a
// message that was confidently wrong.
//
// A mounts-only apply picks one of three paths (deploy_attach.go): the caretaker
// sidecar when the backend is a MountingBackend and useSidecarMounts agrees; the
// server-side kernel-9P fast path when the backend has one; otherwise a
// rejection. containerd is a MountingBackend and IS RemoteCapable, so with
// CORNUS_CONTAINERD_REMOTE unset useSidecarMounts returns false — and containerd
// has no host fast path, so it landed on the rejection, which said "client-local
// mounts are not supported by the containerd backend".
//
// That sentence is false. containerd realizes client-local mounts perfectly well
// with CORNUS_CONTAINERD_REMOTE=1 — the only E2E coverage of the containerd mount
// path (e2e/scenarios/deploy-mounts-sidecar-containerd.star) sets exactly that.
// So the operator was told the feature does not exist on their backend while the
// one variable that turns it on went unmentioned. Nothing logged the difference;
// the deploy simply failed with an authoritative-sounding reason to stop trying.
//
// Three doc comments described the co-located fallback containerd was supposedly
// getting instead (containerdhost.WithRemote, defaultBackendFactory's containerd
// arm, and TestUseSidecarMounts's own docstring), which is how the message
// survived review. Only the `remote` field comment in backend_linux.go had it
// right.
func TestClientLocalMountsUnavailableNamesTheKnob(t *testing.T) {
	cases := []struct {
		name    string
		backend deploy.Backend
		// reachable records whether a mounts-only apply against this backend
		// actually lands on the rejection. It is asserted, not assumed: a message
		// test proves nothing about a shape that never reaches the message.
		reachable bool
		want      []string
		reject    []string
	}{
		{
			name:      "local containerd names CORNUS_CONTAINERD_REMOTE",
			backend:   &namedRemoteMountingBackend{name: "containerd"},
			reachable: true,
			want:      []string{"containerd", "CORNUS_CONTAINERD_REMOTE"},
			reject:    []string{"not supported"},
		},
		{
			// dockerhost cannot reach the rejection (it has the fast path) — hence
			// reachable:false. Kept because if a future change did route it here,
			// the advice must be dockerhost's real variable, not containerd's.
			name:      "a hypothetical local dockerhost names CORNUS_DOCKER_REMOTE",
			backend:   &namedRemoteMountingBackend{name: "dockerhost"},
			reachable: false,
			want:      []string{"dockerhost", "CORNUS_DOCKER_REMOTE"},
			reject:    []string{"not supported", "CORNUS_CONTAINERD_REMOTE"},
		},
		{
			// incus has no ApplyWithMounts, so there is no mode to switch on and
			// the original sentence is the truthful one. Naming CORNUS_INCUS_REMOTE
			// here would be the same defect in reverse: advice that cannot help.
			name:      "incus is genuinely unsupported and names no variable",
			backend:   &namedPlainBackend{name: "incus"},
			reachable: true,
			want:      []string{"incus", "not supported"},
			reject:    []string{"CORNUS_"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// These are exactly the conditions deploy_attach.go's mounts-only case
			// checks before it rejects. The MountingBackend assertion is part of
			// the sidecar test and cannot be dropped: incus fails it and so is
			// rejected however Remote() answers.
			_, mounting := tc.backend.(deploy.MountingBackend)
			reachable := !(mounting && useSidecarMounts(tc.backend)) && !hostcheck.UsesHostMountFastPath(tc.backend.Name())
			if reachable != tc.reachable {
				t.Fatalf("a mounts-only apply on %q reaches the rejection = %v, want %v: the routing this message "+
					"belongs to has changed, so re-derive which backends land here before trusting the rows below",
					tc.backend.Name(), reachable, tc.reachable)
			}

			got := clientLocalMountsUnavailable(tc.backend)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("message %q does not mention %q", got, w)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(got, r) {
					t.Errorf("message %q still says %q, which is what made it misleading", got, r)
				}
			}
		})
	}
}

// TestRemoteModeEnvsAreTheVariablesTheFactoryReads closes the loop the message
// opens: naming a variable is only useful if the server actually reads it.
//
// remoteModeEnvs is the single source now — defaultBackendFactory calls
// remoteModeEnabled instead of its own os.Getenv — so this asserts the property
// that made the consolidation worth doing: no CORNUS_*_REMOTE literal survives
// anywhere in server.go. A reintroduced literal would silently split the two
// again, and the split is invisible: the factory would honour one name while the
// error told the operator to set another.
func TestRemoteModeEnvsAreTheVariablesTheFactoryReads(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	known := map[string]bool{}
	for _, env := range remoteModeEnvs {
		known[env] = true
	}
	if !known["CORNUS_CONTAINERD_REMOTE"] {
		t.Fatal("remoteModeEnvs no longer carries CORNUS_CONTAINERD_REMOTE; this test has lost its subject")
	}
	// Only os.Getenv arguments count. The variable names appear in prose
	// comments throughout server.go, and prose is not a second reader.
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Getenv" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil || !known[name] {
			return true
		}
		t.Errorf("server.go:%d reads %s with os.Getenv directly; route it through remoteModeEnabled so the "+
			"variable the factory honours and the variable clientLocalMountsUnavailable tells the operator to set "+
			"cannot drift apart", fset.Position(lit.Pos()).Line, name)
		return true
	})
}

// TestEveryWithRemoteArmAsksRemoteModeEnabled covers what the test above cannot,
// and the gap was real rather than theoretical.
//
// Rejecting `os.Getenv("CORNUS_..._REMOTE")` literals only forbids ONE way of
// disagreeing with remoteModeEnvs. Two others leave it green and are worse,
// because neither reads like a mistake:
//
//   - WithRemote(false) — remote mode is now unreachable for that backend, and
//     clientLocalMountsUnavailable still tells the operator to set a variable
//     that no longer does anything. The advice becomes a dead end that the server
//     itself prints.
//   - the option dropped entirely — same outcome, and nothing in the diff even
//     mentions the variable.
//
// So this asserts the positive form: every WithRemote in the factory is handed
// remoteModeEnabled(...), and there is one per RemoteCapable backend. The count
// is what catches deletion; a bare presence check cannot.
func TestEveryWithRemoteArmAsksRemoteModeEnabled(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	var factory *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "defaultBackendFactory" {
			factory = fn
			break
		}
	}
	if factory == nil {
		t.Fatal("no defaultBackendFactory in server.go; this test has lost track of what it guards")
	}
	var arms int
	ast.Inspect(factory, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithRemote" {
			return true
		}
		arms++
		line := fset.Position(call.Pos()).Line
		if len(call.Args) != 1 {
			t.Errorf("server.go:%d: WithRemote takes %d args, want 1", line, len(call.Args))
			return true
		}
		inner, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			t.Errorf("server.go:%d: WithRemote is passed a non-call argument. A literal here (WithRemote(false), "+
				"say) silently pins the mode off while clientLocalMountsUnavailable still tells the operator to set "+
				"the environment variable — advice the server prints and then ignores.", line)
			return true
		}
		id, ok := inner.Fun.(*ast.Ident)
		if !ok || id.Name != "remoteModeEnabled" {
			t.Errorf("server.go:%d: WithRemote's argument does not come from remoteModeEnabled, so this backend's "+
				"remote mode and the variable named in the error message are decided in two places again.", line)
		}
		return true
	})
	// One per RemoteCapable backend: dockerhost, containerd, bare, incus.
	// kubernetes has no remote mode and correctly passes no option.
	if want := len(remoteModeEnvs); arms != want {
		t.Errorf("defaultBackendFactory has %d WithRemote arms, want %d (one per entry in remoteModeEnvs). "+
			"A dropped option leaves that backend permanently in local mode with no diff line mentioning the "+
			"variable, and the rejection message keeps advertising it.", arms, want)
	}
}

// TestHostMountFastPathIsOneClassification pins the agreement that the exported
// hostcheck.UsesHostMountFastPath now makes structural.
//
// pkg/server routes a mounts-only apply onto the kernel-9P path for exactly the
// backends this predicate names, and pkg/hostcheck runs the mount-PROPAGATION
// check for exactly the backends it names. Those were two independent literal
// comparisons. Divergence had no error path: a backend the server routed onto the
// fast path but hostcheck did not check would get its mounts-dir propagation
// unverified, and an unpropagated mount shows up as an EMPTY directory inside the
// container with every startup check green — the precise failure pkg/hostcheck
// exists to prevent.
func TestHostMountFastPathIsOneClassification(t *testing.T) {
	for _, tc := range []struct {
		backend string
		want    bool
	}{
		{"dockerhost", true},
		{"", true}, // the unset default IS dockerhost
		{"bare", true},
		{"containerd", false},
		{"incus", false},
		{"kubernetes", false},
		{"k8s", false},
	} {
		if got := hostcheck.UsesHostMountFastPath(tc.backend); got != tc.want {
			t.Errorf("UsesHostMountFastPath(%q) = %v, want %v", tc.backend, got, tc.want)
		}
	}
	// And server.go must not keep a private copy: the branch it guards used to
	// compare Name() against literals inline.
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "deploy_attach.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse deploy_attach.go: %v", err)
	}
	var uses int
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "UsesHostMountFastPath" {
			uses++
		}
		return true
	})
	if uses == 0 {
		t.Error("deploy_attach.go no longer asks hostcheck.UsesHostMountFastPath which backends have the " +
			"co-located path; if the comparison has gone back inline, the two classifications can diverge silently")
	}
}
