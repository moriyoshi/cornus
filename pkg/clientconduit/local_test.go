package clientconduit

import (
	"context"
	"net"
	"testing"

	"cornus/pkg/socks5"
)

// fakeLocal is a stand-in for an in-process listener (pkg/memlisten in
// production); AddLocal only stores it, so it never has to dial here.
type fakeLocal struct{}

func (fakeLocal) DialLocal(context.Context) (net.Conn, error) { return nil, nil }

func TestSocks5ConduitAddLocalLifecycle(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	sc, ok := e.(*socks5Conduit)
	if !ok {
		t.Fatalf("Start returned %T, want *socks5Conduit", e)
	}

	pubCtx, cancel := context.WithCancel(context.Background())
	published, err := e.AddLocal(pubCtx, "cornus.internal", 80, fakeLocal{})
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("AddLocal on a socks5 conduit reported not published")
	}
	if res, _ := sc.router.Resolve("cornus.internal", 80); res.Kind != socks5.KindLocal {
		t.Fatalf("cornus.internal:80 after AddLocal = %+v, want KindLocal", res)
	}

	// Ending the publishing ctx withdraws the name — pure session state, exactly
	// like an alias.
	cancel()
	eventually(t, func() bool {
		res, _ := sc.router.Resolve("cornus.internal", 80)
		return res.Kind != socks5.KindLocal
	})
}

func TestSocks5ConduitCloseClearsLocals(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	sc := e.(*socks5Conduit)
	// A background ctx that never cancels: only Close should drop the name.
	if _, err := e.AddLocal(context.Background(), "cornus.internal", 80, fakeLocal{}); err != nil {
		t.Fatal(err)
	}
	if res, _ := sc.router.Resolve("cornus.internal", 80); res.Kind != socks5.KindLocal {
		t.Fatalf("after AddLocal = %+v, want KindLocal", res)
	}
	e.Close()
	if res, _ := sc.router.Resolve("cornus.internal", 80); res.Kind == socks5.KindLocal {
		t.Fatalf("after Close = %+v, want the published name cleared", res)
	}
}

// TestAddLocalNoopModes locks the contract the agent depends on to report a clear
// error: only SOCKS5 resolves names, so the other modes must publish nothing and
// say so rather than promise a name that will never resolve.
func TestAddLocalNoopModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"port-forward", Config{Mode: ModePortForward}},
		{"none", Config{Mode: ModeNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Start(context.Background(), fakeDialer{}, tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			published, err := e.AddLocal(context.Background(), "cornus.internal", 80, fakeLocal{})
			if err != nil {
				t.Fatalf("AddLocal: %v", err)
			}
			if published {
				t.Fatalf("%s conduit reported publishing a name; it resolves none", tc.name)
			}
		})
	}
}

func TestSocks5ConduitAddLocalRejectsInvalid(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	for _, tc := range []struct {
		name string
		host string
		port int
		d    socks5.LocalDialer
	}{
		{"empty host", "", 80, fakeLocal{}},
		{"zero port", "cornus.internal", 0, fakeLocal{}},
		{"port out of range", "cornus.internal", 70000, fakeLocal{}},
		{"nil dialer", "cornus.internal", 80, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			published, err := e.AddLocal(context.Background(), tc.host, tc.port, tc.d)
			if err == nil {
				t.Fatal("want an error")
			}
			if published {
				t.Fatal("want published=false")
			}
		})
	}
}
