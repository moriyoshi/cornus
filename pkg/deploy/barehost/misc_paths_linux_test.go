//go:build linux

package barehost

// A few remaining paths that are reachable without privilege: waiting for a log
// that does not exist yet, reading an image config out of the content store, and
// the TTY exec's bail-out when no pty ever arrives.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/namespaces"
	runc "github.com/containerd/go-runc"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"cornus/pkg/api"
)

// TestWaitForLogOpensTheFileOnceItAppears covers `logs -f` issued before the
// container has written anything: the follow must wait for the file rather than
// returning "no logs" and ending the stream.
func TestWaitForLogOpensTheFileOnceItAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cornus-web-0.log")
	type result struct {
		f   *os.File
		err error
	}
	done := make(chan result, 1)
	go func() {
		f, err := waitForLog(context.Background(), path)
		done <- result{f, err}
	}()

	time.Sleep(2 * logFollowInterval)
	if err := os.WriteFile(path, []byte("first output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("waitForLog: %v", r.err)
		}
		defer r.f.Close()
		data, err := io.ReadAll(r.f)
		if err != nil || string(data) != "first output\n" {
			t.Errorf("opened file contains %q (%v), want the container's first output", data, err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("waitForLog never noticed the log file appear")
	}
}

// TestWaitForLogGivesUpWhenTheClientDisconnects pins the cancellation path: a
// follow of a log that never appears must not leak a goroutine polling forever.
func TestWaitForLogGivesUpWhenTheClientDisconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := waitForLog(ctx, filepath.Join(t.TempDir(), "never.log"))
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waitForLog after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("waitForLog ignored the cancelled context")
	}
}

// TestDiffIDsReadsTheUncompressedLayerDigests covers the step that decides which
// snapshot chain a pull unpacks into: the diff IDs come from the image config
// blob, not from the manifest's (compressed) layer descriptors.
func TestDiffIDsReadsTheUncompressedLayerDigests(t *testing.T) {
	s, err := newImageStore(t.TempDir(), "native")
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	ctx := namespaces.WithNamespace(t.Context(), contentNamespace)

	want := []digest.Digest{digest.FromString("layer-0"), digest.FromString("layer-1")}
	desc := writeConfigBlob(t, ctx, s.content, ocispec.Image{
		Platform: ocispec.Platform{Architecture: "amd64", OS: "linux"},
		RootFS:   ocispec.RootFS{Type: "layers", DiffIDs: want},
	})

	got, err := s.diffIDs(ctx, desc)
	if err != nil {
		t.Fatalf("diffIDs: %v", err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("diffIDs = %v, want %v", got, want)
	}
}

func TestDiffIDsFailsOnAnAbsentOrUnreadableConfig(t *testing.T) {
	s, err := newImageStore(t.TempDir(), "native")
	if err != nil {
		t.Fatalf("newImageStore: %v", err)
	}
	ctx := namespaces.WithNamespace(t.Context(), contentNamespace)

	absent := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromString("never written"),
		Size:      12,
	}
	if _, err := s.diffIDs(ctx, absent); err == nil {
		t.Error("diffIDs for a config blob that was never fetched: want error")
	}

	// A blob that is present but not an image config must be reported as a parse
	// failure, not silently treated as an image with no layers.
	blob := []byte("this is not json")
	desc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(blob), Size: int64(len(blob))}
	if err := content.WriteBlob(ctx, s.content, "junk", bytes.NewReader(blob), desc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.diffIDs(ctx, desc); err == nil {
		t.Error("diffIDs on a non-config blob: want a parse error")
	}
}

func writeConfigBlob(t *testing.T, ctx context.Context, store content.Store, img ocispec.Image) ocispec.Descriptor {
	t.Helper()
	blob, err := json.Marshal(img)
	if err != nil {
		t.Fatal(err)
	}
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromBytes(blob),
		Size:      int64(len(blob)),
	}
	if err := content.WriteBlob(ctx, store, "config-"+desc.Digest.String(), bytes.NewReader(blob), desc); err != nil {
		t.Fatalf("write config blob: %v", err)
	}
	return desc
}

// TestTTYExecWithoutAPtyFinishesTheSession covers the bail-out the TTY path
// needs: when the runtime errors before handing over a pty master, the session
// must be closed out with a failure instead of the caller hanging on a
// connection nobody will ever write to.
func TestTTYExecWithoutAPtyFinishesTheSession(t *testing.T) {
	b, rt := newTestBackend(t)
	ctx := t.Context()
	seedRunnableInstance(t, b, rt, "web", 0)

	rt.execFn = func(_ context.Context, _ string, _ specs.Process, opts runtimeExecOpts) error {
		// A runtime that fails before sending a master: closing the console socket
		// is what the exec observes.
		if c, ok := opts.ConsoleSocket.(interface{ Close() error }); ok {
			_ = c.Close()
		}
		return &runc.ExitError{Status: 126}
	}

	execID, err := b.ExecCreate(ctx, "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	conn := newMemConn("")
	done := make(chan error, 1)
	go func() { done <- b.ExecStart(ctx, execID, api.ExecStartConfig{}, conn) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ExecStart: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("a TTY exec that never got a pty blocked instead of bailing out")
	}

	st, err := b.ExecInspect(ctx, execID)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if st.Running {
		t.Error("the session is still marked running after the exec failed")
	}
	if st.ExitCode == 0 {
		t.Error("a TTY exec that never started must not report success")
	}
}
