package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"cornus/pkg/authtoken"
	"cornus/pkg/hub"
	"cornus/pkg/wire"
)

// TestHubJWTIdentityIsAuthoritative confirms the identity fold: when a hub spoke
// authenticates with a bearer JWT, its `sub` is the authoritative hub identity and
// overrides whatever it declares on the control stream — the same guarantee mTLS
// already gave (TestHubMTLSIdentityIsAuthoritative), now for JWT auth. Policy allows
// only "web" -> "echo".
func TestHubJWTIdentityIsAuthoritative(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("CORNUS_JWT_HS256_SECRET", secret)
	t.Setenv("CORNUS_HUB_POLICY", `{"web":["echo"]}`)

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()

	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/.cornus/v1/caretaker/attach"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mint := func(sub string) string {
		tok, err := authtoken.Issue(authtoken.IssueOptions{Subject: sub, Scope: authtoken.ScopeAPI, TTL: time.Hour, HS256Secret: []byte(secret)})
		if err != nil {
			t.Fatalf("issue %s: %v", sub, err)
		}
		return tok
	}
	dial := func(sub string) *yamux.Session {
		hdr := http.Header{"Authorization": {"Bearer " + mint(sub)}}
		sess, err := wire.DialControlHeader(ctx, wsURL, nil, hdr)
		if err != nil {
			t.Fatalf("%s dial: %v", sub, err)
		}
		return sess
	}

	// Destination (sub "dest") registers echo dial-direct.
	dst := dial("dest")
	defer dst.Close()
	dreg, err := hub.Register(dst, hub.Registration{Services: []hub.Service{{Name: "echo", Addr: echo.Addr().String()}}})
	if err != nil {
		t.Fatalf("dest register: %v", err)
	}
	defer dreg.Close()

	reach := func(sub, declared string) string {
		sess := dial(sub)
		defer sess.Close()
		creg, err := hub.Register(sess, hub.Registration{Identity: declared})
		if err != nil {
			t.Fatalf("%s register: %v", sub, err)
		}
		defer creg.Close()

		var got string
		for i := 0; i < 100; i++ {
			stream, err := hub.OpenTo(sess, "echo")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			_, _ = stream.Write([]byte("ping\n"))
			_ = stream.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
			buf := make([]byte, 5)
			n, _ := io.ReadFull(stream, buf)
			stream.Close()
			if n == 5 {
				got = string(buf)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		return got
	}

	// JWT sub "web" wins over the declared "denied" -> allowed.
	if got := reach("web", "denied"); got != "ping\n" {
		t.Errorf("sub=web declared=denied: got %q, want echo (JWT sub should win)", got)
	}
	// JWT sub "intruder" wins over the declared "web" -> denied.
	if got := reach("intruder", "web"); got != "" {
		t.Errorf("sub=intruder declared=web: got %q, want no echo (JWT sub should win)", got)
	}
}
