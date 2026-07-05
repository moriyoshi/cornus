package lazyctx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestSyntheticImageStructure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "a")
	writeFile(t, dir, "b/c.txt", "c")
	man := build(t, dir, nil)
	ld := man.Digest()

	annotations := map[string]string{LazyLabel: "ctx/data"}
	im, err := SyntheticImage(ld, int64(len(man.Bytes())), "cornus/lazy@"+ld.String(), annotations)
	if err != nil {
		t.Fatalf("SyntheticImage: %v", err)
	}

	// Manifest blob parses and links config -> layer.
	manBytes, ok := im.Blobs()[im.Manifest.Digest]
	if !ok {
		t.Fatal("manifest blob missing")
	}
	if im.Manifest.Digest != digest.FromBytes(manBytes) {
		t.Error("manifest descriptor digest mismatch")
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(manBytes, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(m.Layers) != 1 {
		t.Fatalf("layers = %d, want 1", len(m.Layers))
	}
	if m.Layers[0].Digest != ld {
		t.Errorf("layer digest = %s, want %s", m.Layers[0].Digest, ld)
	}
	if m.Layers[0].Annotations[LazyLabel] != "ctx/data" {
		t.Errorf("layer annotations lost: %v", m.Layers[0].Annotations)
	}

	// Config exists and its DiffID references the layer digest (uncompressed).
	cfgBytes, ok := im.Blobs()[m.Config.Digest]
	if !ok {
		t.Fatal("config blob missing")
	}
	var cfg ocispec.Image
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if len(cfg.RootFS.DiffIDs) != 1 || cfg.RootFS.DiffIDs[0] != ld {
		t.Errorf("config diffIDs = %v, want [%s]", cfg.RootFS.DiffIDs, ld)
	}

	// The layer blob is intentionally NOT materialized.
	if _, ok := im.Blobs()[ld]; ok {
		t.Error("layer blob should not be materialized (served lazily)")
	}
}

func TestSyntheticImageWriteLayout(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "x", "1")
	ld := build(t, src, nil).Digest()
	im, err := SyntheticImage(ld, 10, "ref", nil)
	if err != nil {
		t.Fatalf("SyntheticImage: %v", err)
	}

	out := t.TempDir()
	if err := im.WriteLayout(out); err != nil {
		t.Fatalf("WriteLayout: %v", err)
	}
	// oci-layout marker + index.json present.
	if _, err := os.Stat(filepath.Join(out, "oci-layout")); err != nil {
		t.Errorf("oci-layout marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err != nil {
		t.Errorf("index.json: %v", err)
	}
	// config + manifest blobs written; layer blob absent.
	blobs := filepath.Join(out, "blobs", "sha256")
	ents, err := os.ReadDir(blobs)
	if err != nil {
		t.Fatalf("read blobs: %v", err)
	}
	if len(ents) != 2 {
		t.Errorf("blob count = %d, want 2 (config+manifest, no layer)", len(ents))
	}
	if _, err := os.Stat(filepath.Join(blobs, ld.Encoded())); !os.IsNotExist(err) {
		t.Errorf("layer blob was written (should be lazy): stat err = %v", err)
	}
}
