package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/wire"
)

// TestLogStreamHandlerErr covers the streaming/tunnel error logger: a genuine
// backend failure (e.g. RBAC denied) is logged at WARN so an operator can see why
// a silently-closing tunnel or committed-200 stream failed, while a client-side
// teardown (cancelled request context) is demoted to DEBUG so a routine Ctrl-C on
// a --follow or a closed port-forward does not spam warnings.
func TestLogStreamHandlerErr(t *testing.T) {
	capture := func(r *http.Request, err error) string {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		defer slog.SetDefault(prev)
		logStreamHandlerErr(r, "port-forward", "web", err)
		return buf.String()
	}

	// A real failure on a live request -> WARN with the error text.
	live := httptest.NewRequest(http.MethodGet, "/.cornus/v1/deploy/web/portforward", nil)
	got := capture(live, errors.New("forbidden: cannot create resource pods/portforward"))
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "pods/portforward") {
		t.Fatalf("real failure should log WARN with the error; got %q", got)
	}

	// A cancelled request context (client went away) -> DEBUG, never WARN.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := httptest.NewRequest(http.MethodGet, "/.cornus/v1/deploy/web/portforward", nil).WithContext(ctx)
	got = capture(cancelled, errors.New("stream closed"))
	if strings.Contains(got, "level=WARN") {
		t.Fatalf("client disconnect must not log WARN; got %q", got)
	}
	if !strings.Contains(got, "level=DEBUG") {
		t.Fatalf("client disconnect should log DEBUG; got %q", got)
	}

	// A nil error logs nothing.
	if got := capture(live, nil); got != "" {
		t.Fatalf("nil error should log nothing; got %q", got)
	}
}

// wsURL converts an httptest server URL to its ws:// form.
func wsURL(httpURL string) string {
	return "ws://" + strings.TrimPrefix(httpURL, "http://")
}

// TestExecCreateAndInspect covers the REST half of exec: create returns the
// backend exec id, inspect renders the canned state.
func TestExecCreateAndInspect(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	body, _ := json.Marshal(api.ExecConfig{Cmd: []string{"sh"}})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/exec", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	var cr struct {
		Id string `json:"Id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if cr.Id != "exec-web" {
		t.Fatalf("exec create id = %q", cr.Id)
	}

	resp, err = http.Get(srv.URL + "/.cornus/v1/deploy/exec/exec-web/json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st api.ExecState
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if !st.Running || st.Pid != 4242 {
		t.Fatalf("exec inspect = %+v", st)
	}
}

// TestExecStartWS dials the exec-start WebSocket, sends an ExecStartConfig
// preamble then a payload, and asserts the echo (the fake backend's ExecStart
// echoes conn) — proving the preamble + raw tunnel wiring.
func TestExecStartWS(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	conn, err := wire.DialConn(context.Background(), wsURL(srv.URL)+"/.cornus/v1/deploy/exec/exec-web/start")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pre, _ := json.Marshal(api.ExecStartConfig{Tty: true})
	if _, err := conn.Write(append(pre, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "ping-exec"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("ping-exec"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "ping-exec" {
		t.Fatalf("exec echo = %q", got)
	}
}

// TestExecStartOutputWithoutStdin models a non-interactive `docker exec` over the
// WS tunnel: the client sends the preamble but NO stdin, and the backend emits
// output independent of stdin. The full output must arrive (the preamble path
// must not swallow it, and no stdin EOF may truncate it).
func TestExecStartOutputWithoutStdin(t *testing.T) {
	fb := &fakeBackend{execOutput: "EXEC_MARKER"}
	srv := newTestServer(t, fb)
	defer srv.Close()

	conn, err := wire.DialConn(context.Background(), wsURL(srv.URL)+"/.cornus/v1/deploy/exec/exec-web/start")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pre, _ := json.Marshal(api.ExecStartConfig{})
	if _, err := conn.Write(append(pre, '\n')); err != nil {
		t.Fatal(err)
	}
	// Send no stdin at all, then read the output.
	got, _ := io.ReadAll(conn)
	if !strings.Contains(string(got), "EXEC_MARKER") {
		t.Fatalf("exec output without stdin = %q, want it to contain EXEC_MARKER", got)
	}
}

// TestAttachWS dials the attach WebSocket, sends an AttachConfig preamble then a
// payload, and asserts the echo (the fake backend's Attach echoes conn).
func TestAttachWS(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	conn, err := wire.DialConn(context.Background(), wsURL(srv.URL)+"/.cornus/v1/deploy/web/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pre, _ := json.Marshal(api.AttachConfig{Stream: true, Stdin: true, Stdout: true})
	if _, err := conn.Write(append(pre, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "ping-attach"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("ping-attach"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "ping-attach" {
		t.Fatalf("attach echo = %q", got)
	}
}

// TestPortForwardWS dials the port-forward WebSocket, sends a PortForwardConfig
// preamble then a payload, and asserts both that the backend decoded the target
// port/protocol and that the raw tunnel echoes (the fake backend's ForwardPort
// echoes conn) — proving the preamble + bridge wiring end to end.
func TestPortForwardWS(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	conn, err := wire.DialConn(context.Background(), wsURL(srv.URL)+"/.cornus/v1/deploy/web/portforward")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pre, _ := json.Marshal(api.PortForwardConfig{Port: 5432, Protocol: "tcp"})
	if _, err := conn.Write(append(pre, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "ping-pf"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("ping-pf"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(got) != "ping-pf" {
		t.Fatalf("port-forward echo = %q", got)
	}

	fb.mu.Lock()
	port, proto := fb.fwdPort, fb.fwdProto
	fb.mu.Unlock()
	if port != 5432 || proto != "tcp" {
		t.Fatalf("ForwardPort got port=%d proto=%q, want 5432/tcp", port, proto)
	}
}

// udpFakeBackend is a fakeBackend that advertises the optional UDP port-forward
// capability the handler probes for.
type udpFakeBackend struct{ *fakeBackend }

func (u *udpFakeBackend) SupportsUDPPortForward() bool { return true }

// readAckLine reads one newline-terminated ack off conn byte-by-byte (so no
// stream bytes past the newline are consumed) and unmarshals it.
func readAckLine(t *testing.T, conn io.Reader) api.PortForwardAck {
	t.Helper()
	var line []byte
	buf := make([]byte, 1)
	for {
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("read ack: %v", err)
		}
		if buf[0] == '\n' {
			break
		}
		line = append(line, buf[0])
	}
	var ack api.PortForwardAck
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("unmarshal ack %q: %v", line, err)
	}
	return ack
}

// TestPortForwardUDPAckAndFrames dials the port-forward WebSocket with a udp
// preamble against a UDP-capable backend: the server must ack ok before any
// frames, pass proto "udp" through to the backend, and bridge the framed
// datagram stream (the fake echoes bytes, which echoes frames intact).
func TestPortForwardUDPAckAndFrames(t *testing.T) {
	fb := &udpFakeBackend{&fakeBackend{}}
	srv := newTestServer(t, fb)
	defer srv.Close()

	conn, err := wire.DialConn(context.Background(), wsURL(srv.URL)+"/.cornus/v1/deploy/web/portforward")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pre, _ := json.Marshal(api.PortForwardConfig{Port: 53, Protocol: "udp"})
	if _, err := conn.Write(append(pre, '\n')); err != nil {
		t.Fatal(err)
	}
	if ack := readAckLine(t, conn); ack.Error != "" {
		t.Fatalf("ack error = %q, want ok", ack.Error)
	}
	if err := wire.WriteDatagram(conn, []byte("ping-udp")); err != nil {
		t.Fatal(err)
	}
	got, err := wire.ReadDatagram(conn)
	if err != nil {
		t.Fatalf("read echoed datagram: %v", err)
	}
	if string(got) != "ping-udp" {
		t.Fatalf("udp port-forward echo = %q", got)
	}

	fb.mu.Lock()
	port, proto := fb.fwdPort, fb.fwdProto
	fb.mu.Unlock()
	if port != 53 || proto != "udp" {
		t.Fatalf("ForwardPort got port=%d proto=%q, want 53/udp", port, proto)
	}
}

// TestPortForwardUDPRejectedByBackend asserts the kubernetes-shaped path: a udp
// preamble against a backend without the UDP capability gets an error ack and
// the tunnel closes without ForwardPort ever being called.
func TestPortForwardUDPRejectedByBackend(t *testing.T) {
	fb := &fakeBackend{}
	srv := newTestServer(t, fb)
	defer srv.Close()

	conn, err := wire.DialConn(context.Background(), wsURL(srv.URL)+"/.cornus/v1/deploy/web/portforward")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pre, _ := json.Marshal(api.PortForwardConfig{Port: 53, Protocol: "udp"})
	if _, err := conn.Write(append(pre, '\n')); err != nil {
		t.Fatal(err)
	}
	ack := readAckLine(t, conn)
	if !strings.Contains(ack.Error, "not supported") || !strings.Contains(ack.Error, "TCP-only") {
		t.Fatalf("ack error = %q, want an UDP-unsupported rejection", ack.Error)
	}
	// The tunnel must close without bridging.
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("tunnel still open after rejected udp preamble")
	}
	fb.mu.Lock()
	proto := fb.fwdProto
	fb.mu.Unlock()
	if proto != "" {
		t.Fatalf("ForwardPort was called (proto %q) despite the rejection", proto)
	}
}

// TestClientPortForwardUDP exercises pkg/client's udp ack handling against the
// real handler: a capable backend hands back a live framed tunnel; an incapable
// one fails the dial with the ack's rejection.
func TestClientPortForwardUDP(t *testing.T) {
	fb := &udpFakeBackend{&fakeBackend{}}
	srv := newTestServer(t, fb)
	defer srv.Close()

	c := client.New(srv.URL)
	conn, err := c.PortForward(context.Background(), "web", 53, "udp")
	if err != nil {
		t.Fatalf("PortForward udp: %v", err)
	}
	defer conn.Close()
	if err := wire.WriteDatagram(conn, []byte("via-client")); err != nil {
		t.Fatal(err)
	}
	got, err := wire.ReadDatagram(conn)
	if err != nil || string(got) != "via-client" {
		t.Fatalf("client udp echo = %q, %v", got, err)
	}

	rejSrv := newTestServer(t, &fakeBackend{})
	defer rejSrv.Close()
	if _, err := client.New(rejSrv.URL).PortForward(context.Background(), "web", 53, "udp"); err == nil ||
		!strings.Contains(err.Error(), "not supported") {
		t.Fatalf("PortForward udp against a TCP-only backend = %v, want the ack rejection", err)
	}
}
