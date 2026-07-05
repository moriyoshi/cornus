package dockerhost

// Network and inspection Engine methods for the libpod engine.
//
// libpod's network API is the least Docker-like corner of the whole surface. The
// create body is snake_case where volumes and inspect are PascalCase, connect
// takes a snake_case body while DISCONNECT takes a Docker-shaped PascalCase one
// (that route is wired to the compat handler), and create returns 200 where the
// compat endpoint returns 201. None of that is inferred — see
// .agents/docs/LTM/podman-libpod-api-findings.md.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sort"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// libpodNetwork is the create body — libpod's own types.Network, decoded
// verbatim by the handler. Note the snake_case tags.
type libpodNetwork struct {
	Name             string            `json:"name"`
	Driver           string            `json:"driver,omitempty"`
	Subnets          []libpodSubnet    `json:"subnets,omitempty"`
	IPv6Enabled      bool              `json:"ipv6_enabled,omitempty"`
	Internal         bool              `json:"internal,omitempty"`
	DNSEnabled       bool              `json:"dns_enabled"`
	Labels           map[string]string `json:"labels,omitempty"`
	Options          map[string]string `json:"options,omitempty"`
	NetworkInterface string            `json:"network_interface,omitempty"`
}

type libpodSubnet struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway,omitempty"`
}

// networkEnsure creates the network if it is absent.
//
// **dns_enabled is written explicitly, always true**, and it is the single most
// consequential line in this file.
//
// libpod's create takes the body verbatim and does NOT default it. The CLI sets
// it (`--disable-dns` defaults off) and the Docker-compat endpoint forces it on
// for bridge networks, so every other way of creating a podman network gets DNS
// — but this one does not. Measured: a network created without it resolves peer
// container names as NXDOMAIN, while an otherwise identical one with it resolves
// them.
//
// Cornus's compose user networks depend entirely on container-name resolution,
// so omitting this produces a network that is created successfully, reports
// healthy, passes every structural check, and silently cannot resolve a single
// service name. The regression test for it therefore asserts at the DNS lookup,
// not at this call.
//
// The `json:"dns_enabled"` tag deliberately carries no omitempty: the field must
// appear on the wire even when false, so a future caller that wants it off gets
// what it asked for rather than libpod's default.
func (e *podmanEngine) networkEnsure(ctx context.Context, net api.NetworkAttachment) error {
	body := libpodNetwork{
		Name:        net.Name,
		Driver:      net.Driver,
		Internal:    net.Internal,
		IPv6Enabled: net.EnableIPv6,
		DNSEnabled:  true,
		Labels:      net.Labels,
		Options:     net.DriverOpts,
	}
	// The kubernetes pseudo-drivers are not real network drivers; letting one
	// through would make libpod reject the create for an unknown driver.
	if k8sPseudoDriver[body.Driver] {
		body.Driver = ""
	}
	if net.Subnet != "" {
		body.Subnets = []libpodSubnet{{Subnet: net.Subnet, Gateway: net.Gateway}}
	}

	// ignoreIfExists makes this idempotent server-side, replacing the
	// exists-then-create race the Docker path works around with CheckDuplicate.
	resp, err := e.do(ctx, http.MethodPost, "/networks/create?ignoreIfExists=true", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 409 is the SAME outcome ignoreIfExists asks for, reached the other way.
	// The parameter arrived in libpod 5.0; an older server does not reject an
	// unknown query key, it silently ignores it and answers a duplicate create
	// with 409 Conflict. Measured on podman 4.3.1: every compose re-deploy onto
	// an existing user network failed with "network name X already used".
	//
	// Accepting it is not a workaround for one version — a network that already
	// exists is exactly the state this call exists to establish, so treating the
	// conflict as anything but success would make idempotency depend on which
	// podman happens to be behind the socket.
	//
	// It does inherit ignoreIfExists's own caveat: neither form reconciles an
	// existing network whose settings differ from the request (a network created
	// without dns_enabled stays without it). Correcting it is not ours to do —
	// cornus does not own a network it did not create, and other workloads may be
	// attached — so warnDNSDisabled below reports it instead.
	if resp.StatusCode == http.StatusConflict {
		e.warnDNSDisabled(ctx, net.Name)
		return nil
	}
	// 200, not compat's 201.
	if err := expect(resp, http.StatusOK, http.StatusCreated); err != nil {
		return err
	}
	e.warnDNSDisabled(ctx, net.Name)
	return nil
}

// warnDNSDisabled reports a network that came out of networkEnsure without
// container-name resolution, which is only possible when the network ALREADY
// existed: this file's create always asks for dns_enabled.
//
// It exists because ignoreIfExists (and the 409 that stands in for it on podman
// 4.x) is silent about the difference between "created as asked" and "yours is
// not the one in use". A network created once without DNS — by an older cornus,
// by `podman network create --disable-dns`, or by another tool — then serves
// every future deploy with no name resolution and nothing says so. Measured
// 2026-08-06 while neutralizing compose-dns-resolution.star: a run with a
// deliberately broken cornus left the network behind, and the FIXED cornus
// reused it and failed identically, so the fix looked like no fix at all.
//
// The symptom it converts is the worst kind: the network is present, inspect
// looks plausible, the deploy succeeds, and only service-name lookups fail — so
// it reads as an application bug. Docker does not have this shape (its
// user-defined networks always carry embedded DNS); this is podman-specific.
//
// Deliberately a warning rather than a refusal: a DNS-less network can be what
// the operator wanted, and refusing would break a working setup to fix a
// suspicion. Equally deliberately NOT warn-once — every Apply onto this network
// produces workloads that cannot resolve their peers, so a later deploy must not
// inherit an earlier one's silence.
//
// Nothing here can fail the caller: this is diagnosis, and a network that is
// serving traffic must not be refused because an inspect did not answer.
func (e *podmanEngine) warnDNSDisabled(ctx context.Context, name string) {
	dnsEnabled, driver, err := e.networkDNSState(ctx, name)
	// Unknown means unknown. A libpod that omits the field, or an inspect that
	// did not answer, is not evidence of a misconfigured network, and warning on
	// it would train the reader to ignore the warning that matters.
	if err != nil || dnsEnabled == nil || *dnsEnabled {
		return
	}
	// Only bridge carries netavark's aardvark-dns. A macvlan or ipvlan network
	// has no name resolution BY CONSTRUCTION, so reporting its absence there
	// would be a false alarm on every deploy that uses one.
	if driver != "" && driver != "bridge" {
		return
	}
	logging.FromContext(ctx, slog.Group("dockerhost", "network", name)).
		WarnContext(ctx, "reusing a pre-existing podman network that has DNS disabled: container-name resolution will not work on it, so services on this network cannot reach each other by name and lookups return NXDOMAIN; cornus asks for dns_enabled on every network it creates but does not reconcile one it did not create. Remove the network (podman network rm) and re-deploy so cornus can create it, or attach the workload to a different network",
			"network", name)
}

// networkDNSState reports whether a network resolves container names, and the
// driver that decides whether it could. A nil dnsEnabled means the daemon did
// not say — an older libpod, or a network that is simply gone.
func (e *podmanEngine) networkDNSState(ctx context.Context, name string) (dnsEnabled *bool, driver string, err error) {
	resp, err := e.do(ctx, http.MethodGet, "/networks/"+name+"/json", nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return nil, "", err
	}
	var out struct {
		DNSEnabled *bool  `json:"dns_enabled"`
		Driver     string `json:"driver"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", err
	}
	return out.DNSEnabled, out.Driver, nil
}

// networkConnect attaches a container with per-network options (aliases).
func (e *podmanEngine) networkConnect(ctx context.Context, net api.NetworkAttachment, containerID string) error {
	body := map[string]any{"container": containerID}
	if len(net.Aliases) > 0 {
		body["aliases"] = net.Aliases
	}
	return e.connect(ctx, net.Name, body)
}

// networkJoin attaches a container with no per-network options.
func (e *podmanEngine) networkJoin(ctx context.Context, netName, containerID string) error {
	return e.connect(ctx, netName, map[string]any{"container": containerID})
}

// connect performs the attach and swallows the already-connected case.
//
// That case needs care. Measured on 5.8.2: connecting a container that is
// already on the network returns **500** on libpod (403 on compat, 409 on real
// Docker), and 500 is otherwise indistinguishable from a genuine internal
// failure. libpod's error body carries a short `cause` field — literally
// "network is already connected" — which is the only usable discriminator, so
// that is what this matches on rather than the long `message` that embeds ids.
//
// Worth knowing: when the container is STOPPED, libpod treats the same request
// as a silent no-op and returns 200, discarding any per-network options. So
// "already connected" is not one behaviour but two, and neither is an error for
// a converge-to-desired-state caller.
func (e *podmanEngine) connect(ctx context.Context, netName string, body map[string]any) error {
	resp, err := e.do(ctx, http.MethodPost, "/networks/"+netName+"/connect", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK, http.StatusNoContent); err != nil {
		if causeIs(err, "already connected") {
			return nil
		}
		return err
	}
	return nil
}

// networkLeave detaches a container.
//
// The body is Docker-shaped PascalCase while connect's is snake_case — not a
// typo here: libpod's disconnect route is wired to the Docker-compat handler,
// so it decodes Docker's DisconnectOptions.
func (e *podmanEngine) networkLeave(ctx context.Context, netName, containerID string) error {
	body := map[string]any{"Container": containerID, "Force": false}
	resp, err := e.do(ctx, http.MethodPost, "/networks/"+netName+"/disconnect", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// A container already off the network, or a network already gone, are both
	// the desired end state.
	return expect(resp, http.StatusOK, http.StatusNoContent, http.StatusNotFound, http.StatusInternalServerError)
}

// networkInspect returns the network's labels and member container ids.
func (e *podmanEngine) networkInspect(ctx context.Context, name string) (map[string]string, []string, error) {
	resp, err := e.do(ctx, http.MethodGet, "/networks/"+name+"/json", nil)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, nil
	}
	if err := expect(resp, http.StatusOK); err != nil {
		return nil, nil, err
	}
	var out struct {
		Labels     map[string]string          `json:"labels"`
		Containers map[string]json.RawMessage `json:"containers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, nil, err
	}
	members := make([]string, 0, len(out.Containers))
	for id := range out.Containers {
		members = append(members, id)
	}
	// Map iteration order is randomized; callers compare and log this, so sort.
	sort.Strings(members)
	return out.Labels, members, nil
}

func (e *podmanEngine) networkDriver(ctx context.Context, name string) (string, error) {
	resp, err := e.do(ctx, http.MethodGet, "/networks/"+name+"/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return "", err
	}
	var out struct {
		Driver string `json:"driver"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Driver, nil
}

func (e *podmanEngine) networkRemove(ctx context.Context, name string) error {
	q := url.Values{}
	q.Set("force", "false")
	resp, err := e.do(ctx, http.MethodDelete, "/networks/"+name+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Delete-if-exists, and a network still in use is left alone rather than
	// failing the caller's teardown.
	return expect(resp, http.StatusOK, http.StatusNoContent, http.StatusNotFound,
		http.StatusConflict, http.StatusInternalServerError)
}

// --- container inspection --------------------------------------------------

// podmanInspect is the subset of libpod's container inspect this engine reads.
// The shape is Docker's — State.{Running,ExitCode,Health.Status} and
// NetworkSettings — which libpod matches deliberately.
type podmanInspect struct {
	State struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
		// Pid is the container's init pid. libpod spells it "Pid" like Docker
		// does; encoding/json matches case-insensitively either way.
		//
		// This field is why credential ENDPOINT delivery works on podman: it is
		// the only handle on the workload's network namespace
		// (/proc/<Pid>/ns/net). It was missing here when the docker engine gained
		// it, and the symptom was not a compile error or a decode failure — the
		// zero value simply read as "not running yet", so the endpoint retried
		// forever and the credential never arrived, on a container that was
		// demonstrably running.
		Pid    int `json:"Pid"`
		Health *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
		Networks  map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (e *podmanEngine) inspect(ctx context.Context, id string) (podmanInspect, error) {
	resp, err := e.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil)
	if err != nil {
		return podmanInspect{}, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return podmanInspect{}, err
	}
	var out podmanInspect
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return podmanInspect{}, err
	}
	return out, nil
}

func (e *podmanEngine) containerInspect(ctx context.Context, id string) (containerInspectResult, error) {
	in, err := e.inspect(ctx, id)
	if err != nil {
		return containerInspectResult{}, err
	}
	res := containerInspectResult{ExitCode: in.State.ExitCode, Running: in.State.Running, Pid: in.State.Pid}
	if in.State.Health != nil {
		res.Health = in.State.Health.Status
	}
	return res, nil
}

// containerAddresses returns the per-network addresses and the default-bridge
// address, matching the Docker engine's contract so the shared address-selection
// logic (pickNetworkIP / selectIP) works unchanged.
func (e *podmanEngine) containerAddresses(ctx context.Context, id string) (map[string]string, string, error) {
	in, err := e.inspect(ctx, id)
	if err != nil {
		return nil, "", err
	}
	nets := make(map[string]string, len(in.NetworkSettings.Networks))
	for name, n := range in.NetworkSettings.Networks {
		if n.IPAddress != "" {
			nets[name] = n.IPAddress
		}
	}
	return nets, in.NetworkSettings.IPAddress, nil
}

func (e *podmanEngine) containerNetworks(ctx context.Context, id string) (map[string]string, error) {
	nets, _, err := e.containerAddresses(ctx, id)
	return nets, err
}
