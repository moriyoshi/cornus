package dockerhost

// Regression tests for the two hijacked endpoints — exec-start and attach.
//
// These exist because the engine seam (engine_iface.go) replaced a raw
// `hijack(method, path, body)` on the interface with two SEMANTIC methods, which
// moved the request construction (attach's query string, exec-start's JSON body)
// out of dockerhost.go and into engine.go. That move was made against a green
// test suite that, it turned out, covered neither endpoint at all: nothing in the
// package called Attach or ExecStart, so "all tests pass" said exactly nothing
// about whether the requests still went out correctly.
//
// The failure that would have escaped is specifically quiet. Docker reads the
// attach flags as "1"/"0" values rather than as presence, so dropping one does
// not error — the daemon substitutes its own default. For stdin that is the
// difference between an interactive session and a silently read-only one, with
// no diagnostic anywhere.
//
// So these assert the bytes on the wire. hijack hand-rolls its HTTP request, so
// a bare TCP listener sees exactly what the daemon would.

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
)

// captureHijack stands up a listener that accepts one connection, reads the
// hand-rolled request, replies 101 so the client returns cleanly, and hands the
// request back. It returns the address to point DOCKER_HOST at.
func captureHijack(t *testing.T) (addr string, got <-chan hijackRequest) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	ch := make(chan hijackRequest, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		var req hijackRequest
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		req.RequestLine = strings.TrimSpace(line)
		// Headers to the blank line, remembering Content-Length so the body can
		// be read exactly (the connection is never closed by the client side).
		n := 0
		for {
			h, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if h == "\r\n" || h == "\n" {
				break
			}
			if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "content-length") {
				for _, c := range strings.TrimSpace(v) {
					if c >= '0' && c <= '9' {
						n = n*10 + int(c-'0')
					}
				}
			}
		}
		if n > 0 {
			buf := make([]byte, n)
			if _, err := readFull(br, buf); err == nil {
				req.Body = string(buf)
			}
		}
		ch <- req
		// Complete the upgrade so the client's hijack returns without error.
		conn.Write([]byte("HTTP/1.1 101 UPGRADED\r\n\r\n"))
		// Hold the connection briefly; the client treats EOF as stream end.
		time.Sleep(50 * time.Millisecond)
	}()
	return ln.Addr().String(), ch
}

type hijackRequest struct {
	RequestLine string
	Body        string
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func newCapturingClient(t *testing.T, addr string) *engineClient {
	t.Helper()
	t.Setenv("DOCKER_HOST", "tcp://"+addr)
	c, err := newEngineClient()
	if err != nil {
		t.Fatalf("newEngineClient: %v", err)
	}
	return c
}

// TestContainerAttachSendsEveryStreamFlag pins attach's query string.
//
// Every flag is asserted with its explicit value, including the FALSE ones: the
// contract is that each is written as "0" rather than omitted, because omission
// takes the daemon's default instead of the caller's answer.
func TestContainerAttachSendsEveryStreamFlag(t *testing.T) {
	addr, got := captureHijack(t)
	c := newCapturingClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := c.containerAttach(ctx, "abc123", api.AttachConfig{
		Stream: true, Stdin: false, Stdout: true, Stderr: false, Logs: true,
	})
	if err != nil {
		t.Fatalf("containerAttach: %v", err)
	}
	defer conn.Close()

	var req hijackRequest
	select {
	case req = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the attach request")
	}

	if !strings.HasPrefix(req.RequestLine, "POST /containers/abc123/attach?") {
		t.Fatalf("request line = %q, want POST /containers/abc123/attach?...", req.RequestLine)
	}
	for _, want := range []string{
		"stream=1", "stdin=0", "stdout=1", "stderr=0", "logs=1",
	} {
		if !strings.Contains(req.RequestLine, want) {
			t.Errorf("attach query missing %q; full request line: %s", want, req.RequestLine)
		}
	}
}

// TestExecStartSendsDetachAndTty pins exec-start's path and JSON body.
//
// Detach must be false: a detached exec-start returns immediately and the caller
// bridges a stream that carries nothing, which looks like a command that
// produced no output rather than like an error.
func TestExecStartSendsDetachAndTty(t *testing.T) {
	for _, tc := range []struct {
		name string
		tty  bool
		want string
	}{
		{"tty", true, `"Tty":true`},
		{"no tty", false, `"Tty":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, got := captureHijack(t)
			c := newCapturingClient(t, addr)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := c.execStart(ctx, "exec-77", tc.tty)
			if err != nil {
				t.Fatalf("execStart: %v", err)
			}
			defer conn.Close()

			var req hijackRequest
			select {
			case req = <-got:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for the exec-start request")
			}

			if !strings.HasPrefix(req.RequestLine, "POST /exec/exec-77/start ") {
				t.Fatalf("request line = %q, want POST /exec/exec-77/start", req.RequestLine)
			}
			if !strings.Contains(req.Body, tc.want) {
				t.Errorf("body = %q, want it to contain %s", req.Body, tc.want)
			}
			if !strings.Contains(req.Body, `"Detach":false`) {
				t.Errorf("body = %q, want it to contain \"Detach\":false", req.Body)
			}
		})
	}
}
