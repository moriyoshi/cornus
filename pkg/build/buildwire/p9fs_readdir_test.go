package buildwire

import (
	"testing"
	"time"

	"github.com/hugelgupf/p9/fsimpl/templatefs"
	"github.com/hugelgupf/p9/p9"
)

// evilDir models a hostile 9P server that answers every Treaddir with a
// non-empty batch whose last entry's Offset never advances — the resume cookie
// the honest client would use to page. Every other method is a no-op default.
type evilDir struct {
	templatefs.NoopFile
}

func (evilDir) Walk(names []string) ([]p9.QID, p9.File, error) {
	// Only the empty (clone) walk is exercised by readdir("").
	return nil, evilDir{}, nil
}

func (evilDir) Open(mode p9.OpenFlags) (p9.QID, uint32, error) {
	return p9.QID{Type: p9.TypeDir}, 0, nil
}

func (evilDir) Readdir(offset uint64, count uint32) (p9.Dirents, error) {
	// Fixed Offset=1 regardless of the requested offset: after the first read
	// the client's cursor can never move past 1.
	return p9.Dirents{
		{Offset: 1, Name: "a"},
		{Offset: 1, Name: "b"},
	}, nil
}

type evilAttacher struct{}

func (evilAttacher) Attach() (p9.File, error) { return evilDir{}, nil }

// TestReaddirNonAdvancingOffset proves p9FS.readdir does not spin forever (a
// remote DoS) when the untrusted peer returns a non-empty dirent batch whose
// resume offset never advances; it must return an error instead.
func TestReaddirNonAdvancingOffset(t *testing.T) {
	cl := rawClient(t, evilAttacher{})
	fsys := &p9FS{client: cl}

	done := make(chan error, 1)
	go func() {
		_, err := fsys.readdir("")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("readdir returned nil error for a non-advancing peer offset; expected failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("readdir did not terminate on a non-advancing peer offset (infinite loop)")
	}
}
