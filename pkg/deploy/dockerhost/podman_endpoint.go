package dockerhost

// How cornus reaches Podman's API — an explicit operator choice, never inferred.
//
// Selecting CORNUS_DEPLOY_BACKEND=podman says WHICH runtime. A second, separate
// variable says HOW to reach it, and if neither is set the server refuses to
// start rather than going looking:
//
//	CORNUS_PODMAN_SOCKET=<path|url>  use exactly this endpoint
//	CORNUS_PODMAN_SERVICE=1          cornus runs `podman system service` itself
//
// Deliberately absent: CONTAINER_HOST, DOCKER_HOST, and probing the stock
// rootless ($XDG_RUNTIME_DIR/podman/podman.sock) and rootful
// (/run/podman/podman.sock) paths. Ambient discovery is what makes "which daemon
// did it actually talk to?" unanswerable from a bug report, and this package
// already refuses to infer this class of thing elsewhere — see WithRemote, whose
// comment says co-location "is always an explicit operator choice, never
// inferred". An operator who wants CONTAINER_HOST honored writes
// CORNUS_PODMAN_SOCKET=$CONTAINER_HOST: one line, and it leaves a record.
//
// The self-spawned service exists because enabling podman.socket is a step an
// operator may not want, or may not be able, to take. Podman stays daemonless
// either way — the socket-activated unit and this child process both start a
// service on demand; the difference is only who owns its lifecycle.

import (
	"fmt"
	"os"
	"strings"

	"cornus/pkg/runtimeendpoint"
)

// Podman endpoint selectors. Both are read only when the podman flavor is
// selected; neither affects the dockerhost flavor.
const (
	envPodmanSocket  = "CORNUS_PODMAN_SOCKET"
	envPodmanService = "CORNUS_PODMAN_SERVICE"
)

// podmanAccess is the resolved answer to "how do we reach Podman".
type podmanAccess struct {
	// Endpoint is set when the operator named a socket directly.
	Endpoint runtimeendpoint.Endpoint
	// Spawn is true when cornus must start `podman system service` itself, in
	// which case Endpoint is not yet meaningful — the socket does not exist until
	// the service is started (see podman_service.go).
	Spawn bool
	// SSH holds the raw ssh:// destination when the operator named one. Resolving
	// it opens a live SSH connection, so it needs a context and cannot happen in
	// this pure environment read.
	SSH string
}

// resolvePodmanAccess reads the two selectors and reports how to reach Podman.
//
// Setting both is an error rather than a precedence rule. A precedence would
// silently ignore one of two things the operator explicitly asked for, and which
// one gets ignored is exactly the kind of detail nobody remembers under an
// incident.
func resolvePodmanAccess(getenv func(string) string) (podmanAccess, error) {
	sock := strings.TrimSpace(getenv(envPodmanSocket))
	svc := strings.TrimSpace(getenv(envPodmanService))

	switch {
	case sock != "" && svc != "":
		return podmanAccess{}, fmt.Errorf(
			"%s and %s are both set; they are alternatives — %s names a socket to connect to, "+
				"%s makes cornus start `podman system service` itself. Unset one",
			envPodmanSocket, envPodmanService, envPodmanSocket, envPodmanService)

	case sock != "" && isSSHEndpoint(sock):
		// Validated eagerly for shape; the connection itself is opened by
		// podmanSSHEndpoint, which needs a context.
		return podmanAccess{SSH: sock}, nil

	case sock != "":
		ep, err := runtimeendpoint.Parse(normalizePodmanSocket(sock), "")
		if err != nil {
			return podmanAccess{}, fmt.Errorf("%s: %w", envPodmanSocket, err)
		}
		return podmanAccess{Endpoint: ep}, nil

	case svc != "":
		return podmanAccess{Spawn: true}, nil

	default:
		return podmanAccess{}, fmt.Errorf(
			"the podman backend needs to be told how to reach Podman: set %s to the API socket "+
				"(enable it with `systemctl --user enable --now podman.socket`, or `systemctl enable "+
				"--now podman.socket` for a rootful daemon), or set %s=1 to have cornus run "+
				"`podman system service` itself. Neither is inferred: cornus does not probe "+
				"CONTAINER_HOST, DOCKER_HOST, or the stock socket paths, so that which daemon it "+
				"drove is always answerable from the configuration",
			envPodmanSocket, envPodmanService)
	}
}

// normalizePodmanSocket accepts the spellings an operator actually types.
//
// `podman system connection list` prints URLs (unix:///run/user/1000/podman/
// podman.sock), while a person reading their own filesystem types the bare path.
// Both mean the same thing and neither should be a startup failure. Anything
// already carrying a scheme is passed through untouched so runtimeendpoint can
// reject the schemes it does not support, with its own message.
func normalizePodmanSocket(v string) string {
	if strings.HasPrefix(v, "/") {
		return "unix://" + v
	}
	return v
}

// podmanEnvOrOS adapts os.Getenv to the getenv parameter resolvePodmanAccess
// takes, which exists so the resolution can be tested without mutating process
// state.
func podmanEnvOrOS(name string) string { return os.Getenv(name) }

// ValidatePodmanAccess reports whether the podman endpoint SELECTION is
// coherent, without touching the network.
//
// It exists because the server constructs its backend lazily and deliberately —
// "so the server can start even when no Docker host is reachable" — which is
// right for a daemon that is merely down, and wrong for a configuration that can
// never work. "The socket is unreachable" may resolve on its own; "neither
// selector is set" will not, and podman has no default endpoint to fall back to.
//
// So the static half is checked eagerly at startup while the I/O stays lazy: an
// operator who forgot the variable is told at boot rather than at their first
// deploy, and a server whose podman happens to be stopped still starts and
// serves its registry.
func ValidatePodmanAccess() error {
	_, err := resolvePodmanAccess(podmanEnvOrOS)
	return err
}
