package dockerhost

// HTTP plumbing for the libpod engine: request helpers, error decoding, and the
// hijack used by exec-start and attach.
//
// Kept separate from engineClient's equivalents rather than shared, because the
// two differ in exactly the places that matter. libpod reports errors as a typed
// JSON object rather than Docker's `{"message":...}`, and every libpod route
// carries a version segment that Docker's does not — see podman_engine.go.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// libpodError is the uniform error body libpod returns. Measured on 5.8.2:
//
//	{"cause":"network is already connected","message":"container ... is already
//	 connected to network withdns: network is already connected","response":500}
//
// `cause` is the short, stable discriminator; `message` embeds ids and names and
// is the one to show a human. Both matter — see networkJoin, which has to tell an
// already-connected 500 from a genuine one and can only do it by cause.
type libpodError struct {
	Cause    string `json:"cause"`
	Message  string `json:"message"`
	Response int    `json:"response"`
}

// podmanAPIError carries a decoded libpod failure.
type podmanAPIError struct {
	Status string
	Cause  string
	Detail string
}

func (e *podmanAPIError) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Cause
	}
	if msg == "" {
		return fmt.Sprintf("podman api: %s", e.Status)
	}
	return fmt.Sprintf("podman api: %s: %s", e.Status, msg)
}

// causeIs reports whether a failed call carried the given libpod `cause`.
//
// It exists for the one place a status code is not enough: connecting a RUNNING
// container to a network it is already on returns 500 on libpod (403 on compat,
// 409 on real Docker), which is otherwise indistinguishable from a genuine
// internal error. Measured on Podman 5.8.2.
func causeIs(err error, cause string) bool {
	var apiErr *podmanAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.Cause), strings.ToLower(cause)) ||
		strings.Contains(strings.ToLower(apiErr.Detail), strings.ToLower(cause))
}

// do issues a libpod request against a version-prefixed path.
func (e *podmanEngine) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	return e.doWithHeaders(ctx, method, path, body, nil)
}

func (e *podmanEngine) doWithHeaders(ctx context.Context, method, path string, body any, headers http.Header) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.url(path), rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return e.http.Do(req)
}

// doRaw issues a request with a raw (already-encoded) body, for the archive PUT
// which streams a tar rather than JSON.
func (e *podmanEngine) doRaw(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.url(path), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return e.http.Do(req)
}

// expect closes over the non-2xx handling: it decodes libpod's error object so
// the cause survives, and consumes the body so the connection can be reused.
func expect(resp *http.Response, okCodes ...int) error {
	for _, code := range okCodes {
		if resp.StatusCode == code {
			return nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	apiErr := &podmanAPIError{Status: resp.Status}
	var le libpodError
	if err := json.Unmarshal(b, &le); err == nil && (le.Cause != "" || le.Message != "") {
		apiErr.Cause, apiErr.Detail = le.Cause, le.Message
	} else {
		apiErr.Detail = strings.TrimSpace(string(b))
	}
	return apiErr
}

// hijack performs the HTTP upgrade libpod uses for exec-start and attach, and
// returns the connection positioned at the raw stream.
//
// It hand-rolls the request for the same reason engineClient does: net/http
// gives no way to take the connection over after an upgrade. The response is
// read off the wire far enough to consume the status line and headers, leaving
// the caller at the first payload byte.
func (e *podmanEngine) hijack(ctx context.Context, method, path string, body []byte) (net.Conn, error) {
	conn, err := e.dial(ctx)
	if err != nil {
		return nil, err
	}
	full := e.prefix + "/libpod" + path
	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\n", method, full)
	fmt.Fprintf(&req, "Host: %s\r\n", e.hostHeader)
	req.WriteString("Connection: Upgrade\r\n")
	req.WriteString("Upgrade: tcp\r\n")
	if body != nil {
		req.WriteString("Content-Type: application/json\r\n")
		fmt.Fprintf(&req, "Content-Length: %d\r\n", len(body))
	}
	req.WriteString("\r\n")
	if body != nil {
		req.Write(body)
	}
	if _, err := conn.Write(req.Bytes()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("podman hijack %s %s: write request: %w", method, full, err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("podman hijack %s %s: read status: %w", method, full, err)
	}
	fields := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	code := 0
	if len(fields) >= 2 {
		code, _ = strconv.Atoi(fields[1])
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("podman hijack %s %s: read headers: %w", method, full, err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	// 101 is the upgrade; 200 is what libpod returns when it streams without
	// upgrading. Anything else is a failure whose body is still buffered.
	if code != http.StatusSwitchingProtocols && code != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(br, 8<<10))
		conn.Close()
		return nil, fmt.Errorf("podman hijack %s %s: %s: %s",
			method, full, strings.TrimSpace(statusLine), strings.TrimSpace(string(b)))
	}
	// The bufio.Reader carries any bytes already pulled off the socket while
	// reading headers; hijackedConn reads through it so none are lost.
	return &hijackedConn{Conn: conn, r: br}, nil
}
