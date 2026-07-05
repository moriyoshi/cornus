package hostenv

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Propagation values returned by Mapper.Propagation, named for the mount(8)
// spellings an operator would use in a `-v src:dst:<mode>` bind or a
// `mount --make-<mode>` call.
const (
	// PropagationShared is a mount whose events propagate BOTH ways (rshared).
	// A mount created under it inside this container becomes visible on the
	// host, which is what the client-local-mount fast path needs.
	PropagationShared = "shared"
	// PropagationSlave receives the peer group's events but does not send its
	// own (rslave). A mount created here stays invisible to the host.
	PropagationSlave = "slave"
	// PropagationPrivate is isolated in both directions, the kernel default.
	PropagationPrivate = "private"
	// PropagationUnknown means the mount backing the path could not be found,
	// e.g. /proc/self/mountinfo is unreadable (non-Linux, or a locked-down
	// procfs).
	PropagationUnknown = "unknown"
)

// mountEntry is the subset of one /proc/self/mountinfo line we use.
type mountEntry struct {
	// root is the path of this mount within its source filesystem (field 4).
	// For a Docker-managed bind of a per-container file it carries the
	// container id, which is what selfIDsFromMountinfo mines.
	root string
	// mountPoint is where it is mounted in THIS mount namespace (field 5).
	mountPoint string
	// propagation is derived from the optional fields (field 7).
	propagation string
	// fsType is the filesystem type (field 9, after the "-" separator).
	fsType string
	// source is the mount source (field 10).
	source string
}

// parseMountinfo parses /proc/self/mountinfo content. Malformed lines are
// skipped rather than failing the parse: this feeds diagnostics and a
// best-effort path map, and one unparseable line from a future kernel must not
// blind us to every other mount.
//
// Line shape (mount ID, parent ID, major:minor, root, mount point, options,
// then zero or more optional "tag[:value]" fields, a "-" separator, fs type,
// source, super options):
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw
func parseMountinfo(content string) []mountEntry {
	var entries []mountEntry
	sc := bufio.NewScanner(strings.NewReader(content))
	// mountinfo lines are short, but a deeply nested path can exceed the
	// default 64KiB token; give the scanner room rather than silently
	// truncating the table.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 7 {
			continue
		}
		// Find the "-" separator that ends the variable-length optional fields.
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || len(fields) < sep+3 {
			continue
		}
		entries = append(entries, mountEntry{
			root:        unescapeOctal(fields[3]),
			mountPoint:  unescapeOctal(fields[4]),
			propagation: propagationFromOptional(fields[6:sep]),
			fsType:      fields[sep+1],
			source:      unescapeOctal(fields[sep+2]),
		})
	}
	return entries
}

// propagationFromOptional maps mountinfo's optional fields to a propagation
// mode. "shared:N" marks a member of a peer group that SENDS events, which is
// the only mode where a mount we create becomes visible to the host, so it wins
// over a simultaneous "master:N" (a mount can be both shared and slave).
func propagationFromOptional(optional []string) string {
	prop := PropagationPrivate
	for _, f := range optional {
		switch {
		case strings.HasPrefix(f, "shared:"):
			return PropagationShared
		case strings.HasPrefix(f, "master:"):
			prop = PropagationSlave
		}
	}
	return prop
}

// unescapeOctal decodes the \OOO escapes the kernel writes for space, tab,
// newline and backslash in mountinfo paths, so a path containing a space
// compares equal to the one a caller passes in.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			v := (s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0')
			b.WriteByte(v)
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// findMount returns the mountinfo entry backing path: the one whose mount point
// is the longest prefix of it. Returns false when the table is empty or nothing
// matches (which cannot happen on a live Linux system, where "/" always does).
func findMount(entries []mountEntry, path string) (mountEntry, bool) {
	path = filepath.Clean(path)
	best := -1
	for i, e := range entries {
		if !pathHasPrefix(path, e.mountPoint) {
			continue
		}
		if best < 0 || len(e.mountPoint) > len(entries[best].mountPoint) {
			best = i
		}
	}
	if best < 0 {
		return mountEntry{}, false
	}
	return entries[best], true
}

// hasMountPoint reports whether path is itself a mount point in this namespace.
// Distinct from findMount, which asks which mount BACKS a path and so always
// succeeds (the root mount backs everything). Confirming our own container
// needs this stricter question: a runtime-managed bind or volume appears as its
// own mountPoint entry, so "the root mount backs it" is no evidence at all.
//
// The symlink-resolved spelling is tried as well, because the runtime and the
// kernel do not always agree on how to write the same destination. Docker
// reports a socket bind as /var/run/docker.sock while mountinfo records
// /run/docker.sock, since /var/run is a symlink in most images — and a
// container whose ONLY mount is that socket would otherwise fail to confirm
// itself, silently losing path translation on the most ordinary setup there is.
func hasMountPoint(entries []mountEntry, path string) bool {
	candidates := []string{filepath.Clean(path)}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != candidates[0] {
		candidates = append(candidates, resolved)
	}
	for _, e := range entries {
		mp := filepath.Clean(e.mountPoint)
		for _, c := range candidates {
			if mp == c {
				return true
			}
		}
	}
	return false
}

// pathHasPrefix reports whether path is prefix or lies beneath it, comparing
// whole path components so /var/lib/cornus-old is not treated as living under
// /var/lib/cornus.
func pathHasPrefix(path, prefix string) bool {
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	prefix = strings.TrimSuffix(prefix, "/")
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// readMountinfo reads and parses procRoot/self/mountinfo, returning nil when it
// cannot be read (non-Linux, or a procfs that hides it).
func readMountinfo(procRoot string) []mountEntry {
	b, err := os.ReadFile(filepath.Join(procRoot, "self", "mountinfo"))
	if err != nil {
		return nil
	}
	return parseMountinfo(string(b))
}
