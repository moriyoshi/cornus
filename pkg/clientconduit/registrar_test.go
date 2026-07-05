package clientconduit

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"cornus/pkg/conduithost"
	"cornus/pkg/portfwd"
	"cornus/pkg/socks5"
)

type stubDialer struct{ name string }

func (stubDialer) PortForward(context.Context, string, int, string) (net.Conn, error) {
	return nil, nil
}

type stubLocal struct{ upstream string }

func (stubLocal) DialLocal(context.Context) (net.Conn, error) { return nil, nil }

func newRegistrar(t *testing.T) (*Registrar, *socks5.Router) {
	t.Helper()
	router, err := socks5.NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	return &Registrar{
		Router: router,
		Dialer: func(p conduithost.Peer) portfwd.Dialer { return stubDialer{name: "peer"} },
		LocalDial: func(upstream string) socks5.LocalDialer {
			if upstream == "" {
				return nil
			}
			return stubLocal{upstream: upstream}
		},
	}, router
}

func alias(t *testing.T, label, dep string, seq uint64) conduithost.Registration {
	t.Helper()
	b, err := json.Marshal(AliasPayload{Label: label, Deployment: dep})
	if err != nil {
		t.Fatal(err)
	}
	return conduithost.Registration{Kind: KindAlias, Payload: b, Seq: seq}
}

// The bridge's whole job: an opaque payload from the control socket becomes a claim
// the router resolves.
func TestRegistrarAppliesAndWithdrawsAnAlias(t *testing.T) {
	r, router := newRegistrar(t)
	withdraw, err := r.Register(context.Background(), alias(t, "web", "demo-web", 0))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := router.Resolve("web.cornus.internal", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "demo-web" {
		t.Errorf("resolved to %q, want demo-web", got.Service)
	}
	if got.Dialer == nil {
		t.Error("the claim carries no dialer, so a consolidated conduit would tunnel it to the wrong server")
	}
	withdraw()
	if got, _ := router.Resolve("web.cornus.internal", 8080); got.Service != "web" {
		t.Errorf("after withdrawal the alias still resolves (%q)", got.Service)
	}
}

// The sequence must reach the router verbatim. Dropping it would renumber replayed
// claims by arrival order, so a contested short name would resolve differently after
// every takeover.
func TestRegistrarCarriesTheSequenceThrough(t *testing.T) {
	r, router := newRegistrar(t)
	// Applied in the REVERSE of their precedence, as a takeover replay does.
	if _, err := r.Register(context.Background(), alias(t, "web", "shop-web", 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(context.Background(), alias(t, "web", "demo-web", 1)); err != nil {
		t.Fatal(err)
	}
	got, err := router.Resolve("web.cornus.internal", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "shop-web" {
		t.Errorf("resolved to %q, want shop-web — the higher sequence must win regardless of arrival order", got.Service)
	}
}

// A published name is withdrawn by HANDLE. With two publishers on one subject, a
// key-based withdrawal would remove whichever claim is serving — somebody else's.
func TestRegistrarWithdrawsExactlyItsOwnPublishedName(t *testing.T) {
	r, router := newRegistrar(t)
	ctx := context.Background()

	first, err := json.Marshal(LocalPayload{Host: "app.test", Port: 80, Upstream: "/tmp/first.sock"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(LocalPayload{Host: "app.test", Port: 80, Upstream: "/tmp/second.sock"})
	if err != nil {
		t.Fatal(err)
	}
	withdrawFirst, err := r.Register(ctx, conduithost.Registration{Kind: KindLocal, Payload: first, Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(ctx, conduithost.Registration{Kind: KindLocal, Payload: second, Seq: 2}); err != nil {
		t.Fatal(err)
	}

	withdrawFirst()
	got, err := router.Resolve("app.test", 80)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != socks5.KindLocal {
		t.Fatalf("after the FIRST publisher withdrew, app.test resolves %v; its teardown took down the live one", got.Kind)
	}
	if got.Local.(stubLocal).upstream != "/tmp/second.sock" {
		t.Errorf("app.test serves %q, want the second publisher's upstream", got.Local.(stubLocal).upstream)
	}
}

// An unknown kind must be refused, not misread. A host meeting a registration it
// does not understand has to say so.
func TestRegistrarRefusesWhatItCannotApply(t *testing.T) {
	r, _ := newRegistrar(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		reg  conduithost.Registration
		want string
	}{
		{"unknown kind", conduithost.Registration{Kind: "mystery", Payload: json.RawMessage(`{}`)}, "unknown registration kind"},
		{"malformed payload", conduithost.Registration{Kind: KindAlias, Payload: json.RawMessage(`{`)}, "malformed"},
		{"incomplete alias", conduithost.Registration{Kind: KindAlias, Payload: json.RawMessage(`{"label":"web"}`)}, "label and a deployment"},
		{"bad port", conduithost.Registration{Kind: KindLocal, Payload: json.RawMessage(`{"host":"a","port":0}`)}, "port in 1-65535"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Register(ctx, tc.reg); err == nil {
				t.Fatal("accepted a registration it cannot apply")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the problem (want it to mention %q)", err, tc.want)
			}
		})
	}
}

// A host with no way to reach another process's sockets must refuse a published
// name rather than pretend it published one.
func TestRegistrarRefusesAPublishedNameItCannotServe(t *testing.T) {
	router, err := socks5.NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	r := &Registrar{Router: router} // no LocalDial
	b, err := json.Marshal(LocalPayload{Host: "app.test", Port: 80, Upstream: "/tmp/x.sock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(context.Background(), conduithost.Registration{Kind: KindLocal, Payload: b}); err == nil {
		t.Fatal("published a name it has no way to serve")
	}
}

// The recovery capability must reach the router, or a takeover answers unrestored
// names as unknown instead of waiting for them.
func TestRegistrarBeginsRecoveryOnTheRouter(t *testing.T) {
	r, router := newRegistrar(t)
	r.BeginRecovery(time.Now().Add(5 * time.Second))

	got, err := router.Resolve("web", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != socks5.KindPending {
		t.Errorf("an unclaimed name during recovery resolved %v, want KindPending", got.Kind)
	}
}

// A name moving between workloads must be REPORTED. Several projects may share a
// conduit and claim the same short name; the latest wins, and withdrawing it hands
// the name back to whoever held it before. Both are otherwise silent — a client
// keeps using the name and reaches a different workload, with no error anywhere —
// and the registrar is the only place that sees both claims.
func TestRegistrarReportsANameMoving(t *testing.T) {
	router, err := socks5.NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var said []string
	r := &Registrar{
		Router: router,
		Logf: func(format string, args ...any) {
			mu.Lock()
			said = append(said, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	}
	lines := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), said...)
	}

	ctx := context.Background()
	if _, err := r.Register(ctx, alias(t, "web", "demo-web", 0)); err != nil {
		t.Fatal(err)
	}
	// A second project takes the name.
	withdrawShop, err := r.Register(ctx, alias(t, "web", "shop-web", 0))
	if err != nil {
		t.Fatal(err)
	}
	got := lines()
	if len(got) != 2 || !strings.Contains(got[1], "shop-web") {
		t.Fatalf("claims reported %v, want the second to name shop-web", got)
	}

	// And giving it back is the case that matters: nothing else would reveal that a
	// name in use now reaches somewhere else.
	withdrawShop()
	got = lines()
	if len(got) != 3 {
		t.Fatalf("reported %v, want the handback reported too", got)
	}
	if !strings.Contains(got[2], "demo-web") || !strings.Contains(got[2], "shop-web left") {
		t.Errorf("handback reported as %q, want it to name both the new target and who left", got[2])
	}
}

// During recovery a claim arriving is a name being RESTORED after a takeover, not a
// name moving. Narrating each one would bury the case that matters under a burst
// after every handover.
func TestRegistrarStaysQuietWhileRecovering(t *testing.T) {
	router, err := socks5.NewSuffixRouter("")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var said []string
	r := &Registrar{
		Router: router,
		Logf: func(format string, args ...any) {
			mu.Lock()
			said = append(said, fmt.Sprintf(format, args...))
			mu.Unlock()
		},
	}
	r.BeginRecovery(time.Now().Add(10 * time.Second))

	ctx := context.Background()
	if _, err := r.Register(ctx, alias(t, "web", "demo-web", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(ctx, alias(t, "web", "shop-web", 2)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(said)
	mu.Unlock()
	if n != 0 {
		t.Errorf("reported %d movements while recovering, want silence: %v", n, said)
	}

	// Once the window closes, movements are reported again.
	r.BeginRecovery(time.Time{})
	if _, err := r.Register(ctx, alias(t, "web", "later-web", 3)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(said) != 1 {
		t.Errorf("after recovery ended, reported %v, want the movement", said)
	}
}
