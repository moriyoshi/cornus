package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestKnownDeployBackendsMatchTheFactorySwitch enforces the invariant
// knownDeployBackends states in a comment and nothing checked:
// "Kept in sync with defaultBackendFactory's switch."
//
// The two directions are not equally dangerous, which is why this is worth a
// test rather than care.
//
//   - A name in the SWITCH but not the list is loud: validateDeployBackend rejects
//     the value, the server refuses to start, and whoever added the backend finds
//     out immediately.
//   - A name in the LIST but not the switch is SILENT. defaultBackendFactory falls
//     through to dockerhost for anything it does not recognise, so the operator
//     gets a running server on the wrong backend. validateDeployBackend's own doc
//     comment describes exactly this failure for the near-miss "docker": the right
//     backend with the wrong registry semantics, "no diagnostic anywhere", builds
//     pushing blobs to a store the operator never intended. The list existing is
//     what turns that into a startup error — so a list entry the switch does not
//     handle re-opens the hole the list was added to close.
//
// Reading the switch out of the AST rather than calling the factory keeps this a
// pure source-level check: constructing a real backend needs a daemon, a
// kubeconfig, or root depending on the arm.
func TestKnownDeployBackendsMatchTheFactorySwitch(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}

	handled := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "defaultBackendFactory" {
			return true
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					handled[s] = true
				}
			}
			return true
		})
		return false
	})
	if len(handled) == 0 {
		t.Fatal("found no case values in defaultBackendFactory; this test has lost track of the function it guards")
	}

	for _, name := range knownDeployBackends {
		// "dockerhost" is the DEFAULT arm, not a case: the switch ends by
		// constructing dockerhost, which is also what "" selects. It is therefore
		// legitimately handled without appearing as a case value. Every OTHER name
		// must be an explicit case, because for those the default arm is the bug.
		if name == "dockerhost" {
			continue
		}
		if !handled[name] {
			t.Errorf("knownDeployBackends accepts %q but defaultBackendFactory has no case for it: "+
				"the switch falls through to dockerhost, so the operator gets a running server on the WRONG backend "+
				"with no diagnostic — the exact failure validateDeployBackend exists to prevent", name)
		}
	}
	for name := range handled {
		if name == "dockerhost" {
			continue // the default arm; may or may not appear as an explicit case
		}
		var listed bool
		for _, known := range knownDeployBackends {
			if known == name {
				listed = true
				break
			}
		}
		if !listed {
			t.Errorf("defaultBackendFactory handles %q but knownDeployBackends does not list it: "+
				"validateDeployBackend will reject the value and the server will refuse to start with that backend selected", name)
		}
	}
}

// TestIsHostBackendClassifiesEveryKnownBackend forces a deliberate answer for
// every accepted CORNUS_DEPLOY_BACKEND value, instead of letting a new backend
// inherit one by falling off the end of an expression.
//
// isHostBackend decides whether the registry re-exports the runtime's own image
// store (host-native) or runs the classic CAS. Its shape is `a || b || c`, so a
// backend nobody considered is not rejected — it is silently answered "no". That
// is the same failure `validateDeployBackend` exists to prevent one level up: a
// server that starts fine, on the right backend, with the wrong registry
// semantics and no diagnostic anywhere.
//
// The classification below is the CONTRACT, not a mirror of the implementation —
// it is written from what each backend can actually do, so a change to
// isHostBackend that flips an answer fails here rather than being ratified. The
// enumeration is driven off knownDeployBackends so adding a backend without
// deciding this question cannot compile-and-pass.
func TestIsHostBackendClassifiesEveryKnownBackend(t *testing.T) {
	// "" is the unset form of dockerhost and must classify identically to it; a
	// predicate that handled only the spelled-out name would send an operator who
	// never set the variable to the wrong registry.
	want := map[string]bool{
		"":           true, // unset selects dockerhost
		"dockerhost": true, // re-exports the Docker daemon's image store
		// podman re-exports through the SAME docker-daemon source: its image
		// save/load endpoints are Docker-shaped, so only the socket differs. Without
		// this a podman server would get the classic CAS and every build would
		// round-trip a full image through it.
		"podman":     true,
		"containerd": true,  // re-exports containerd's content store
		"kubernetes": false, // no local store on the server's own host
		"k8s":        false, // alias of kubernetes; must agree with it
		"bare":       false, // keeps its own content store, not served out of
		"incus":      false, // likewise; incusd owns the image store
	}
	for _, backend := range append([]string{""}, knownDeployBackends...) {
		exp, ok := want[backend]
		if !ok {
			t.Errorf("backend %q is accepted by validateDeployBackend but this test has no expectation for it: "+
				"decide whether it re-exports a host image store and add it here AND to isHostBackend — "+
				"leaving it unlisted means it silently gets the classic CAS registry", backend)
			continue
		}
		if got := isHostBackend(backend); got != exp {
			t.Errorf("isHostBackend(%q) = %v, want %v", backend, got, exp)
		}
	}
	// The aliases must never diverge: they name one backend.
	if isHostBackend("kubernetes") != isHostBackend("k8s") {
		t.Error(`isHostBackend disagrees between "kubernetes" and "k8s", which are the same backend`)
	}
	if isHostBackend("") != isHostBackend("dockerhost") {
		t.Error(`isHostBackend disagrees between "" and "dockerhost"; unset selects dockerhost, so they must classify alike`)
	}
}

// TestHostcheckNormalizerKnowsEveryDeployBackend enforces the invariant
// hostcheck.normalizeBackend states in a comment and nothing checked: that it is
// "mirroring the server's own selector".
//
// The vocabulary of CORNUS_DEPLOY_BACKEND values lives here, in
// knownDeployBackends. pkg/hostcheck keeps its own copy — it has to, because
// pkg/server imports pkg/hostcheck and the reverse would be an import cycle — and
// normalizeBackend maps a raw value onto it, folding "" to dockerhost and "k8s"
// to "kubernetes" the way the server's own switches do.
//
// The failure this guards is quiet and lands on the worst possible command. A
// name added HERE but not to normalizeBackend hits that function's default arm,
// which treats an unrecognized value as dockerhost — deliberately, so an unknown
// value does not skip every check. That is right for a typo and wrong for an
// alias: `cornus daemon preflight` would then probe DOCKER on a containerd or
// bare host and report a confident verdict about the wrong runtime, on the one
// command whose whole job is telling the operator whether their host can run the
// backend they configured.
//
// Parsing hostcheck's source rather than calling it keeps this a source-level
// check without exporting normalizeBackend or hoisting the vocabulary into a
// third package — both are real options for a proper fix, and both are larger
// than the guard. See .agents/docs/TODO.md, "hostcheck.normalizeBackend mirrors
// the server's backend-alias handling".
func TestHostcheckNormalizerKnowsEveryDeployBackend(t *testing.T) {
	const src = "../hostcheck/hostcheck.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v (has the file moved? this test guards normalizeBackend and must be repointed with it)", src, err)
	}

	// normalizeBackend's cases are a mix of string literals ("" and "k8s") and
	// identifiers (backendKubernetes, ...), so resolve the package's string
	// consts first. Without this the identifier arms look like no coverage at all
	// and the test would fail for every backend, which is a louder wrong answer
	// than the drift it is meant to catch.
	consts := map[string]string{}
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						consts[n.Name] = s
					}
				}
			}
		}
	}

	handled := map[string]bool{}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "normalizeBackend" {
			return true
		}
		found = true
		ast.Inspect(fn, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List { // nil List == the default arm, which is what we must NOT count
				switch v := e.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						if s, err := strconv.Unquote(v.Value); err == nil {
							handled[s] = true
						}
					}
				case *ast.Ident:
					if s, ok := consts[v.Name]; ok {
						handled[s] = true
					}
				}
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("no normalizeBackend in pkg/hostcheck; this test has lost track of the function it guards")
	}
	if len(handled) == 0 {
		t.Fatal("resolved no case values from normalizeBackend; the parse or the const resolution is broken, not the code under test")
	}

	for _, name := range knownDeployBackends {
		if !handled[name] {
			t.Errorf("knownDeployBackends accepts %q but hostcheck.normalizeBackend has no explicit case for it: "+
				"it falls to the default arm and is treated as dockerhost, so `cornus daemon preflight` checks the WRONG runtime "+
				"and reports a confident verdict about a backend the operator is not running", name)
		}
	}
	// "" is not in knownDeployBackends (it is the unset form) but must normalize
	// explicitly, since the whole point of the default arm is that reaching it
	// means "unrecognized".
	if !handled[""] {
		t.Error(`hostcheck.normalizeBackend has no explicit case for "": an unset CORNUS_DEPLOY_BACKEND would reach the default arm, ` +
			`making the common case indistinguishable from a typo`)
	}
}
