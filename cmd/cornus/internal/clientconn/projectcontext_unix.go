//go:build unix

package clientconn

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// untrustedProvenance returns a non-empty reason when the file at path shows a
// tampering signal on Unix, or "" when it looks trustworthy. It flags a file owned
// by another user (someone else planted it) or one sitting in a world-writable,
// non-sticky directory (anyone may replace it). A file that cannot be stat'd is
// treated as trustworthy here so a missing explicit --context-file surfaces as the
// loader's clean "no such file" error rather than a provenance skip. The result is
// advisory: --trust-context-file bypasses this check entirely.
func untrustedProvenance(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	uid := os.Getuid()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && uid != 0 && int(st.Uid) != uid {
		return fmt.Sprintf("file is owned by uid %d, not you (uid %d)", st.Uid, uid)
	}
	dfi, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return ""
	}
	// A world-writable directory lets anyone plant/replace the file. The sticky bit
	// (as on /tmp) confines deletion to the owner, so a same-owner file there is
	// still fine — a foreign file is already caught by the ownership check above.
	if dfi.Mode().Perm()&0o002 != 0 && dfi.Mode()&os.ModeSticky == 0 {
		return "containing directory is world-writable"
	}
	return ""
}
