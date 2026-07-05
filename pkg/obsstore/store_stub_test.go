//go:build !imbh

package obsstore

import (
	"errors"
	"testing"
)

// TestStubReportsAbsence pins the contract the whole feature rests on: without
// the `imbh` tag the store is not merely broken, it reports itself absent, so
// the server can skip its routes and the recorder can stay asleep instead of
// silently recording nothing.
func TestStubReportsAbsence(t *testing.T) {
	if Compiled() {
		t.Fatal("Compiled() = true in a build without the imbh tag")
	}
	s, err := Open(Config{Dir: t.TempDir()})
	if !errors.Is(err, ErrNotCompiled) {
		t.Errorf("Open error = %v, want ErrNotCompiled", err)
	}
	if s != nil {
		t.Errorf("Open returned a store (%v) as well as an error", s)
	}
}
