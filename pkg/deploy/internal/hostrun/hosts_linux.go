//go:build linux

package hostrun

// Inter-container name resolution, nerdctl-style: the bridge CNI has no embedded
// DNS, so every instance gets a backend-managed /etc/hosts bind-mounted from
// <DataDir>/<subdir>/hosts/<instance>, carrying a marker-delimited block the
// backend rewrites on every deploy/delete with one line per peer reachable on a
// shared network. The only per-backend part is WHERE the peer table comes from
// (barehost from its instance records, containerd from container labels), so the
// backend builds a []HostsPeer and hands it to SyncHosts.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	HostsMarkerBegin = "# <cornus-managed-hosts-begin> entries below are rewritten by cornus; do not edit"
	HostsMarkerEnd   = "# <cornus-managed-hosts-end>"
)

// HostsStore owns the per-instance hosts files under <DataDir>/<subdir>/hosts/.
type HostsStore struct {
	mu        sync.Mutex
	dir       string
	errPrefix string
}

// NewHostsStore builds a store rooted at <dataDir>/<subdir>/hosts; errPrefix
// ("bare"/"containerd") heads its errors.
func NewHostsStore(dataDir, subdir, errPrefix string) *HostsStore {
	return &HostsStore{dir: filepath.Join(dataDir, subdir, "hosts"), errPrefix: errPrefix}
}

func (h *HostsStore) Path(id string) string { return filepath.Join(h.dir, id) }

// Create seeds an instance's hosts file (localhost entries, its own hostname at
// its primary IP, an empty managed block) and returns its path for the
// /etc/hosts bind mount.
func (h *HostsStore) Create(id, hostname, ip string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		return "", fmt.Errorf("%s: create hosts dir: %w", h.errPrefix, err)
	}
	var sb strings.Builder
	sb.WriteString("127.0.0.1\tlocalhost\n")
	sb.WriteString("::1\tlocalhost ip6-localhost ip6-loopback\n")
	sb.WriteString("fe00::0\tip6-localnet\n")
	sb.WriteString("ff00::0\tip6-mcastprefix\n")
	sb.WriteString("ff02::1\tip6-allnodes\n")
	sb.WriteString("ff02::2\tip6-allrouters\n")
	if ip != "" && hostname != "" {
		fmt.Fprintf(&sb, "%s\t%s\n", ip, hostname)
	}
	sb.WriteString(HostsMarkerBegin + "\n")
	sb.WriteString(HostsMarkerEnd + "\n")
	path := h.Path(id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return "", fmt.Errorf("%s: write hosts file: %w", h.errPrefix, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("%s: install hosts file: %w", h.errPrefix, err)
	}
	return path, nil
}

// Remove deletes an instance's hosts file (best-effort).
func (h *HostsStore) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = os.Remove(h.Path(id))
}

// sync rewrites the managed block of an instance's hosts file with lines,
// preserving everything outside the markers. The update is written in-place (not
// temp+rename) because the file backs a live bind mount — a rename would swap the
// inode out from under running containers.
func (h *HostsStore) sync(id string, lines []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := h.Path(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	next := spliceManagedSection(string(data), lines)
	if next == string(data) {
		return nil
	}
	return os.WriteFile(path, []byte(next), 0o644)
}

// spliceManagedSection replaces the marker-delimited block of content with lines
// (appending a fresh block when the markers are missing or mangled).
func spliceManagedSection(content string, lines []string) string {
	var sb strings.Builder
	sb.WriteString(HostsMarkerBegin + "\n")
	for _, l := range lines {
		sb.WriteString(l + "\n")
	}
	sb.WriteString(HostsMarkerEnd + "\n")
	section := sb.String()

	begin := strings.Index(content, HostsMarkerBegin)
	end := strings.Index(content, HostsMarkerEnd)
	if begin < 0 || end < begin {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + section
	}
	tail := content[end:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	} else {
		tail = ""
	}
	return content[:begin] + section + tail
}

// HostsPeer is one managed instance's name-resolution record, decoded by each
// backend from its own store (records / labels).
type HostsPeer struct {
	ID       string
	App      string
	Replica  int
	Networks []string
	IPs      map[string]string   // network -> IP
	Aliases  map[string][]string // network -> aliases
}

// hostsEntry is the address one app publishes on one network: the IP of its
// lowest-index replica, named by the service name and its aliases there.
type hostsEntry struct {
	ip      string
	names   []string
	replica int
}

// SyncHosts rewrites the managed /etc/hosts block of every peer so services
// resolve each other by name (and aliases) on shared networks. A multi-replica
// app publishes replica 0's IP (hosts files have no round-robin).
func SyncHosts(store *HostsStore, peers []HostsPeer) error {
	// Per network, each app resolves to its lowest-index replica with an IP there.
	table := map[string]map[string]hostsEntry{}
	for _, p := range peers {
		for _, nw := range p.Networks {
			ip := p.IPs[nw]
			if ip == "" {
				continue
			}
			t := table[nw]
			if t == nil {
				t = map[string]hostsEntry{}
				table[nw] = t
			}
			if cur, ok := t[p.App]; ok && cur.replica <= p.Replica {
				continue
			}
			t[p.App] = hostsEntry{
				ip:      ip,
				names:   append([]string{p.App}, p.Aliases[nw]...),
				replica: p.Replica,
			}
		}
	}
	var firstErr error
	for _, p := range peers {
		if err := store.sync(p.ID, hostsLines(p, table)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// hostsLines renders one instance's managed block: for each of its networks
// (sorted), one "<ip>\t<app> <aliases...>" line per member app (sorted).
func hostsLines(p HostsPeer, table map[string]map[string]hostsEntry) []string {
	var lines []string
	seen := map[string]bool{}
	nws := append([]string(nil), p.Networks...)
	sort.Strings(nws)
	for _, nw := range nws {
		t := table[nw]
		apps := make([]string, 0, len(t))
		for app := range t {
			apps = append(apps, app)
		}
		sort.Strings(apps)
		for _, app := range apps {
			e := t[app]
			line := e.ip + "\t" + strings.Join(e.names, " ")
			if !seen[line] {
				seen[line] = true
				lines = append(lines, line)
			}
		}
	}
	return lines
}
