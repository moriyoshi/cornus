package clientagent

import (
	"testing"

	"cornus/cmd/cornus/internal/webbff"
)

// TestWebLocalRootsCarriesEveryField covers the last hop of `cornus web
// --local-root --publish-in-conduit`: the wire form becoming the BFF's form
// inside the agent.
//
// It is a field-for-field copy, which is exactly why it is worth pinning. The two
// types are deliberately separate (protocol.go is a wire contract), so they are
// kept in step by hand — and a hand-written copy that silently drops ReadOnly
// turns a declared refusal into a writable root, with nothing failing anywhere.
// The empty case is asserted too: nil in, nil out, so a published UI with no
// declared roots does not send an empty slice that reads as "roots were given".
func TestWebLocalRootsCarriesEveryField(t *testing.T) {
	if got := webLocalRoots(nil); got != nil {
		t.Errorf("webLocalRoots(nil) = %v, want nil", got)
	}
	got := webLocalRoots([]WebLocalRoot{
		{Label: "notes", Path: "/srv/notes", ReadOnly: true},
		{Path: "/srv/scratch"},
	})
	want := []webbff.LocalRootSpec{
		{Label: "notes", Path: "/srv/notes", ReadOnly: true},
		{Path: "/srv/scratch"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d roots, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("root %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
