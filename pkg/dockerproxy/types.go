// Package dockerproxy is a local daemon that speaks a subset of the Docker
// Engine REST API and translates container operations into cornus deploys
// against a remote cornus server. Bind mounts whose source is a local
// directory are streamed to the server over 9P (via the deploy-attach path), so
// stock `docker` (with DOCKER_HOST pointed at the proxy socket) can run
// workloads on a remote cornus with the caller's local files mounted in.
//
// The wire structs here are hand-rolled (the subset the proxy needs) rather than
// imported from a specific moby types version — the same philosophy as
// pkg/deploy/dockerhost/engine.go.
//
// First slice: `docker run -d [-v local:ctr[:ro]] [-p] [-e] IMAGE`, `docker ps`,
// `docker inspect`, `docker stop`, `docker rm`, plus compose (networks/volumes/
// events), logs, stats, cp/archive, and interactive `docker exec`/`docker attach`
// (hijacked raw stdio tunnels). Build is deferred.
package dockerproxy

// createRequest is the Docker POST /containers/create body (subset).
type createRequest struct {
	Image      string   `json:"Image"`
	Cmd        []string `json:"Cmd"`
	Entrypoint []string `json:"Entrypoint"`
	Env        []string `json:"Env"`
	// Tty is `docker run -t`. It has to be recorded at CREATE because it is a
	// property of the container, not of the later attach: the backend allocates
	// the pseudo-TTY when it creates the container, and the CLI decides how to
	// decode the attach stream (raw for a TTY, stdcopy-multiplexed otherwise)
	// from the Config.Tty it reads back out of inspect. Dropping it here made
	// every proxied container non-TTY, so `docker run -t` produced a container
	// whose programs saw a pipe.
	Tty              bool                `json:"Tty"`
	Labels           map[string]string   `json:"Labels"`
	ExposedPorts     map[string]struct{} `json:"ExposedPorts"`
	HostConfig       hostConfig          `json:"HostConfig"`
	NetworkingConfig networkingConfig    `json:"NetworkingConfig"`
}

// networkingConfig carries the networks a container is attached to at create
// (compose sets EndpointsConfig keyed by network name).
type networkingConfig struct {
	EndpointsConfig map[string]endpointConfig `json:"EndpointsConfig"`
}

// endpointConfig is the per-network endpoint settings sent at create; only the
// DNS aliases matter to cornus (compose includes the service name here).
type endpointConfig struct {
	Aliases []string `json:"Aliases"`
}

type hostConfig struct {
	Binds         []string                 `json:"Binds"`
	Mounts        []mountPoint             `json:"Mounts"`
	PortBindings  map[string][]portBinding `json:"PortBindings"`
	RestartPolicy restartPolicy            `json:"RestartPolicy"`
}

type mountPoint struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly"`
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type restartPolicy struct {
	Name string `json:"Name"`
	// MaximumRetryCount is the attempt cap the CLI parses out of
	// `--restart=on-failure:N`; Docker sends it alongside Name and it is
	// meaningless (and rejected) for any other policy. It maps onto
	// api.DeploySpec.RestartMaxAttempts.
	MaximumRetryCount int `json:"MaximumRetryCount"`
}

type createResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// containerSummary is one element of GET /containers/json (docker ps).
// NetworkSettings mirrors dockerd's types.SummaryNetworkSettings — just the
// Networks map keyed by network name — and is always populated: compose v5's
// convergence (checkExpectedNetworks) nil-derefs it when diffing a running
// container and uses its keys as the container's network membership.
type containerSummary struct {
	ID              string            `json:"Id"`
	Names           []string          `json:"Names"`
	Image           string            `json:"Image"`
	State           string            `json:"State"`
	Status          string            `json:"Status"`
	Labels          map[string]string `json:"Labels"`
	Mounts          []mountJSON       `json:"Mounts"`
	NetworkSettings map[string]any    `json:"NetworkSettings"`
}

// containerJSON is GET /containers/{id}/json (docker inspect). NetworkSettings
// and HostConfig are always populated (non-nil): compose dereferences
// NetworkSettings.Networks after create and panics if it is nil.
type containerJSON struct {
	ID              string         `json:"Id"`
	Name            string         `json:"Name"`
	Created         string         `json:"Created"`
	Image           string         `json:"Image"`
	State           stateJSON      `json:"State"`
	Config          configJSON     `json:"Config"`
	Mounts          []mountJSON    `json:"Mounts"`
	NetworkSettings map[string]any `json:"NetworkSettings"`
	HostConfig      map[string]any `json:"HostConfig"`
}

type stateJSON struct {
	Status    string `json:"Status"` // created|running|exited
	Running   bool   `json:"Running"`
	ExitCode  int    `json:"ExitCode"`
	StartedAt string `json:"StartedAt"`
}

// waitResponse is the body of POST /containers/{id}/wait (dockerd's
// container.WaitResponse). Error is Docker's own channel for "the daemon cannot
// tell you how this ended"; the docker CLI turns a response carrying it into
// exit status 125 rather than trusting StatusCode. It is the only place in the
// Docker API where an unknown exit status can be expressed at all, which is why
// the proxy uses it instead of reporting a 0 it does not know to be true.
type waitResponse struct {
	StatusCode int        `json:"StatusCode"`
	Error      *waitError `json:"Error,omitempty"`
}

type waitError struct {
	Message string `json:"Message"`
}

type configJSON struct {
	Image      string            `json:"Image"`
	Cmd        []string          `json:"Cmd"`
	Entrypoint []string          `json:"Entrypoint"`
	Env        []string          `json:"Env"`
	Tty        bool              `json:"Tty"`
	Labels     map[string]string `json:"Labels"`
}

type mountJSON struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}
