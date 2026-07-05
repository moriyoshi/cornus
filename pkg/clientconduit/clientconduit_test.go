package clientconduit

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/socks5"
)

// fakeDialer satisfies portfwd.Dialer; it is never actually dialed in these tests
// (no connection is opened), so it only needs to exist.
type fakeDialer struct{}

func (fakeDialer) PortForward(ctx context.Context, name string, port int, proto string) (net.Conn, error) {
	return nil, context.Canceled
}

func TestStartNone(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModeNone})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if b := e.Banner(); b != nil {
		t.Errorf("Banner = %v, want nil", b)
	}
	fs, err := e.Add(context.Background(), "web", []api.PortMapping{{Host: 8080, Container: 80}})
	if err != nil || fs != nil {
		t.Errorf("Add = %v, %v; want nil, nil", fs, err)
	}
}

func TestStartUnknownMode(t *testing.T) {
	if _, err := Start(context.Background(), fakeDialer{}, Config{Mode: "bogus"}); err == nil {
		t.Fatal("want error for unknown mode")
	}
}

func TestStartPortForwardAddBindsListeners(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModePortForward})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if b := e.Banner(); b != nil {
		t.Errorf("port-forward Banner = %v, want nil", b)
	}
	// Host 0 binds an ephemeral local port, so the test never fights for a port.
	fs, err := e.Add(context.Background(), "web", []api.PortMapping{{Host: 0, Container: 80}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Name != "web" || fs[0].Container != 80 || fs[0].Local == "" {
		t.Fatalf("Add forwards = %+v, want one {web 80 <local>}", fs)
	}
	// The reported local listener is actually bound (connect succeeds).
	c, err := net.Dial("tcp", fs[0].Local)
	if err != nil {
		t.Fatalf("dial forwarded listener %s: %v", fs[0].Local, err)
	}
	_ = c.Close()
}

func TestStartSocks5BannerAndProxy(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	banner := e.Banner()
	if len(banner) != 1 || !strings.Contains(banner[0], socks5.DefaultSuffix) {
		t.Fatalf("socks5 banner = %v, want one line mentioning %q", banner, socks5.DefaultSuffix)
	}
	// Add binds no listeners in socks5 mode (the proxy reaches services by name).
	fs, err := e.Add(context.Background(), "web", []api.PortMapping{{Host: 8080, Container: 80}})
	if err != nil || fs != nil {
		t.Errorf("socks5 Add = %v, %v; want nil, nil", fs, err)
	}
}

// eventually polls cond up to ~1s; it fails the test if cond never holds. Alias
// withdrawal runs in a goroutine on ctx.Done, so the assertion must wait for it.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestSocks5ConduitAliasLifecycle(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	sc, ok := e.(*socks5Conduit)
	if !ok {
		t.Fatalf("Start returned %T, want *socks5Conduit", e)
	}

	// A compose service "web" deploys as "demo-web"; Add registers the short name.
	svcCtx, cancel := context.WithCancel(context.Background())
	if _, err := e.Add(svcCtx, "demo-web", []api.PortMapping{{Host: 0, Container: 80}}, "web"); err != nil {
		t.Fatal(err)
	}
	if res, _ := sc.router.Resolve("web", 8080); res.Kind != socks5.KindService || res.Service != "demo-web" {
		t.Fatalf("bare web after Add = %+v, want demo-web", res)
	}

	// Ending the service ctx withdraws the alias.
	cancel()
	eventually(t, func() bool {
		res, _ := sc.router.Resolve("web", 8080)
		return res.Kind != socks5.KindService
	})
}

func TestSocks5ConduitCloseClearsAliases(t *testing.T) {
	e, err := Start(context.Background(), fakeDialer{}, Config{Mode: ModeSocks5, Socks5Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	sc := e.(*socks5Conduit)
	// A background ctx that never cancels: only Close should drop the alias.
	if _, err := e.Add(context.Background(), "demo-web", nil, "web"); err != nil {
		t.Fatal(err)
	}
	if res, _ := sc.router.Resolve("web", 8080); res.Kind != socks5.KindService {
		t.Fatalf("bare web after Add = %+v, want matched", res)
	}
	e.Close()
	if res, _ := sc.router.Resolve("web", 8080); res.Kind == socks5.KindService {
		t.Fatalf("bare web after Close = %+v, want unmatched (aliases cleared)", res)
	}
}

func TestSocks5BareServiceNamesToggle(t *testing.T) {
	disabled := false
	e, err := Start(context.Background(), fakeDialer{}, Config{
		Mode:                   ModeSocks5,
		Socks5Listen:           "127.0.0.1:0",
		Socks5BareServiceNames: &disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	sc := e.(*socks5Conduit)
	if _, err := e.Add(context.Background(), "demo-web", nil, "web"); err != nil {
		t.Fatal(err)
	}
	// Bare form disabled by config: direct egress.
	if res, _ := sc.router.Resolve("web", 8080); res.Kind == socks5.KindService {
		t.Errorf("bare web with toggle off = %+v, want unmatched", res)
	}
	// Suffixed form still remaps to the real deployment.
	if res, _ := sc.router.Resolve("web.cornus.internal", 8080); res.Kind != socks5.KindService || res.Service != "demo-web" {
		t.Errorf("suffixed web with toggle off = %+v, want demo-web", res)
	}
}

func TestRouterCustomRulesTakePrecedence(t *testing.T) {
	r, err := Router(Config{
		Socks5Suffix:  ".ignored.internal",
		Socks5Resolve: []socks5.Rule{{Pattern: `^(.*):5000$`, Replace: `\1:10000`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Resolve("db", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != socks5.KindService || res.Service != "db" || res.Port != 10000 {
		t.Fatalf("custom rule resolve = %+v, want db:10000", res)
	}
	// The suffix default was not used, so a suffixed host does NOT match.
	res, _ = r.Resolve("x.ignored.internal", 80)
	if res.Kind == socks5.KindService {
		t.Fatalf("suffix rule should be ignored when explicit rules are set: %+v", res)
	}
}
