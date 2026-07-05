package lazyctx

import (
	"context"
	"io"
	"testing"

	"github.com/containerd/errdefs"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestContentStoreServesBlobsNotLayer(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "a", "1")
	ld := build(t, src, nil).Digest()
	im, err := SyntheticImage(ld, 42, "ref", map[string]string{LazyLabel: "ctx/data"})
	if err != nil {
		t.Fatalf("SyntheticImage: %v", err)
	}
	cs := im.ContentStore()
	ctx := context.Background()

	// Manifest blob: Info reports its size and ReaderAt returns its exact bytes.
	info, err := cs.Info(ctx, im.Manifest.Digest)
	if err != nil {
		t.Fatalf("Info(manifest): %v", err)
	}
	if info.Size != im.Manifest.Size {
		t.Errorf("Info size = %d, want %d", info.Size, im.Manifest.Size)
	}
	ra, err := cs.ReaderAt(ctx, im.Manifest)
	if err != nil {
		t.Fatalf("ReaderAt(manifest): %v", err)
	}
	defer ra.Close()
	got := make([]byte, ra.Size())
	if _, err := ra.ReadAt(got, 0); err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(got) != string(im.Blobs()[im.Manifest.Digest]) {
		t.Error("manifest bytes mismatch")
	}

	// The lazy layer blob is NOT served — a fetch is NotFound (never happens on
	// the remote-snapshot path).
	if _, err := cs.Info(ctx, ld); !errdefs.IsNotFound(err) {
		t.Errorf("Info(layer) err = %v, want NotFound", err)
	}
	if _, err := cs.ReaderAt(ctx, ocispec.Descriptor{Digest: ld}); !errdefs.IsNotFound(err) {
		t.Errorf("ReaderAt(layer) err = %v, want NotFound", err)
	}
}

func TestContentStoreReadOnly(t *testing.T) {
	im, err := SyntheticImage("sha256:"+zeros64, 1, "ref", nil)
	if err != nil {
		t.Fatalf("SyntheticImage: %v", err)
	}
	cs := im.ContentStore()
	if _, err := cs.Writer(context.Background()); err == nil {
		t.Error("Writer should be unsupported on a read-only store")
	}
	if err := cs.Delete(context.Background(), im.Manifest.Digest); err == nil {
		t.Error("Delete should be unsupported")
	}
}

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"
