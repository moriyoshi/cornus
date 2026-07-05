package lazyctx

import (
	"context"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestComputeDigestsDeterministicAndContentSensitive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello")
	writeFile(t, dir, "sub/b.txt", "world")
	ctx := context.Background()

	d1, err := ComputeDigests(ctx, dir, nil)
	if err != nil {
		t.Fatalf("ComputeDigests: %v", err)
	}
	byPath := map[string]digest.Digest{}
	for _, fd := range d1 {
		if fd.Stat == nil || fd.Digest == "" {
			t.Errorf("%s: stat=%v digest=%q", fd.Path, fd.Stat, fd.Digest)
		}
		byPath[fd.Path] = fd.Digest
	}
	// Deterministic across runs.
	d2, _ := ComputeDigests(ctx, dir, nil)
	for _, fd := range d2 {
		if byPath[fd.Path] != fd.Digest {
			t.Errorf("digest not stable for %s", fd.Path)
		}
	}
	// Content-sensitive: changing a file changes its digest.
	writeFile(t, dir, "a.txt", "hello-CHANGED-and-longer")
	d3, _ := ComputeDigests(ctx, dir, nil)
	for _, fd := range d3 {
		if fd.Path == "a.txt" && fd.Digest == byPath["a.txt"] {
			t.Error("a.txt digest unchanged after content change")
		}
	}
}
