package dockerhost

// Short image names, and why the podman engine has to qualify them itself.
//
// Podman refuses an unqualified reference outright:
//
//	short-name "nginx:alpine" did not resolve to an alias and no
//	unqualified-search registries are defined in "/etc/containers/registries.conf"
//
// That is deliberate on podman's part — the CLI prompts the operator to choose a
// registry, and a daemon has nobody to ask. Docker, containerd's resolver and
// Kubernetes all silently mean Docker Hub instead.
//
// Cornus cannot inherit podman's answer, because a deploy spec is portable across
// all six backends by design. `image: nginx:alpine` deploys on the other five; if
// it failed on podman alone, the spec would no longer describe a deployment, it
// would describe a deployment AND a runtime. The remedy podman offers is
// /etc/containers/registries.conf — a file on the DAEMON's host that cornus does
// not own and, over a tcp:// or ssh:// endpoint, cannot even see. The same
// reasoning already put tlsVerify on the pull query rather than in that file.
//
// So the engine qualifies the reference itself, to exactly what podman would
// produce with `unqualified-search-registries = ["docker.io"]` — the setting
// almost every distribution ships. Nothing is guessed and nothing is hidden: the
// name that reaches podman is the name `podman images` will show.

import "strings"

// dockerHubRegistry is the host short names resolve against, matching every other
// cornus backend and the default in the containers-common package.
const dockerHubRegistry = "docker.io"

// qualifyImageRef returns ref with an explicit registry host, leaving an already
// qualified reference untouched.
//
// The test for "already qualified" is the one containers/image and Docker both
// use: the first path component is a registry if it contains a "." or a ":", or
// is exactly "localhost". Everything else is a Docker Hub namespace.
//
// That test is load-bearing in BOTH directions here, and getting it wrong is
// silent either way. Cornus serves its build output from a loopback registry, so
// refs like "127.0.0.1:39715/demo:latest" and "localhost:5000/app" flow through
// this function constantly; rewriting one of those to docker.io/... would send
// the pull to Docker Hub looking for an image that only ever existed locally.
// Conversely, leaving a bare "nginx" alone is the failure this exists to prevent.
//
// A digest-only reference ("sha256:...") has no registry to add and is returned
// unchanged — resolving it is the caller's problem, not this one's.
func qualifyImageRef(ref string) string {
	if ref == "" {
		return ref
	}
	// A bare digest is not a name; adding a host would corrupt it.
	if strings.HasPrefix(ref, "sha256:") {
		return ref
	}
	first, rest, hasSlash := strings.Cut(ref, "/")
	if hasSlash && isRegistryHost(first) {
		return ref
	}
	if !hasSlash {
		// Single component: a Docker Hub official image, which lives under the
		// implicit "library" namespace. "nginx" -> "docker.io/library/nginx".
		return dockerHubRegistry + "/library/" + ref
	}
	// Two or more components with a non-host first one: a Hub user namespace.
	// "bitnami/redis" -> "docker.io/bitnami/redis".
	return dockerHubRegistry + "/" + first + "/" + rest
}

// isRegistryHost reports whether a leading path component names a registry rather
// than a Docker Hub namespace.
func isRegistryHost(s string) bool {
	return s == "localhost" || strings.ContainsAny(s, ".:")
}
