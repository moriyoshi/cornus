package server

import "os"

// remoteModeEnvs names, per deploy backend, the environment variable that opts
// that backend into its always-on caretaker-companion ("remote") mode — the
// mode in which client-local mounts, port-forward, and exec agent-forwarding
// are realized through a companion container instead of a co-located fast path.
//
// It is a map rather than four scattered os.Getenv calls because the name is
// needed in two places that must not disagree: defaultBackendFactory, which
// READS it to construct the backend, and clientLocalMountsUnavailable, which
// NAMES it in the error a user sees when the mode is off and the feature they
// asked for needs it. A message naming a variable the factory does not read is
// worse than no message — it sends the operator to set something with no
// effect, and nothing anywhere reports that the advice was wrong.
//
// kubernetes is absent deliberately: it has no co-located path to be an
// alternative to, so its caretaker sidecar is unconditional and there is no
// mode to opt into.
var remoteModeEnvs = map[string]string{
	"dockerhost": "CORNUS_DOCKER_REMOTE",
	"podman":     "CORNUS_PODMAN_REMOTE",
	"containerd": "CORNUS_CONTAINERD_REMOTE",
	"bare":       "CORNUS_BARE_REMOTE",
	"incus":      "CORNUS_INCUS_REMOTE",
}

// remoteModeEnabled reports whether the named backend's remote mode is on.
//
// Any non-empty value enables it, including "0" and "false" — that is the
// pre-existing convention for these four variables and is left alone here. The
// value is deliberately NOT trimmed: unlike the structural variables in env.go,
// this one is never dialed, opened, or compared, so whitespace cannot travel
// past this predicate and reappear as a malformed URL somewhere else.
func remoteModeEnabled(backend string) bool {
	name := remoteModeEnvs[backend]
	return name != "" && os.Getenv(name) != ""
}
