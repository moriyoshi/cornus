package hostenv

import (
	"regexp"
	"strings"
)

// Resolving our OWN container id is a guess with several independent sources,
// none individually authoritative:
//
//   - mountinfo is the most reliable on Docker, because the daemon bind-mounts
//     per-container files (/etc/hosts, /etc/hostname, /etc/resolv.conf) whose
//     mountinfo root carries the id;
//   - the cgroup path carries it under cgroup v1 and under systemd-managed
//     cgroup v2, but a plain cgroup v2 container sees only "0::/";
//   - HOSTNAME is the short id by default, but any `--hostname` or a
//     Kubernetes pod name overwrites it.
//
// So we collect candidates in that order and let the caller confirm one by
// inspecting it (see confirmSelf): a wrong guess is rejected rather than
// trusted, which matters because the whole path map is derived from whatever
// container we decide we are.

var (
	// reMountinfoID matches the container id in a Docker-managed per-container
	// bind source, e.g. /var/lib/docker/containers/<id>/hosts. The
	// "/containers/" anchor is load-bearing: overlay2 layer directories are
	// also 64 hex chars, and matching those would yield a confident wrong id.
	reMountinfoID = regexp.MustCompile(`/containers/([0-9a-f]{64})(?:/|$)`)
	// reCgroupID matches the id in a cgroup path, covering the plain
	// (/docker/<id>) and systemd (docker-<id>.scope, cri-containerd-<id>.scope,
	// crio-<id>.scope) spellings.
	reCgroupID = regexp.MustCompile(`(?:^|/)(?:docker[-/]|crio-|cri-containerd[-:]|libpod-)?([0-9a-f]{64})(?:\.scope)?(?:/|$)`)
	// reShortID matches a 12-hex-digit short id, the default HOSTNAME Docker
	// gives a container.
	reShortID = regexp.MustCompile(`^[0-9a-f]{12}$`)
)

// selfIDsFromMountinfo mines container ids out of the mount table's root and
// source fields.
func selfIDsFromMountinfo(entries []mountEntry) []string {
	var ids []string
	for _, e := range entries {
		for _, field := range []string{e.root, e.source} {
			if m := reMountinfoID.FindStringSubmatch(field); m != nil {
				ids = append(ids, m[1])
			}
		}
	}
	return dedupe(ids)
}

// selfIDsFromCgroup mines container ids out of /proc/self/cgroup content.
func selfIDsFromCgroup(content string) []string {
	var ids []string
	for _, line := range strings.Split(content, "\n") {
		// A cgroup line is "hierarchy:controllers:path"; only the path can
		// carry an id.
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, seg := range strings.Split(parts[2], "/") {
			if m := reCgroupID.FindStringSubmatch("/" + seg); m != nil {
				ids = append(ids, m[1])
			}
		}
	}
	return dedupe(ids)
}

// selfIDFromHostname returns HOSTNAME when it looks like a Docker short id.
// Anything else (an operator's --hostname, a Kubernetes pod name) is not a
// candidate at all rather than a guess to be confirmed.
func selfIDFromHostname(hostname string) string {
	if reShortID.MatchString(hostname) {
		return hostname
	}
	return ""
}

// selfIDCandidates orders every candidate id most-reliable first, deduped.
func selfIDCandidates(mounts []mountEntry, cgroup, hostname string) []string {
	ids := append([]string(nil), selfIDsFromMountinfo(mounts)...)
	ids = append(ids, selfIDsFromCgroup(cgroup)...)
	if id := selfIDFromHostname(hostname); id != "" {
		ids = append(ids, id)
	}
	return dedupe(ids)
}

func dedupe(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
