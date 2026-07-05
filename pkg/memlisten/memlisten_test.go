package memlisten

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDialLocalAndAcceptRoundTrip(t *testing.T) {
	l := New("cornus.internal")
	defer l.Close()

	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		_, _ = c.Write(b)
	}()

	c, err := l.DialLocal(context.Background())
	if err != nil {
		t.Fatalf("DialLocal: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo = %q, want %q", got, "ping")
	}
}

// TestServeHTTPOverListener is the case the feature actually depends on: an
// http.Server serving on the Listener, reached only by DialLocal — no port, no
// socket file.
func TestServeHTTPOverListener(t *testing.T) {
	l := New("cornus.internal")

	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "host="+r.Host)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()

	cl := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return l.DialLocal(ctx)
		},
	}}
	resp, err := cl.Get("http://cornus.internal/hello")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// The Host the handler sees is the published name, which is what makes the
	// WebSocket Origin/Host equality check pass through the proxy.
	if string(body) != "host=cornus.internal" {
		t.Fatalf("body = %q, want %q", body, "host=cornus.internal")
	}
}

func TestAcceptUnblocksOnClose(t *testing.T) {
	l := New("x")
	errCh := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		errCh <- err
	}()
	// Give Accept a moment to park before closing.
	time.Sleep(10 * time.Millisecond)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept err = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept did not unblock on Close")
	}
}

func TestDialAfterCloseFails(t *testing.T) {
	l := New("x")
	l.Close()
	if _, err := l.DialLocal(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("DialLocal err = %v, want net.ErrClosed", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	l := New("x")
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestDialCancelledWhileParked covers a dial that arrives with no Accept waiting:
// it must honor ctx rather than park forever.
func TestDialCancelledWhileParked(t *testing.T) {
	l := New("x")
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.DialLocal(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DialLocal err = %v, want context.DeadlineExceeded", err)
	}
}

func TestAddr(t *testing.T) {
	l := New("cornus.internal")
	defer l.Close()
	if got := l.Addr().Network(); got != "mem" {
		t.Fatalf("Network() = %q, want %q", got, "mem")
	}
	if got := l.Addr().String(); got != "cornus.internal" {
		t.Fatalf("String() = %q, want %q", got, "cornus.internal")
	}
}

// TestConcurrentDials checks the listener under -race with many dialers and one
// acceptor, the shape the proxy produces when a browser opens parallel
// connections.
func TestConcurrentDials(t *testing.T) {
	l := New("x")
	defer l.Close()

	const n = 32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := l.DialLocal(ctx)
			if err != nil {
				done <- err
				return
			}
			defer c.Close()
			if _, err := c.Write([]byte("hi")); err != nil {
				done <- err
				return
			}
			b := make([]byte, 2)
			_, err = io.ReadFull(c, b)
			done <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
	}
}
