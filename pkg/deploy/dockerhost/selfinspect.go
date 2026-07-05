package dockerhost

import (
	"context"
	"encoding/json"
	"net/http"

	"cornus/pkg/hostenv"
)

// SelfInspector returns a hostenv.Inspector backed by this host's Docker Engine
// API, so a containerized cornus can ask the daemon what THIS container's
// mounts are and derive the host paths it must hand back to that same daemon.
//
// It deliberately reuses the backend's own engine client rather than dialing
// separately: whatever DOCKER_HOST resolution, transport and tracing the deploy
// path uses, self-inspection uses too, so the two can never disagree about
// which daemon "the runtime" means.
//
// The returned Inspector performs no I/O until called, and hostenv only calls
// it when the process looks containerized — so a server running on the host
// never touches the socket for this.
func SelfInspector() (hostenv.Inspector, error) {
	c, err := newEngineClient()
	if err != nil {
		return nil, err
	}
	return c.selfInspect, nil
}

// selfInspect fetches the subset of GET /containers/{id}/json that hostenv
// needs to build a path map and confirm the container is really us.
//
// Mounts[] is the load-bearing field: Docker reports each bind and volume as
// {Source (a HOST path), Destination (a path in this container)}, which is
// exactly the correspondence a containerized cornus has no other way to learn.
func (c *engineClient) selfInspect(ctx context.Context, id string) (hostenv.SelfInspect, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil)
	if err != nil {
		return hostenv.SelfInspect{}, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return hostenv.SelfInspect{}, err
	}
	var raw struct {
		ID     string `json:"Id"`
		Config struct {
			Hostname string `json:"Hostname"`
		} `json:"Config"`
		HostConfig struct {
			NetworkMode string `json:"NetworkMode"`
		} `json:"HostConfig"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return hostenv.SelfInspect{}, err
	}
	self := hostenv.SelfInspect{
		ID:          raw.ID,
		Hostname:    raw.Config.Hostname,
		NetworkMode: raw.HostConfig.NetworkMode,
	}
	for _, m := range raw.Mounts {
		self.Mounts = append(self.Mounts, hostenv.MountPoint{Source: m.Source, Destination: m.Destination})
	}
	return self, nil
}
