// Package imageref classifies and rewrites image references by their registry
// part. A "bare" reference has no registry part (e.g. "app:v1" or
// "team/app:v1"); for fidelity with Docker, where a bare reference belongs to
// the default registry, cornus treats it as belonging to its own builtin
// registry rather than Docker Hub. The build CLI, the compose CLI, and the
// docker-compat endpoint share these helpers so bare tags land in, and resolve
// from, the builtin registry.
package imageref

import "strings"

// IsBare reports whether ref lacks an explicit registry host, applying Docker's
// splitDockerDomain rule: the component before the first '/' is a registry host
// only if it contains '.' or ':' or equals "localhost". A ref with no '/' (e.g.
// "app:v1") is always bare. An explicit "docker.io/library/nginx" is therefore
// NOT bare (its leading component contains '.'), so explicit Docker Hub refs are
// left untouched, while "app:v1", "team/app:v1", and "app@sha256:..." are bare.
func IsBare(ref string) bool {
	i := strings.IndexByte(ref, '/')
	if i < 0 {
		return true
	}
	host := ref[:i]
	return host != "localhost" && !strings.ContainsAny(host, ".:")
}

// QualifyBare prepends registryHost to ref when ref is bare (no registry host)
// and registryHost is non-empty; otherwise ref is returned unchanged. Prepending
// the host is sufficient for bare refs that carry a ":tag" or "@digest" suffix.
func QualifyBare(ref, registryHost string) string {
	if registryHost == "" || !IsBare(ref) {
		return ref
	}
	return registryHost + "/" + ref
}

// SplitHostRepo splits an image name (a reference without a tag/digest suffix,
// as Docker sends it on the push route) into its registry host and repository
// path. A bare name yields an empty host and the whole name as the repository —
// it deliberately does NOT apply Docker Hub's "library/" normalization, so a
// bare "app" stays "app" (the repository path under the builtin registry).
func SplitHostRepo(name string) (host, repo string) {
	if IsBare(name) {
		return "", name
	}
	i := strings.IndexByte(name, '/')
	return name[:i], name[i+1:]
}
