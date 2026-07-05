package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"cornus/pkg/config"
)

// TestHubForwardAddrDerivation pins the inter-replica forward address every hub
// store stamps on its registrations: the address peers dial to reach THIS
// replica. An explicit CORNUS_HUB_FORWARD_URL wins; otherwise it is derived from
// the downward-API POD_IP and the server's own listen port; with neither it is
// empty, which leaves remote delivery unavailable to this replica rather than
// advertising an address nothing answers on.
func TestHubForwardAddrDerivation(t *testing.T) {
	cases := []struct {
		name       string
		forwardURL string
		podIP      string
		httpAddr   string
		want       string
	}{
		{"explicit url wins over POD_IP", "ws://peer.example:9000", "10.1.2.3", ":5000", "ws://peer.example:9000"},
		{"explicit url is trimmed", " ws://peer.example:9000\n", "", ":5000", "ws://peer.example:9000"},
		{"derived from POD_IP and listen port", "", "10.1.2.3", ":8080", "ws://10.1.2.3:8080"},
		{"derived port defaults to 5000 when unparseable", "", "10.1.2.3", "garbage", "ws://10.1.2.3:5000"},
		{"IPv6 POD_IP is bracketed", "", "fd00::5", ":8080", "ws://[fd00::5]:8080"},
		{"neither set is empty, not a half-formed url", "", "", ":5000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CORNUS_HUB_FORWARD_URL", tc.forwardURL)
			t.Setenv("POD_IP", tc.podIP)
			if got := hubForwardAddr(config.Config{HTTPAddr: tc.httpAddr}); got != tc.want {
				t.Errorf("hubForwardAddr() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBothHubStoresDeriveTheForwardAddrIdentically is the regression test for a
// live asymmetry found by reviewing this package's own diff.
//
// newHubStore has two arms. The kube arm passed hubForwardAddr(cfg); the Redis
// arm read CORNUS_HUB_FORWARD_URL directly and so skipped the ws://$POD_IP
// fallback. Both stores stamp the value on every registration as the address
// peers dial to forward a delivery here (RedisStore.ForwardAddr, and kubehub's
// equivalent disposition), so the effect was that a Redis-backed multi-replica
// deployment which did not set CORNUS_HUB_FORWARD_URL registered an EMPTY
// forward address. Peers then could not reach this replica at all — silently, no
// error on any path, with POD_IP sitting unused in the environment. The symptom
// is a hub name that resolves for some callers and not others depending on which
// replica owns it.
//
// This is asserted over the SOURCE rather than by calling newHubStore, because
// its Redis arm dials and pings a real Redis before returning, so the arm that
// actually had the bug is the one a unit test cannot reach. Parsing is what makes
// the check possible at all here — the alternative is no coverage on the arm that
// broke.
func TestBothHubStoresDeriveTheForwardAddrIdentically(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	var found bool
	var viaAddr, viaBareURL int
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newHubStore" {
			return true
		}
		found = true
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			switch id.Name {
			case "hubForwardAddr":
				viaAddr++
			case "hubForwardURL":
				viaBareURL++
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("no newHubStore in server.go; this test has lost track of the function it guards")
	}
	if viaAddr < 2 {
		t.Errorf("newHubStore derives the forward address via hubForwardAddr only %d time(s), want one per store arm: "+
			"an arm that resolves it some other way advertises a different address than its sibling, and a replica that "+
			"advertises nothing is unreachable to peers with no error anywhere", viaAddr)
	}
	if viaBareURL != 0 {
		t.Errorf("newHubStore calls hubForwardURL directly (%d time(s)): that is the raw env accessor and skips the "+
			"ws://$POD_IP fallback, which is exactly how the Redis arm came to register an empty forward address. "+
			"Route it through hubForwardAddr so both arms agree.", viaBareURL)
	}
}
