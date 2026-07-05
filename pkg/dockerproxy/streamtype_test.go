package dockerproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The defect these tests pin: every container stream — logs, attach, exec —
// was announced as application/vnd.docker.raw-stream, including the non-TTY
// ones, which are stdcopy-multiplexed. Since API v1.42 the daemon distinguishes
// the two, and a client that trusts the media type over re-inspecting
// Config.Tty will hand a non-TTY stream's 8-byte stdcopy frame headers to the
// user as text. moby's rule, which streamContentType mirrors:
//
//	contentType := types.MediaTypeRawStream
//	if !tty && versions.GreaterThanOrEqualTo(version, "1.42") {
//	    contentType = types.MediaTypeMultiplexedStream
//	}
//
// Both halves of the condition are load-bearing and fail independently, so the
// TTY axis and the version axis are asserted separately below.

// TestAPIVersionAtLeastComparesNumerically pins the comparison itself. Docker
// minor versions are well past 9, so a lexicographic compare — the obvious
// wrong implementation — reports "1.5" >= "1.42" and would switch a v1.5 client
// to a media type that did not exist for another 37 versions.
func TestAPIVersionAtLeastComparesNumerically(t *testing.T) {
	cases := []struct {
		v, min string
		want   bool
	}{
		{"1.42", "1.42", true},  // the boundary is inclusive
		{"1.41", "1.42", false}, // one below
		{"1.43", "1.42", true},
		{"1.5", "1.42", false}, // lexicographically "1.5" > "1.42"; numerically it is older
		{"1.9", "1.42", false},
		{"2.0", "1.42", true}, // a larger major wins regardless of minor
		{"0.99", "1.42", false},
		{"v1.43", "1.42", true}, // tolerate a leading v
		{"1.100", "1.42", true}, // three-digit minors compare numerically too
		{"", "1.42", false},     // unparsable -> conservative (pre-1.42) answer
		{"garbage", "1.42", false},
		{"1", "1.42", false}, // no minor component
		{"1.x", "1.42", false},
	}
	for _, c := range cases {
		if got := apiVersionAtLeast(c.v, c.min); got != c.want {
			t.Errorf("apiVersionAtLeast(%q, %q) = %v, want %v", c.v, c.min, got, c.want)
		}
	}
}

// TestRequestAPIVersionDefaultsToDaemonVersion pins the unversioned case. An
// unversioned request is a client declining to pin, which docker serves at the
// daemon's own version — so it must get the NEWEST behaviour. Defaulting the
// other way (to the pre-1.42 answer) would be invisible to the docker CLI,
// which always pins, and would silently mistype every SDK request that does not.
func TestRequestAPIVersionDefaultsToDaemonVersion(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/containers/x/logs", nil)
	if got := requestAPIVersion(r); got != apiVersion {
		t.Fatalf("requestAPIVersion(unversioned) = %q, want the daemon version %q", got, apiVersion)
	}
	if got := requestAPIVersion(withAPIVersion(r, "1.41")); got != "1.41" {
		t.Fatalf("requestAPIVersion(pinned 1.41) = %q, want 1.41", got)
	}
}

// TestStreamContentTypeMatrix walks both axes of moby's condition.
func TestStreamContentTypeMatrix(t *testing.T) {
	cases := []struct {
		name    string
		version string // "" = unversioned request
		tty     bool
		want    string
	}{
		{"non-TTY on 1.42 is multiplexed", "1.42", false, mediaTypeMultiplexedStream},
		{"non-TTY on 1.43 is multiplexed", "1.43", false, mediaTypeMultiplexedStream},
		{"non-TTY unversioned is multiplexed", "", false, mediaTypeMultiplexedStream},
		{"non-TTY on 1.41 stays raw", "1.41", false, mediaTypeRawStream},
		{"non-TTY on 1.24 stays raw", "1.24", false, mediaTypeRawStream},
		// A TTY stream is unframed at the source, so it is raw at EVERY version —
		// the half a version-only fix would get wrong. These cases pin the RULE,
		// not an end-to-end guarantee: whether a TTY body really is unframed is
		// the backend's business, and the backends currently disagree (see
		// streamContentType's comment).
		{"TTY on 1.43 is raw", "1.43", true, mediaTypeRawStream},
		{"TTY unversioned is raw", "", true, mediaTypeRawStream},
		{"TTY on 1.41 is raw", "1.41", true, mediaTypeRawStream},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/containers/x/logs", nil)
			if c.version != "" {
				r = withAPIVersion(r, c.version)
			}
			if got := streamContentType(r, c.tty); got != c.want {
				t.Fatalf("streamContentType(version=%q, tty=%v) = %q, want %q", c.version, c.tty, got, c.want)
			}
		})
	}
}

// createWithTTY creates and starts a container, recording Tty, and returns its
// id. It posts through the UNVERSIONED path so the version under test is only
// the one the caller puts on the logs/attach request.
func createWithTTY(t *testing.T, srv *httptest.Server, name string, tty bool) string {
	t.Helper()
	b, _ := json.Marshal(createRequest{Image: "img", Tty: tty})
	resp, err := http.Post(srv.URL+"/containers/create?name="+name, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var cr createResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if cr.ID == "" {
		t.Fatal("no container id")
	}
	do(t, http.MethodPost, srv.URL+"/containers/"+cr.ID+"/start", nil).Body.Close()
	return cr.ID
}

// TestLogsContentTypeThroughHandler drives the real handler, so it covers the
// plumbing the unit tests above cannot: that Handler preserves the /vX.Y prefix
// it strips for routing, and that logs reads the container's RECORDED Tty
// rather than assuming one. Both were absent before this change — Handler
// discarded the version outright.
func TestLogsContentTypeThroughHandler(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		tty    bool
		want   string
	}{
		{"unversioned non-TTY", "", false, mediaTypeMultiplexedStream},
		{"v1.43 non-TTY", "/v1.43", false, mediaTypeMultiplexedStream},
		{"v1.42 non-TTY", "/v1.42", false, mediaTypeMultiplexedStream},
		{"v1.41 non-TTY", "/v1.41", false, mediaTypeRawStream},
		{"v1.43 TTY", "/v1.43", true, mediaTypeRawStream},
		{"unversioned TTY", "", true, mediaTypeRawStream},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fa := &fakeAttacher{}
			srv := httptest.NewServer(New(fa).Handler())
			defer srv.Close()
			id := createWithTTY(t, srv, "web", c.tty)

			resp := do(t, http.MethodGet, srv.URL+c.prefix+"/containers/"+id+"/logs?stdout=1&stderr=1", nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("logs status = %d, want 200", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != c.want {
				t.Fatalf("case %d: content-type = %q, want %q", i, ct, c.want)
			}
		})
	}
}

// readHandshakeContentType reads a hijacked stream's handshake and returns its
// status line and Content-Type. Unlike drainHeaders it keeps the header value,
// which is the whole point here: for attach and exec the media type rides in a
// hand-written response, so nothing in net/http would catch a wrong one.
func readHandshakeContentType(t *testing.T, br *bufio.Reader) (status, contentType string) {
	t.Helper()
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			return status, contentType
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Type") {
			contentType = strings.TrimSpace(v)
		}
	}
}

// TestAttachHandshakeContentType covers the hijacked attach path. Its handshake
// is written by hand onto the raw connection, so it is a SEPARATE code path
// from the logs header and can regress on its own.
func TestAttachHandshakeContentType(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		tty    bool
		want   string
	}{
		{"non-TTY attach is multiplexed", "/v1.43", false, mediaTypeMultiplexedStream},
		{"TTY attach is raw", "/v1.43", true, mediaTypeRawStream},
		{"non-TTY attach on 1.41 is raw", "/v1.41", false, mediaTypeRawStream},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fa := &fakeAttacher{}
			srv := httptest.NewServer(New(fa).Handler())
			defer srv.Close()
			id := createWithTTY(t, srv, "web", c.tty)

			conn := rawDial(t, srv.URL)
			defer conn.Close()
			req := "POST " + c.prefix + "/containers/" + id + "/attach?stream=1&stdout=1&stderr=1 HTTP/1.1\r\n" +
				"Host: docker\r\n" +
				"Connection: Upgrade\r\n" +
				"Upgrade: tcp\r\n" +
				"Content-Length: 0\r\n\r\n"
			if _, err := io.WriteString(conn, req); err != nil {
				t.Fatal(err)
			}
			br := bufio.NewReader(conn)
			status, ct := readHandshakeContentType(t, br)
			if !strings.Contains(status, "101") {
				t.Fatalf("attach handshake status = %q, want 101", status)
			}
			if ct != c.want {
				t.Fatalf("attach handshake content-type = %q, want %q", ct, c.want)
			}
		})
	}
}

// TestExecStartHandshakeContentType covers the third site. exec takes its Tty
// from the exec-create record rather than the container, so a fix applied only
// to the container paths would leave `docker exec` mistyped.
func TestExecStartHandshakeContentType(t *testing.T) {
	cases := []struct {
		name string
		tty  bool
		want string
	}{
		{"non-TTY exec is multiplexed", false, mediaTypeMultiplexedStream},
		{"TTY exec is raw", true, mediaTypeRawStream},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fa := &fakeAttacher{}
			srv := httptest.NewServer(New(fa).Handler())
			defer srv.Close()
			// The CONTAINER is non-TTY in both cases: the exec's own Tty is what
			// must drive the media type here.
			id := createWithTTY(t, srv, "web", false)

			eb, _ := json.Marshal(map[string]any{"Cmd": []string{"sh"}, "Tty": c.tty})
			resp := do(t, http.MethodPost, srv.URL+"/containers/"+id+"/exec", eb)
			var er struct {
				Id string `json:"Id"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&er)
			resp.Body.Close()
			if er.Id == "" {
				t.Fatal("no exec id")
			}

			conn := rawDial(t, srv.URL)
			defer conn.Close()
			body := `{"Detach":false}`
			req := "POST /v1.43/exec/" + er.Id + "/start HTTP/1.1\r\n" +
				"Host: docker\r\n" +
				"Content-Type: application/json\r\n" +
				"Connection: Upgrade\r\n" +
				"Upgrade: tcp\r\n" +
				"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
			if _, err := io.WriteString(conn, req); err != nil {
				t.Fatal(err)
			}
			br := bufio.NewReader(conn)
			status, ct := readHandshakeContentType(t, br)
			if !strings.Contains(status, "101") {
				t.Fatalf("exec start handshake status = %q, want 101", status)
			}
			if ct != c.want {
				t.Fatalf("exec start handshake content-type = %q, want %q", ct, c.want)
			}
		})
	}
}
