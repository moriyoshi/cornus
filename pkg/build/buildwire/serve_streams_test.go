package buildwire

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/yamux"
	"github.com/hugelgupf/p9/p9"

	"cornus/pkg/wire"
)

// yamuxPair returns a connected (client, server) yamux session pair over a TCP
// loopback, mirroring the transport the build session runs on.
func yamuxPair(t *testing.T) (client, server *yamux.Session) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	srvCh := make(chan *yamux.Session, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			srvCh <- nil
			return
		}
		s, _ := yamux.Server(conn, testYamuxConfig())
		srvCh <- s
	}()
	cConn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err = yamux.Client(cConn, testYamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	server = <-srvCh
	if server == nil {
		t.Fatal("server session not established")
	}
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

// TestServeCallerStreamsRoutesByTag interleaves many SSH and lazy-9P streams the
// server opens on the same session and proves each reaches its correct handler.
// Before the single-dispatcher fix, two racing accept loops each grabbed streams
// of the other kind and closed them, so roughly half of each kind failed.
func TestServeCallerStreamsRoutesByTag(t *testing.T) {
	// Fake SSH agent: reads 4 bytes, replies "PONG".
	agentSock := filepath.Join(t.TempDir(), "agent.sock")
	al, err := net.Listen("unix", agentSock)
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()
	go func() {
		for {
			c, err := al.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("PONG"))
			}()
		}
	}()

	// Lazy context "data" with a marker file the server reads over 9P.
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "marker"), []byte("LAZY-OK"), 0o644); err != nil {
		t.Fatal(err)
	}

	callerSess, serverSess := yamuxPair(t)

	var reads atomic.Int64
	go serveCallerStreams(callerSess, ServeOpts{
		SSHSockets:   map[string]string{"default": agentSock},
		LazyContexts: map[string]string{"data": dataDir},
	}, nil, &reads)

	const n = 24
	var wg sync.WaitGroup
	errCh := make(chan error, 2*n)
	fail := func(format string, args ...any) { errCh <- fmt.Errorf(format, args...) }

	for i := 0; i < n; i++ {
		// SSH stream: mimic BuildKit dialing an agent tunnel.
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := serverSess.OpenStream()
			if err != nil {
				fail("ssh open: %v", err)
				return
			}
			defer stream.Close()
			if _, err := stream.Write([]byte{tagSSH}); err != nil {
				fail("ssh tag: %v", err)
				return
			}
			if _, err := io.WriteString(stream, "default\n"); err != nil {
				fail("ssh id: %v", err)
				return
			}
			if _, err := stream.Write([]byte("PING")); err != nil {
				fail("ssh ping: %v", err)
				return
			}
			resp := make([]byte, 4)
			if _, err := io.ReadFull(stream, resp); err != nil {
				fail("ssh read: %v", err)
				return
			}
			if string(resp) != "PONG" {
				fail("ssh response = %q, want PONG", resp)
			}
		}()

		// Lazy-9P stream: open a backing and read the marker over 9P.
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, err := wire.OpenBacking(serverSess, "data")
			if err != nil {
				fail("9p open: %v", err)
				return
			}
			defer stream.Close()
			cl, err := p9.NewClient(stream)
			if err != nil {
				fail("9p client: %v", err)
				return
			}
			defer cl.Close()
			root, err := cl.Attach("")
			if err != nil {
				fail("9p attach: %v", err)
				return
			}
			defer root.Close()
			_, f, err := root.Walk([]string{"marker"})
			if err != nil {
				fail("9p walk: %v", err)
				return
			}
			defer f.Close()
			if _, _, err := f.Open(p9.ReadOnly); err != nil {
				fail("9p open file: %v", err)
				return
			}
			got, err := io.ReadAll(&p9Reader{f: f})
			if err != nil {
				fail("9p read: %v", err)
				return
			}
			if string(got) != "LAZY-OK" {
				fail("9p content = %q, want LAZY-OK", got)
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if reads.Load() == 0 {
		t.Error("no bytes counted for lazy-9P backing reads")
	}
}
