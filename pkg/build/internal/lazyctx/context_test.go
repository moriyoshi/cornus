package lazyctx

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestPrepareLazyContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "big.bin", "payload")
	writeFile(t, dir, "sub/x", "y")

	lc, err := Prepare("data", dir, "/host/backing", nil)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if lc.StoreID != "cornus-lazy-data" {
		t.Errorf("StoreID = %q", lc.StoreID)
	}
	if lc.ContextAttr != "oci-layout://"+lc.Ref {
		t.Errorf("ContextAttr = %q, want oci-layout://%s", lc.ContextAttr, lc.Ref)
	}
	if !strings.HasPrefix(lc.Ref, "cornus-lazy-data@sha256:") {
		t.Errorf("Ref = %q, want <storeID>@sha256:...", lc.Ref)
	}

	// The store resolves the manifest the oci-layout ref points at.
	mdgst := digest.Digest(strings.TrimPrefix(lc.Ref, "cornus-lazy-data@"))
	ctx := context.Background()
	info, err := lc.Store.Info(ctx, mdgst)
	if err != nil {
		t.Fatalf("store.Info(manifest): %v", err)
	}
	if info.Size == 0 {
		t.Error("manifest blob has zero size")
	}

	// That manifest's single layer carries the lazy label and the tree digest.
	ra, err := lc.Store.ReaderAt(ctx, ocispec.Descriptor{Digest: mdgst})
	if err != nil {
		t.Fatalf("store.ReaderAt(manifest): %v", err)
	}
	buf := make([]byte, ra.Size())
	_, _ = ra.ReadAt(buf, 0)
	ra.Close()
	var man ocispec.Manifest
	if err := json.Unmarshal(buf, &man); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(man.Layers) != 1 || man.Layers[0].Digest != lc.LayerDigest {
		t.Fatalf("layer digest = %v, want %s", man.Layers, lc.LayerDigest)
	}
	if man.Layers[0].Annotations[LazyLabel] != "/host/backing" {
		t.Errorf("LazyLabel not on layer descriptor: %v", man.Layers[0].Annotations)
	}

	// The cache key (layer digest) tracks content: change the tree, digest moves.
	writeFile(t, dir, "big.bin", "payload-CHANGED-and-longer")
	lc2, err := Prepare("data", dir, "/host/backing", nil)
	if err != nil {
		t.Fatal(err)
	}
	if lc2.LayerDigest == lc.LayerDigest {
		t.Error("layer digest (cache key) did not change when the tree changed")
	}
}
