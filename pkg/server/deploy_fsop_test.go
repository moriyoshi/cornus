package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/client"
	"cornus/pkg/deploy"
)

// fsopBackend is a fakeBackend that also implements deploy.FSOperator, so these tests
// drive the real HTTP surface and the real pkg/client against a scripted operator. The
// point of embedding rather than writing a second backend is that a backend WITHOUT the
// capability is then just fakeBackend, and the 501 path is a real type assertion failing
// rather than a flag.
type fsopBackend struct {
	fakeBackend
	got  api.FSOpRequest
	body []byte
	resp api.FSOpResponse
	err  error
	// out, when set, is the tar an FSOpGet streams back.
	out []byte
	// outThenErr makes FSOpGet write out and THEN fail, modelling an operator that
	// dies mid-archive — where the status is already committed.
	outThenErr error
}

func (f *fsopBackend) FSOp(_ context.Context, _ string, req api.FSOpRequest, body io.Reader, out io.Writer) (api.FSOpResponse, error) {
	f.got = req
	if body != nil {
		f.body, _ = io.ReadAll(body)
	}
	if out != nil && len(f.out) > 0 {
		out.Write(f.out)
	}
	if f.outThenErr != nil {
		return api.FSOpResponse{}, f.outThenErr
	}
	return f.resp, f.err
}

func TestFSOpEndpointRoundTripsEveryOp(t *testing.T) {
	fb := &fsopBackend{resp: api.FSOpResponse{Entries: []api.PathStat{{Name: "a.txt", Size: 3}}}}
	srv := newTestServer(t, fb)
	defer srv.Close()
	c := client.New(srv.URL)

	resp, err := c.FSOp(context.Background(), "web",
		api.FSOpRequest{Op: api.FSOpList, Path: "/data"}, nil, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Name != "a.txt" {
		t.Fatalf("entries = %+v", resp.Entries)
	}

	// Every operand has to survive the query string; a flag dropped in transit turns
	// a refusal into a destructive success (NoOverwriteDirNonDir is the one that
	// deletes a directory tree when it goes missing).
	fb.resp = api.FSOpResponse{}
	if _, err := c.FSOp(context.Background(), "web", api.FSOpRequest{
		Op: api.FSOpCopy, Path: "/data/a", To: "/data/b",
		Recursive: true, NoOverwriteDirNonDir: true, CopyUIDGID: true,
	}, nil, nil); err != nil {
		t.Fatalf("copy: %v", err)
	}
	want := api.FSOpRequest{
		Op: api.FSOpCopy, Path: "/data/a", To: "/data/b",
		Recursive: true, NoOverwriteDirNonDir: true, CopyUIDGID: true,
	}
	if fb.got != want {
		t.Fatalf("backend saw %+v, want %+v", fb.got, want)
	}
}

func TestFSOpEndpointCarriesBodiesBothWays(t *testing.T) {
	fb := &fsopBackend{out: []byte("TAR-OUT")}
	srv := newTestServer(t, fb)
	defer srv.Close()
	c := client.New(srv.URL)

	var got bytes.Buffer
	if _, err := c.FSOp(context.Background(), "web",
		api.FSOpRequest{Op: api.FSOpGet, Path: "/data/a"}, nil, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.String() != "TAR-OUT" {
		t.Fatalf("get body = %q", got.String())
	}

	fb.out = nil
	if _, err := c.FSOp(context.Background(), "web",
		api.FSOpRequest{Op: api.FSOpPut, Path: "/data"}, strings.NewReader("TAR-IN"), nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if string(fb.body) != "TAR-IN" {
		t.Fatalf("backend received %q, want TAR-IN", fb.body)
	}
}

// TestFSOpGetReportsAMidStreamFailure: once the tar has begun the 200 is committed, so a
// failure has nowhere to go but the trailer. Without this the caller extracts a truncated
// archive and reports a successful copy of a smaller tree.
func TestFSOpGetReportsAMidStreamFailure(t *testing.T) {
	fb := &fsopBackend{out: []byte("TAR-PREFIX"), outThenErr: errors.New("the pod went away mid-copy")}
	srv := newTestServer(t, fb)
	defer srv.Close()

	var got bytes.Buffer
	_, err := client.New(srv.URL).FSOp(context.Background(), "web",
		api.FSOpRequest{Op: api.FSOpGet, Path: "/data/a"}, nil, &got)
	if err == nil {
		t.Fatal("a truncated archive was reported as a complete one")
	}
	if !strings.Contains(err.Error(), "went away mid-copy") {
		t.Fatalf("err = %v, want the backend's mid-stream reason", err)
	}
	if got.String() != "TAR-PREFIX" {
		t.Fatalf("partial tar = %q", got.String())
	}
}

// TestFSOpRefusalsMapToStatuses is the mapping the fallback chain hangs off. Unsupported
// in particular must be 501 and must surface as ErrFSOpUnsupported, because the caller's
// correct response to it is to relay the bytes itself — not to tell the user anything.
func TestFSOpRefusalsMapToStatuses(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{api.FSErrNotFound, http.StatusNotFound},
		{api.FSErrUnsupported, http.StatusNotImplemented},
		{api.FSErrReadOnly, http.StatusForbidden},
		{api.FSErrNotEmpty, http.StatusConflict},
		{api.FSErrCrossDevice, http.StatusConflict},
		{api.FSErrNotDir, http.StatusBadRequest},
	} {
		t.Run(tc.code, func(t *testing.T) {
			fb := &fsopBackend{resp: api.FSOpResponse{Error: "refused", Code: tc.code}}
			srv := newTestServer(t, fb)
			defer srv.Close()

			raw, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/fsop?op=stat&path=/data", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			raw.Body.Close()
			if raw.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", raw.StatusCode, tc.status)
			}

			resp, err := client.New(srv.URL).FSOp(context.Background(), "web",
				api.FSOpRequest{Op: api.FSOpStat, Path: "/data"}, nil, nil)
			if err == nil {
				t.Fatal("a refusal came back as a success")
			}
			if tc.code == api.FSErrUnsupported {
				if !errors.Is(err, client.ErrFSOpUnsupported) {
					t.Fatalf("err = %v, want ErrFSOpUnsupported so the caller falls back", err)
				}
			}
			if resp.Code != tc.code {
				t.Errorf("code = %q, want %q — the caller cannot classify without it", resp.Code, tc.code)
			}
		})
	}
}

// TestFSOpOnABackendWithoutTheCapability: the plain fakeBackend does not implement
// deploy.FSOperator, so this exercises the real type assertion. It must be 501 with
// "unsupported" in the text — pkg/server's own streamErrStatus matches that substring, and
// so does every fallback in the tree.
func TestFSOpOnABackendWithoutTheCapability(t *testing.T) {
	var _ deploy.Backend = &fakeBackend{}
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	raw, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/fsop?op=list&path=/data", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Body.Close()
	if raw.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", raw.StatusCode)
	}
	body, _ := io.ReadAll(raw.Body)
	if !strings.Contains(string(body), "unsupported") {
		t.Fatalf("body = %q, want it to contain \"unsupported\"", body)
	}

	_, err = client.New(srv.URL).FSOp(context.Background(), "web",
		api.FSOpRequest{Op: api.FSOpList, Path: "/data"}, nil, nil)
	if !errors.Is(err, client.ErrFSOpUnsupported) {
		t.Fatalf("err = %v, want ErrFSOpUnsupported", err)
	}
}

// TestFSOpWritesAreGatedByTheDeployPolicy. Every fsop is a POST, so unlike the archive
// endpoint the METHOD cannot say whether this is a read or a write — the op does. An
// identity restricted out of "deploy" must not be able to delete a volume's contents
// through a surface that merely looks like a read.
func TestFSOpWritesAreGatedByTheDeployPolicy(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	clearAuthEnv(t)
	t.Setenv("CORNUS_JWT_HS256_SECRET", string(secret))
	t.Setenv("CORNUS_API_POLICY", `{"ci-bot":["deploy"]}`)

	srv := newTestServer(t, &fsopBackend{})
	defer srv.Close()
	ci := jwtFor(t, secret, "ci-bot")
	stranger := jwtFor(t, secret, "stranger")

	post := func(t *testing.T, op, token string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost,
			srv.URL+"/.cornus/v1/deploy/web/fsop?op="+op+"&path=/data&to=/data/x", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for _, op := range []api.FSOp{api.FSOpPut, api.FSOpMkdir, api.FSOpRemove, api.FSOpRename, api.FSOpCopy} {
		if code := post(t, string(op), stranger); code != http.StatusForbidden {
			t.Errorf("stranger %s: code = %d, want 403", op, code)
		}
		if code := post(t, string(op), ci); code == http.StatusForbidden {
			t.Errorf("ci-bot %s: got 403, should pass the deploy gate", op)
		}
	}
	// Reads stay ungated, matching the archive endpoint's copy-out.
	for _, op := range []api.FSOp{api.FSOpStat, api.FSOpList, api.FSOpGet} {
		if code := post(t, string(op), stranger); code == http.StatusForbidden {
			t.Errorf("stranger %s: got 403, but reads are not gated", op)
		}
	}
	// An op nobody has heard of is gated, not waved through — the default has to be
	// deny for a vocabulary that will grow.
	if code := post(t, "chown", stranger); code != http.StatusForbidden {
		t.Errorf("stranger chown: code = %d, want 403 (unknown ops default to gated)", code)
	}
}

func TestFSOpRejectsAnIncompleteRequest(t *testing.T) {
	srv := newTestServer(t, &fsopBackend{})
	defer srv.Close()
	for _, q := range []string{"op=list", "path=/data", ""} {
		resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/fsop?"+q, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s: status = %d, want 400", q, resp.StatusCode)
		}
	}
}
