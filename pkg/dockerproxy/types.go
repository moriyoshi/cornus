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
	Image            string              `json:"Image"`
	Cmd              []string            `json:"Cmd"`
	Entrypoint       []string            `json:"Entrypoint"`
	Env              []string            `json:"Env"`
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

type configJSON struct {
	Image      string            `json:"Image"`
	Cmd        []string          `json:"Cmd"`
	Entrypoint []string          `json:"Entrypoint"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
}

type mountJSON struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}
