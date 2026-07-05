package deploy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEveryUDPForwardingBackendDeclaresIt is the regression test for a
// capability that existed and was unreachable.
//
// UDP port-forward is an OPTIONAL capability: pkg/server type-asserts a Backend
// to its udpPortForwarder interface and, on failure, refuses the tunnel with
// "UDP port-forward is not supported by the X backend". That default is the only
// sane one — but it means a backend whose ForwardPort implements datagram
// bridging and merely forgets to declare SupportsUDPPortForward is
// INDISTINGUISHABLE, to every caller and to the compiler, from one that cannot
// do it at all.
//
// incushost was in that state. forwardport_linux.go branches on proto == "udp",
// dials the instance over UDP, and bridges datagrams — on the companion path
// too, with a careful comment about which bridging helper is correct there — and
// never declared the method. So every `cornus port-forward 5353:53/udp` against
// incus was refused, naming a limitation the backend does not have, while
// docs/reference/deploy-backends.md documented it as working "for both TCP and
// UDP". Nothing failed: not the build, not vet, not a test, not a doc check.
//
// The invariant asserted here is the one that was violated: if a backend's
// port-forward machinery has a udp branch, it must say so.
//
// This test observes the method's NAME and nothing more, which is not enough on
// its own: changing incushost's body to `return false` restores the original
// defect in full and leaves this green. TestUDPForwardCapabilityValues
// (udpcapability_linux_test.go) is the other half and calls the method. Neither
// subsumes the other — this one sees a backend that grows a udp branch and never
// declares the capability (there is no method for the other to call), and that
// one sees a declaration that lies. Do not delete either as redundant.
//
// It is checked over
// the SOURCE because the alternative is no check at all — three of these five
// packages are //go:build linux and every one of them needs a live daemon
// (docker, containerd, incusd, a cluster) to construct a Backend, so there is no
// process in which all five can be type-asserted at once. go/parser ignores
// build constraints, which is exactly why it can see all five here.
func TestEveryUDPForwardingBackendDeclaresIt(t *testing.T) {
	// The expected answer per backend is written down rather than derived, so
	// that a backend LOSING its udp branch is also a failure worth reading: the
	// table says what the reference documents, and the source has to match it.
	backends := []struct {
		dir string
		// forwardsUDP is what the docs and the wire contract say this backend
		// does. kubernetes is the one genuine no: the pods/portforward subresource
		// carries a TCP stream and there is nothing to frame datagrams over.
		forwardsUDP bool
	}{
		{"dockerhost", true},
		{"containerdhost", true},
		{"barehost", true},
		{"incushost", true},
		{"kubernetes", false},
	}
	for _, b := range backends {
		t.Run(b.dir, func(t *testing.T) {
			branches, declares, sawForwardPort := scanUDPForward(t, b.dir)
			if !sawForwardPort {
				t.Fatalf("no ForwardPort in pkg/deploy/%s; this test has lost track of what it guards", b.dir)
			}
			if branches != b.forwardsUDP {
				t.Fatalf("pkg/deploy/%s port-forward code branches on proto == \"udp\" = %v, want %v: "+
					"the table here records what the reference documents, so fix whichever is wrong "+
					"rather than the table alone", b.dir, branches, b.forwardsUDP)
			}
			if branches && !declares {
				t.Errorf("pkg/deploy/%s forwards udp but does not declare SupportsUDPPortForward, so the server "+
					"type-assertion fails and every udp tunnel is refused with \"not supported by the %s backend\" — "+
					"a capability this backend HAS. Add the method.", b.dir, b.dir)
			}
			if !branches && declares {
				t.Errorf("pkg/deploy/%s declares SupportsUDPPortForward but its port-forward code never branches on "+
					"udp, so the server will ack a tunnel the backend then cannot serve — worse than the refusal, "+
					"because the client has already started sending datagrams.", b.dir)
			}
		})
	}
}

// scanUDPForward parses one backend package and reports whether its port-forward
// functions branch on proto == "udp", whether it declares
// SupportsUDPPortForward, and whether a ForwardPort exists at all.
//
// The udp scan is scoped to functions whose name mentions ForwardPort (that
// covers ForwardPort itself and the forwardPortViaCompanion helpers) and to
// comparisons whose operand is the identifier `proto`. Both narrowings matter:
// pkg/deploy/kubernetes compares a DIFFERENT variable to "udp" when mapping a
// Compose port protocol onto corev1.ProtocolUDP, and a package-wide grep for the
// literal would read that as port-forward support.
func scanUDPForward(t *testing.T, dir string) (branches, declares, sawForwardPort bool) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filepath.Clean(dir), func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse pkg/deploy/%s: %v", dir, err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if fn.Name.Name == "SupportsUDPPortForward" {
					declares = true
				}
				if fn.Name.Name == "ForwardPort" {
					sawForwardPort = true
				}
				if !strings.Contains(strings.ToLower(fn.Name.Name), "forwardport") {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					bin, ok := n.(*ast.BinaryExpr)
					if !ok || bin.Op != token.EQL {
						return true
					}
					if !isIdent(bin.X, "proto") && !isIdent(bin.Y, "proto") {
						return true
					}
					for _, side := range []ast.Expr{bin.X, bin.Y} {
						lit, ok := side.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						if v, err := strconv.Unquote(lit.Value); err == nil && v == "udp" {
							branches = true
						}
					}
					return true
				})
			}
		}
	}
	return branches, declares, sawForwardPort
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
