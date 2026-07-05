package server

import (
	"context"
	"errors"
	"testing"

	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
)

// backendNoReader satisfies deploy.Backend (embedded, never called) but NOT
// deploy.MountSessionReader.
type backendNoReader struct{ deploy.Backend }

// backendReader adds ExistingMountSession, so it satisfies MountSessionReader.
type backendReader struct {
	deploy.Backend
	id  string
	err error
}

func (b backendReader) ExistingMountSession(_ context.Context, _ string) (string, error) {
	return b.id, b.err
}

// isFreshID reports whether id looks like a freshly minted session id
// (newSessionID: 16 random bytes hex = 32 chars), i.e. NOT a reused literal.
func isFreshID(id string) bool { return len(id) == 32 }

// TestMountSessionIDReuse pins the stable-session-id policy: on re-apply the server
// reuses the id already baked into the running pod (so a reconnecting client
// re-registers under the id the pod presents, keeping its mounts live) — but never
// reuses an id a live session still holds, and always falls back to a fresh id when
// there is nothing safe to reuse.
func TestMountSessionIDReuse(t *testing.T) {
	s := newTestServerObj(t)
	ctx := context.Background()

	// A backend without MountSessionReader -> always a fresh id.
	if id := s.mountSessionID(ctx, backendNoReader{}, "web"); !isFreshID(id) {
		t.Fatalf("no-reader backend must get a fresh id; got %q", id)
	}

	// Reader reports a baked id that no live session holds -> reuse it.
	if id := s.mountSessionID(ctx, backendReader{id: "baked-1"}, "web"); id != "baked-1" {
		t.Fatalf("re-apply must reuse the baked id; got %q", id)
	}

	// Same baked id, but a live session currently holds it -> must NOT clobber it.
	s.mounts.put("baked-1", &deploywire.ServerSession{})
	if id := s.mountSessionID(ctx, backendReader{id: "baked-1"}, "web"); id == "baked-1" {
		t.Fatal("must not reuse an id a live session still holds")
	} else if !isFreshID(id) {
		t.Fatalf("want a fresh id when the baked id is live; got %q", id)
	}

	// Reader reports no baked session -> fresh.
	if id := s.mountSessionID(ctx, backendReader{id: ""}, "web"); !isFreshID(id) {
		t.Fatalf("no baked session must yield a fresh id; got %q", id)
	}

	// A read-back error must never fail the apply -> fresh id.
	if id := s.mountSessionID(ctx, backendReader{err: errors.New("boom")}, "web"); !isFreshID(id) {
		t.Fatalf("a read-back error must fall back to a fresh id; got %q", id)
	}
}
