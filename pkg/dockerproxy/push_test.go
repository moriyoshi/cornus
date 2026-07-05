package dockerproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	cornusregistry "cornus/pkg/registry"
	"cornus/pkg/storage"
)

// newBuiltinRegistry starts an in-process cornus registry (the "builtin
// registry" / local store) and returns its host and an image reference helper.
func newBuiltinRegistry(t *testing.T) (host string, srv *httptest.Server) {
	t.Helper()
	st, err := storage.Open(context.Background(), "mem://", t.TempDir())
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	mux := http.NewServeMux()
	cornusregistry.New(st).Register(mux)
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), srv
}

// pushImageTo pushes a random image to ref (host/repo:tag) over plain HTTP.
func pushImageTo(t *testing.T, refStr string) {
	t.Helper()
	img, err := random.Image(256, 2)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	ref, err := name.ParseReference(refStr, name.Insecure)
	if err != nil {
		t.Fatalf("parse %q: %v", refStr, err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("seed push %q: %v", refStr, err)
	}
}

// readStream decodes the newline-delimited jsonmessage frames from a push/pull
// response body.
func readStream(t *testing.T, resp *http.Response) []jsonMessage {
	t.Helper()
	var msgs []jsonMessage
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m jsonMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode frame %q: %v", line, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// auxDigest returns the Digest from the last aux frame, or "".
func auxDigest(msgs []jsonMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if m, ok := msgs[i].Aux.(map[string]any); ok {
			if d, ok := m["Digest"].(string); ok {
				return d
			}
		}
	}
	return ""
}

// TestImagePushBareLocal confirms `docker push app:v1` (no registry part) is
// acknowledged against the builtin registry: the image already lives there, so
// the stream reports success with its real digest and nothing is copied out.
func TestImagePushBareLocal(t *testing.T) {
	host, _ := newBuiltinRegistry(t)
	pushImageTo(t, host+"/app:v1")
	want, err := remote.Head(mustRef(t, host+"/app:v1"), remote.WithTransport(http.DefaultTransport))
	if err != nil {
		t.Fatalf("head seeded image: %v", err)
	}

	srv := httptest.NewServer(New(&fakeAttacher{registryHost: host}).Handler())
	defer srv.Close()
	resp := do(t, http.MethodPost, srv.URL+"/v1.43/images/app/push?tag=v1", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("push status = %d", resp.StatusCode)
	}
	msgs := readStream(t, resp)
	for _, m := range msgs {
		if m.Error != "" {
			t.Fatalf("push reported error: %s", m.Error)
		}
	}
	if got := auxDigest(msgs); got != want.Digest.String() {
		t.Errorf("aux digest = %q, want %q", got, want.Digest)
	}
}

// TestImagePushDockerHubLocal confirms a docker.io ref (what the docker CLI
// expands a bare push to) is acknowledged against the builtin registry, with the
// official "library/" prefix stripped so it lines up with `cornus build -t app`.
func TestImagePushDockerHubLocal(t *testing.T) {
	host, _ := newBuiltinRegistry(t)
	pushImageTo(t, host+"/app:v1") // stored as repo "app" (no library/)
	want, err := remote.Head(mustRef(t, host+"/app:v1"), remote.WithTransport(http.DefaultTransport))
	if err != nil {
		t.Fatalf("head seeded image: %v", err)
	}

	srv := httptest.NewServer(New(&fakeAttacher{registryHost: host}).Handler())
	defer srv.Close()
	// docker push app:v1 -> the CLI sends POST /images/docker.io/library/app/push
	resp := do(t, http.MethodPost, srv.URL+"/v1.43/images/docker.io/library/app/push?tag=v1", nil)
	defer resp.Body.Close()
	msgs := readStream(t, resp)
	for _, m := range msgs {
		if m.Error != "" {
			t.Fatalf("docker.io push reported error: %s", m.Error)
		}
	}
	if got := auxDigest(msgs); got != want.Digest.String() {
		t.Errorf("aux digest = %q, want %q", got, want.Digest)
	}
}

// TestImagePushNotFound confirms pushing an image absent from the builtin
// registry yields a docker "does not exist locally" error frame (in-stream).
func TestImagePushNotFound(t *testing.T) {
	host, _ := newBuiltinRegistry(t)
	srv := httptest.NewServer(New(&fakeAttacher{registryHost: host}).Handler())
	defer srv.Close()
	resp := do(t, http.MethodPost, srv.URL+"/v1.43/images/ghost/push?tag=v1", nil)
	defer resp.Body.Close()
	msgs := readStream(t, resp)
	var gotErr string
	for _, m := range msgs {
		if m.Error != "" {
			gotErr = m.Error
		}
	}
	if !strings.Contains(gotErr, "does not exist locally") {
		t.Fatalf("error frame = %q, want 'does not exist locally'", gotErr)
	}
}

// TestImagePushExternalCopy confirms a registry-qualified push copies the image
// from the builtin store out to the named (loopback = insecure) registry.
func TestImagePushExternalCopy(t *testing.T) {
	builtin, _ := newBuiltinRegistry(t)
	pushImageTo(t, builtin+"/me/app:v1")

	extHost, _ := newBuiltinRegistry(t) // a second registry standing in for the external one

	srv := httptest.NewServer(New(&fakeAttacher{registryHost: builtin}).Handler())
	defer srv.Close()
	resp := do(t, http.MethodPost, srv.URL+"/v1.43/images/"+extHost+"/me/app/push?tag=v1", nil)
	defer resp.Body.Close()
	msgs := readStream(t, resp)
	for _, m := range msgs {
		if m.Error != "" {
			t.Fatalf("external push error: %s", m.Error)
		}
	}
	// The image must now be resolvable in the external registry.
	if _, err := remote.Head(mustRef(t, extHost+"/me/app:v1"), remote.WithTransport(http.DefaultTransport)); err != nil {
		t.Fatalf("image not found in external registry after push: %v", err)
	}
}

func mustRef(t *testing.T, s string) name.Reference {
	t.Helper()
	ref, err := name.ParseReference(s, name.Insecure)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ref
}
