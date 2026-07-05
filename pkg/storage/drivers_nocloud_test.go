//go:build !cloudblob

package storage

import (
	"context"
	"strings"
	"testing"
)

// TestGSAzblobUnsupportedInDefaultBuild proves the default build returns a clear,
// actionable error for gs:// / azblob:// rather than gocloud's raw "no driver
// registered" message.
func TestGSAzblobUnsupportedInDefaultBuild(t *testing.T) {
	for _, tc := range []struct{ ref, scheme string }{
		{"gs://bucket", "gs"},
		{"azblob://container", "azblob"},
	} {
		_, err := Open(context.Background(), tc.ref, t.TempDir())
		if err == nil {
			t.Fatalf("Open(%q) succeeded, want unsupported-in-this-build error", tc.ref)
		}
		if !strings.Contains(err.Error(), "-tags cloudblob") {
			t.Fatalf("Open(%q) err = %v, want a message pointing at -tags cloudblob", tc.ref, err)
		}
	}
}
