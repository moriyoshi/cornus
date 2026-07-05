package dockerhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/observability"
)

// engineClient is a minimal Docker Engine API client. It supports the handful
// of endpoints cornus needs (image pull, container create/start/list/remove)
// over a unix socket or TCP host, avoiding the heavy moby client dependency.
type engineClient struct {
	http *http.Client
	// host is the value placed in request URLs. For unix sockets it is a
	// fixed placeholder; the dialer ignores it.
	host string
	// dial opens a fresh raw connection to the Docker host, used for hijacked
	// (bidirectional) endpoints like exec-start and attach. It mirrors the
	// transport dialer above but hands back the raw net.Conn so we can take the
	// connection over after the HTTP upgrade.
	dial func(ctx context.Context) (net.Conn, error)
	// hostHeader is the Host: header value written on hand-rolled hijack requests.
	hostHeader string
}

// newEngineClient builds a client from DOCKER_HOST (default
// unix:///var/run/docker.sock).
func newEngineClient() (*engineClient, error) {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	switch {
	case strings.HasPrefix(host, "unix://"):
		sock := strings.TrimPrefix(host, "unix://")
		return &engineClient{
			http: &http.Client{
				Transport: otelTransport(&http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, "unix", sock)
					},
				}),
				Timeout: 0, // streaming pulls can be long
			},
			host: "http://docker",
			dial: func(ctx context.Context) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
			hostHeader: "docker",
		}, nil
	case strings.HasPrefix(host, "tcp://"):
		addr := strings.TrimPrefix(host, "tcp://")
		return &engineClient{
			http: &http.Client{Transport: otelTransport(nil), Timeout: 0},
			host: "http://" + addr,
			dial: func(ctx context.Context) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			},
			hostHeader: addr,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST %q", host)
	}
}

// otelTransport wraps base with otelhttp so Docker Engine API calls over the
// hand-rolled HTTP client become client spans propagating the request's trace
// context. It is gated on observability.Enabled(): when telemetry is off it
// returns base unchanged (nil for the TCP path, so the client falls back to
// http.DefaultTransport exactly as before) — a strict no-op with no wrapping.
// The hijacked exec/attach path dials raw and does not pass through here.
func otelTransport(base http.RoundTripper) http.RoundTripper {
	if !observability.Enabled() {
		return base
	}
	return otelhttp.NewTransport(base)
}

func (c *engineClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.host+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// checkStatus reads the body and returns an error for non-2xx responses.
func checkStatus(resp *http.Response, okCodes ...int) error {
	for _, code := range okCodes {
		if resp.StatusCode == code {
			return nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return fmt.Errorf("docker api: %s: %s", resp.Status, strings.TrimSpace(string(b)))
}

// imagePull pulls an image (fromImage+tag) and waits for the stream to finish.
func (c *engineClient) imagePull(ctx context.Context, ref string) error {
	fromImage, tag := splitRefTag(ref)
	q := url.Values{}
	q.Set("fromImage", fromImage)
	if tag != "" {
		q.Set("tag", tag)
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return err
	}
	// The pull progress is a JSON stream; decoding it to EOF waits for
	// completion. Docker returns HTTP 200 even when the pull fails and reports
	// the failure as an {"error":...,"errorDetail":...} object inside the
	// stream, so we must inspect each message rather than blindly draining —
	// otherwise a failed pull (unknown tag, auth/registry error, mid-pull
	// network drop) is treated as success and Apply proceeds to tear down the
	// running deployment.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("%s", msg.Error)
		}
		if msg.ErrorDetail.Message != "" {
			return fmt.Errorf("%s", msg.ErrorDetail.Message)
		}
	}
}

// imageExists reports whether the daemon already has an image tagged ref, via
// GET /images/{ref}/json (200 present, 404 absent). Any other error is returned
// so the caller can distinguish "definitely absent" from "could not tell".
func (c *engineClient) imageExists(ctx context.Context, ref string) (bool, error) {
	// The Docker Engine API takes the image name verbatim in the path (slashes and
	// the tag colon are literal path characters), matching the moby client — do not
	// url-escape it, which would encode the slashes and 404 every namespaced ref.
	resp, err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, checkStatus(resp, http.StatusOK)
	}
}

type createBody struct {
	Image            string              `json:"Image"`
	Env              []string            `json:"Env,omitempty"`
	Cmd              []string            `json:"Cmd,omitempty"`
	Entrypoint       []string            `json:"Entrypoint,omitempty"`
	Labels           map[string]string   `json:"Labels"`
	User             string              `json:"User,omitempty"`
	WorkingDir       string              `json:"WorkingDir,omitempty"`
	Hostname         string              `json:"Hostname,omitempty"`
	StopSignal       string              `json:"StopSignal,omitempty"`
	StopTimeout      *int                `json:"StopTimeout,omitempty"`
	Tty              bool                `json:"Tty,omitempty"`
	OpenStdin        bool                `json:"OpenStdin,omitempty"`
	ExposedPorts     map[string]struct{} `json:"ExposedPorts,omitempty"`
	Healthcheck      *healthConfig       `json:"Healthcheck,omitempty"`
	HostConfig       hostConfig          `json:"HostConfig"`
	NetworkingConfig *networkingConfig   `json:"NetworkingConfig,omitempty"`
}

// healthConfig mirrors Docker's container Config.Healthcheck. Test is the CMD
// form (["CMD", ...] / ["CMD-SHELL", str] / ["NONE"]); the duration fields are
// nanoseconds, exactly as the Engine API expects.
type healthConfig struct {
	Test          []string `json:"Test,omitempty"`
	Interval      int64    `json:"Interval,omitempty"`
	Timeout       int64    `json:"Timeout,omitempty"`
	StartPeriod   int64    `json:"StartPeriod,omitempty"`
	StartInterval int64    `json:"StartInterval,omitempty"`
	Retries       int      `json:"Retries,omitempty"`
}

type hostConfig struct {
	Binds          []string                 `json:"Binds,omitempty"`
	Mounts         []mountSpec              `json:"Mounts,omitempty"`
	PortBindings   map[string][]portBinding `json:"PortBindings,omitempty"`
	RestartPolicy  restartPolicy            `json:"RestartPolicy"`
	Privileged     bool                     `json:"Privileged,omitempty"`
	NetworkMode    string                   `json:"NetworkMode,omitempty"`
	NanoCpus       int64                    `json:"NanoCpus,omitempty"`
	Memory         int64                    `json:"Memory,omitempty"`
	Init           *bool                    `json:"Init,omitempty"`
	ReadonlyRootfs bool                     `json:"ReadonlyRootfs,omitempty"`
	// MemoryReservation is the soft memory floor (compose
	// deploy.resources.reservations.memory). Docker has no CPU reservation, so a
	// CPU reservation has no field here.
	MemoryReservation int64 `json:"MemoryReservation,omitempty"`
	// Security & networking keys (compose cap_add/cap_drop/security_opt/
	// group_add/sysctls/extra_hosts/dns/dns_search/dns_opt). Docker's Engine API
	// field names (Dns/DnsSearch/DnsOptions carry the historical spelling).
	CapAdd      []string          `json:"CapAdd,omitempty"`
	CapDrop     []string          `json:"CapDrop,omitempty"`
	SecurityOpt []string          `json:"SecurityOpt,omitempty"`
	GroupAdd    []string          `json:"GroupAdd,omitempty"`
	Sysctls     map[string]string `json:"Sysctls,omitempty"`
	ExtraHosts  []string          `json:"ExtraHosts,omitempty"`
	Dns         []string          `json:"Dns,omitempty"`
	DnsSearch   []string          `json:"DnsSearch,omitempty"`
	DnsOptions  []string          `json:"DnsOptions,omitempty"`
	// Resource & host-namespace keys (compose ulimits/tmpfs/devices/shm_size/
	// pid/ipc). Docker's Engine API field names.
	Ulimits []ulimit          `json:"Ulimits,omitempty"`
	Tmpfs   map[string]string `json:"Tmpfs,omitempty"` // path -> mount options ("" for none)
	Devices []deviceMapping   `json:"Devices,omitempty"`
	ShmSize int64             `json:"ShmSize,omitempty"`
	PidMode string            `json:"PidMode,omitempty"`
	IpcMode string            `json:"IpcMode,omitempty"`
}

// ulimit mirrors Docker's HostConfig.Ulimits entry.
type ulimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

// deviceMapping mirrors Docker's HostConfig.Devices entry.
type deviceMapping struct {
	PathOnHost        string `json:"PathOnHost"`
	PathInContainer   string `json:"PathInContainer"`
	CgroupPermissions string `json:"CgroupPermissions"`
}

// networkingConfig / endpointSettings mirror the Docker create-body shapes for
// joining the container's primary user-defined network with its DNS aliases.
type networkingConfig struct {
	EndpointsConfig map[string]endpointSettings `json:"EndpointsConfig"`
}

type endpointSettings struct {
	Aliases []string `json:"Aliases,omitempty"`
	// MacAddress pins the member's MAC on this network (compose service
	// `mac_address`); IPAMConfig pins its per-network IPv4/IPv6 address (compose
	// `ipv4_address`/`ipv6_address`). Empty leaves libnetwork to allocate.
	MacAddress string              `json:"MacAddress,omitempty"`
	IPAMConfig *endpointIPAMConfig `json:"IPAMConfig,omitempty"`
}

// endpointIPAMConfig mirrors Docker's EndpointSettings.IPAMConfig: a fixed
// per-network address for the member.
type endpointIPAMConfig struct {
	IPv4Address string `json:"IPv4Address,omitempty"`
	IPv6Address string `json:"IPv6Address,omitempty"`
}

// endpointConfigFor builds the endpoint settings for a network attachment: the
// DNS aliases plus any pinned MAC / IPv6 address (compose service long-syntax).
// The IPv4 pin (api.NetworkAttachment.IP) is intentionally NOT wired here — the
// compose planner rewrites it to CIDR form for the Multus fabric (usernet.go),
// which is not a valid Docker EndpointSettings address, and libnetwork addresses
// members natively — so dockerhost keeps ignoring IPv4 as it always has.
func endpointConfigFor(n api.NetworkAttachment) endpointSettings {
	ep := endpointSettings{Aliases: n.Aliases, MacAddress: n.MAC}
	if n.IPv6 != "" {
		ep.IPAMConfig = &endpointIPAMConfig{IPv6Address: n.IPv6}
	}
	return ep
}

// mountSpec is a Docker HostConfig.Mounts entry. A "volume"-type mount with an
// empty Source makes Docker auto-provision an anonymous volume for the target,
// which is how cornus backs an api.VolumeSpec on the dockerhost backend. A
// "bind"-type mount's BindOptions.Propagation carries the shared-subtree
// propagation mode ("rshared"/"rslave") used to relay a caretaker sidecar's own
// kernel mount into the app container's view of the same host path — see
// ApplyWithMounts in mounts.go.
type mountSpec struct {
	Type        string       `json:"Type"`             // "volume" for a managed/anonymous volume, "bind" for a host path
	Source      string       `json:"Source,omitempty"` // empty => Docker creates an anonymous volume
	Target      string       `json:"Target"`
	ReadOnly    bool         `json:"ReadOnly,omitempty"`
	BindOptions *bindOptions `json:"BindOptions,omitempty"`
}

// bindOptions mirrors Docker's Mounts[].BindOptions; only the propagation mode
// is needed today.
type bindOptions struct {
	Propagation string `json:"Propagation,omitempty"`
}

type portBinding struct {
	HostIP   string `json:"HostIP,omitempty"`
	HostPort string `json:"HostPort"`
}

type restartPolicy struct {
	Name string `json:"Name"`
	// MaximumRetryCount bounds retries for an "on-failure" policy (compose
	// deploy.restart_policy.max_attempts). Docker only honours it with
	// Name=="on-failure"; omitempty keeps it off (unlimited) otherwise.
	MaximumRetryCount int `json:"MaximumRetryCount,omitempty"`
}

// containerCreate creates a container and returns its ID.
func (c *engineClient) containerCreate(ctx context.Context, name string, body createBody) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusCreated); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *engineClient) containerStart(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusNoContent, http.StatusNotModified)
}

func (c *engineClient) containerStop(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusNoContent, http.StatusNotModified)
}

func (c *engineClient) containerRestart(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/restart", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusNoContent, http.StatusNotModified)
}

// k8sPseudoDriver are DeploySpec network "drivers" that select a kubernetes
// netdriver provider pipeline rather than a Docker network driver; on the
// dockerhost backend they map to Docker's default bridge.
var k8sPseudoDriver = map[string]bool{"services": true, "policy": true, "cilium": true}

// networkEnsure creates a user-defined network if it does not exist, labeling
// it cornus-managed so delete-time GC can tell it apart from external
// (user-provisioned) networks. An already-existing network — cornus's own
// from an earlier apply, or an external one — satisfies the ensure (409).
// Driver and options pass straight through to Docker's network drivers.
func (c *engineClient) networkEnsure(ctx context.Context, net api.NetworkAttachment) error {
	body := map[string]any{
		"Name":           net.Name,
		"CheckDuplicate": true, // pre-1.44 daemons would otherwise mint a duplicate name
		"Labels":         map[string]string{deploy.LabelManaged: "true"},
	}
	// Only real Docker network drivers are passed through. "services"/"policy"/
	// "cilium" are kubernetes-fabric SELECTORS, not Docker drivers — on a Docker
	// host the default bridge already provides embedded DNS and per-network
	// isolation, which is exactly what those selectors ask for, so we let Docker
	// pick its default rather than 500 on an unknown driver.
	if net.Driver != "" && !k8sPseudoDriver[net.Driver] {
		body["Driver"] = net.Driver
	}
	if len(net.DriverOpts) > 0 {
		body["Options"] = net.DriverOpts
	}
	// IPAM config (compose ipam.config[0]): forward the subnet/gateway/ip_range as
	// Docker's single IPAM.Config entry so the network is addressed as requested.
	if net.Subnet != "" || net.Gateway != "" || net.IPRange != "" {
		cfg := map[string]string{}
		if net.Subnet != "" {
			cfg["Subnet"] = net.Subnet
		}
		if net.Gateway != "" {
			cfg["Gateway"] = net.Gateway
		}
		if net.IPRange != "" {
			cfg["IPRange"] = net.IPRange
		}
		body["IPAM"] = map[string]any{"Config": []map[string]string{cfg}}
	}
	// Network toggles (compose internal/attachable/enable_ipv6) and user labels
	// map straight onto Docker's network-create fields. cornus's own management
	// label is already in body["Labels"] and wins on a key clash.
	if net.Internal {
		body["Internal"] = true
	}
	if net.Attachable {
		body["Attachable"] = true
	}
	if net.EnableIPv6 {
		body["EnableIPv6"] = true
	}
	if len(net.Labels) > 0 {
		labels := map[string]string{}
		for k, v := range net.Labels {
			labels[k] = v
		}
		labels[deploy.LabelManaged] = "true"
		body["Labels"] = labels
	}
	resp, err := c.do(ctx, http.MethodPost, "/networks/create", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil // already exists
	}
	return checkStatus(resp, http.StatusCreated)
}

// networkConnect attaches a container to an additional network with its DNS
// aliases and any pinned MAC/IPv6 endpoint settings (POST
// /networks/{name}/connect). Only the first network can ride the create body
// portably across engine versions; the rest go through here.
func (c *engineClient) networkConnect(ctx context.Context, net api.NetworkAttachment, containerID string) error {
	body := map[string]any{
		"Container":      containerID,
		"EndpointConfig": endpointConfigFor(net),
	}
	resp, err := c.do(ctx, http.MethodPost, "/networks/"+url.PathEscape(net.Name)+"/connect", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusOK)
}

// networkInspect returns a network's labels and how many containers are
// currently attached (GET /networks/{name}).
func (c *engineClient) networkInspect(ctx context.Context, name string) (labels map[string]string, members int, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, 0, err
	}
	var out struct {
		Labels     map[string]string          `json:"Labels"`
		Containers map[string]json.RawMessage `json:"Containers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, err
	}
	return out.Labels, len(out.Containers), nil
}

// networkRemove deletes a network (DELETE /networks/{name}); already-gone is fine.
func (c *engineClient) networkRemove(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusNoContent, http.StatusNotFound)
}

type containerSummary struct {
	ID     string
	Image  string
	State  string
	Labels map[string]string
}

func (c *engineClient) containerList(ctx context.Context, label string) ([]containerSummary, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	q := url.Values{}
	q.Set("all", "1")
	q.Set("filters", string(filters))
	resp, err := c.do(ctx, http.MethodGet, "/containers/json?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var raw []struct {
		Id     string            `json:"Id"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]containerSummary, 0, len(raw))
	for _, r := range raw {
		out = append(out, containerSummary{ID: r.Id, Image: r.Image, State: r.State, Labels: r.Labels})
	}
	return out, nil
}

// containerIP inspects id (GET /containers/{id}/json) and returns the container's
// reachable IP address: the default-bridge IPAddress when present, otherwise the
// first non-empty per-network IPAddress (containers on a user-defined network have
// an empty top-level IPAddress and carry their address under NetworkSettings.Networks).
// It is used by ForwardPort to dial a container port directly, so it reaches ports
// the container never published to the host.
func (c *engineClient) containerIP(ctx context.Context, id string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return "", err
	}
	var raw struct {
		NetworkSettings struct {
			IPAddress string `json:"IPAddress"`
			Networks  map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", err
	}
	if ip := raw.NetworkSettings.IPAddress; ip != "" {
		return ip, nil
	}
	networks := make(map[string]string, len(raw.NetworkSettings.Networks))
	for name, n := range raw.NetworkSettings.Networks {
		networks[name] = n.IPAddress
	}
	if ip := pickNetworkIP(networks); ip != "" {
		return ip, nil
	}
	return "", fmt.Errorf("container %s has no IP address (not running, or on a network the server cannot route to)", id)
}

// containerInspectResult holds the subset of GET /containers/{id}/json the
// status path needs: the container's structured health (present only when the
// image declares a HEALTHCHECK), its main-process exit code (meaningful once
// the container has terminated), and whether it is currently running.
type containerInspectResult struct {
	// Health is the Docker health string ("healthy", "unhealthy", "starting"),
	// or "" when the container has no healthcheck.
	Health string
	// ExitCode is State.ExitCode as reported by Docker; only meaningful when the
	// container is not Running.
	ExitCode int
	Running  bool
}

// containerInspect inspects id (GET /containers/{id}/json) and returns its
// health, exit code, and running-ness for the status path. State.Health is
// absent when the image declares no HEALTHCHECK, in which case Health is "".
func (c *engineClient) containerInspect(ctx context.Context, id string) (containerInspectResult, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil)
	if err != nil {
		return containerInspectResult{}, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return containerInspectResult{}, err
	}
	var raw struct {
		State struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
			Health   *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return containerInspectResult{}, err
	}
	res := containerInspectResult{ExitCode: raw.State.ExitCode, Running: raw.State.Running}
	if raw.State.Health != nil {
		res.Health = raw.State.Health.Status
	}
	return res, nil
}

// pickNetworkIP deterministically chooses a container's per-network IP. Docker's
// NetworkSettings.Networks decodes into a Go map, whose iteration order is
// randomized, so a container attached to more than one network would otherwise
// yield a nondeterministic address across calls -- and ForwardPort could
// intermittently dial an unroutable network. Prefer the default "bridge" network
// (which the server host can route to); otherwise fall back to the
// lexicographically first network name so the choice is stable across calls.
func pickNetworkIP(networks map[string]string) string {
	if ip := networks["bridge"]; ip != "" {
		return ip
	}
	names := make([]string, 0, len(networks))
	for name, ip := range networks {
		if ip != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return networks[names[0]]
}

// containerLogs opens Docker's container logs stream for id. For a non-TTY
// container the returned stream is stdcopy-multiplexed (8-byte frame headers per
// chunk); the backend passes those bytes through unchanged to satisfy the
// deploy.Backend.Logs framing contract. The caller must Close the stream; ctx
// cancellation aborts a follow (the request context closes the connection).
func (c *engineClient) containerLogs(ctx context.Context, id string, opts api.LogOptions) (io.ReadCloser, error) {
	stdout, stderr := opts.Streams()
	q := url.Values{}
	if opts.Follow {
		q.Set("follow", "1")
	}
	if stdout {
		q.Set("stdout", "1")
	}
	if stderr {
		q.Set("stderr", "1")
	}
	if opts.Timestamps {
		q.Set("timestamps", "1")
	}
	tail := opts.Tail
	if tail == "" {
		tail = "all"
	}
	q.Set("tail", tail)
	if opts.Since != "" {
		q.Set("since", opts.Since)
	}
	if opts.Until != "" {
		q.Set("until", opts.Until)
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// containerStats opens Docker's live stats stream for id. When stream is false
// (?stream=0) Docker emits a single stats object then closes the stream. The
// JSON is Docker's own stats format and is passed through unchanged. The caller
// must Close the returned stream; ctx cancellation aborts a live stream.
func (c *engineClient) containerStats(ctx context.Context, id string, stream bool) (io.ReadCloser, error) {
	q := url.Values{}
	if stream {
		q.Set("stream", "1")
	} else {
		q.Set("stream", "0")
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/stats?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// execCreateBody is Docker's POST /containers/{id}/exec request body (subset).
type execCreateBody struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
	User         string   `json:"User,omitempty"`
	Privileged   bool     `json:"Privileged,omitempty"`
}

// execCreate creates an exec in container id (POST /containers/{id}/exec) and
// returns Docker's exec id.
func (c *engineClient) execCreate(ctx context.Context, id string, cfg api.ExecConfig) (string, error) {
	body := execCreateBody{
		AttachStdin:  cfg.AttachStdin,
		AttachStdout: cfg.AttachStdout,
		AttachStderr: cfg.AttachStderr,
		Tty:          cfg.Tty,
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
		User:         cfg.User,
		Privileged:   cfg.Privileged,
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+id+"/exec", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusCreated, http.StatusOK); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// execInspect reports an exec's state (GET /exec/{id}/json).
func (c *engineClient) execInspect(ctx context.Context, execID string) (api.ExecState, error) {
	resp, err := c.do(ctx, http.MethodGet, "/exec/"+execID+"/json", nil)
	if err != nil {
		return api.ExecState{}, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return api.ExecState{}, err
	}
	var out struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
		Pid      int  `json:"Pid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return api.ExecState{}, err
	}
	return api.ExecState{Running: out.Running, ExitCode: out.ExitCode, Pid: out.Pid}, nil
}

// execResize resizes the TTY of a running exec to h rows by w columns (POST
// /exec/{id}/resize?h=<h>&w=<w>). It is a plain (non-hijack) request; Docker
// returns 200 with an empty body on success.
func (c *engineClient) execResize(ctx context.Context, execID string, h, w uint) error {
	path := fmt.Sprintf("/exec/%s/resize?h=%d&w=%d", execID, h, w)
	resp, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusOK)
}

// hijackedConn is a raw hijacked connection whose reads come from the buffered
// reader used to consume the HTTP response, so any stream bytes already buffered
// after the headers are not lost.
type hijackedConn struct {
	net.Conn
	r *bufio.Reader
}

func (h *hijackedConn) Read(p []byte) (int, error) { return h.r.Read(p) }

// CloseWrite half-closes the write side of the underlying connection (a real
// *net.TCPConn / *net.UnixConn supports it), so a stdin EOF can be forwarded to
// Docker as a write-side FIN without tearing down the read side still carrying
// the process's output. It is a no-op if the underlying conn cannot half-close.
func (h *hijackedConn) CloseWrite() error {
	if cw, ok := h.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// hijack dials a fresh connection to the Docker host, writes a hand-rolled HTTP
// request that asks to upgrade the connection to a raw bidirectional stream
// (Connection: Upgrade / Upgrade: tcp), reads the status line and headers off the
// wire, and returns the connection positioned at the raw stream. It is used for
// exec-start and attach, which Docker serves by hijacking the HTTP connection.
// A non-101/200 status is surfaced as an error carrying the response body.
func (c *engineClient) hijack(ctx context.Context, method, path string, body []byte) (net.Conn, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	var req bytes.Buffer
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\n", method, path)
	fmt.Fprintf(&req, "Host: %s\r\n", c.hostHeader)
	req.WriteString("Connection: Upgrade\r\n")
	req.WriteString("Upgrade: tcp\r\n")
	if body != nil {
		req.WriteString("Content-Type: application/json\r\n")
		fmt.Fprintf(&req, "Content-Length: %d\r\n", len(body))
	}
	req.WriteString("\r\n")
	if body != nil {
		req.Write(body)
	}
	if _, err := conn.Write(req.Bytes()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("docker hijack %s %s: write request: %w", method, path, err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("docker hijack %s %s: read status: %w", method, path, err)
	}
	// Status line: "HTTP/1.1 101 UPGRADED".
	fields := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	code := 0
	if len(fields) >= 2 {
		code, _ = strconv.Atoi(fields[1])
	}
	// Drain headers up to the blank line.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("docker hijack %s %s: read headers: %w", method, path, err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if code != http.StatusSwitchingProtocols && code != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(br, 8<<10))
		conn.Close()
		return nil, fmt.Errorf("docker hijack %s %s: %s: %s", method, path, strings.TrimSpace(statusLine), strings.TrimSpace(string(msg)))
	}
	return &hijackedConn{Conn: conn, r: br}, nil
}

// containerArchiveStat performs Docker's archive HEAD for path in id, returning
// only the decoded X-Docker-Container-Path-Stat header (no body).
func (c *engineClient) containerArchiveStat(ctx context.Context, id, path string) (api.PathStat, error) {
	q := url.Values{}
	q.Set("path", path)
	resp, err := c.do(ctx, http.MethodHead, "/containers/"+id+"/archive?"+q.Encode(), nil)
	if err != nil {
		return api.PathStat{}, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return api.PathStat{}, err
	}
	return api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
}

// containerArchiveGet performs Docker's archive GET for path in id, returning
// the decoded path stat (from the X-Docker-Container-Path-Stat header) and the
// tar body stream. The caller must Close the returned stream.
func (c *engineClient) containerArchiveGet(ctx context.Context, id, path string) (io.ReadCloser, api.PathStat, error) {
	q := url.Values{}
	q.Set("path", path)
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/archive?"+q.Encode(), nil)
	if err != nil {
		return nil, api.PathStat{}, err
	}
	if err := checkStatus(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, api.PathStat{}, err
	}
	st, err := api.DecodePathStat(resp.Header.Get(api.PathStatHeader))
	if err != nil {
		resp.Body.Close()
		return nil, api.PathStat{}, err
	}
	return resp.Body, st, nil
}

// containerArchivePut performs Docker's archive PUT, extracting the tar read
// from r into path in id. opts carries Docker's noOverwriteDirNonDir/copyUIDGID
// query params.
func (c *engineClient) containerArchivePut(ctx context.Context, id, path string, r io.Reader, opts api.CopyToOptions) error {
	q := url.Values{}
	q.Set("path", path)
	if opts.NoOverwriteDirNonDir {
		q.Set("noOverwriteDirNonDir", "1")
	}
	if opts.CopyUIDGID {
		q.Set("copyUIDGID", "1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.host+"/containers/"+id+"/archive?"+q.Encode(), r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusOK)
}

// containerRemove force-removes a container. v=1 asks dockerd to also remove
// the container's anonymous volumes (docker rm -v parity), which the
// deploy.Backend.Delete contract promises; named volumes are never touched.
func (c *engineClient) containerRemove(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+id+"?force=1&v=1", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusNoContent, http.StatusOK)
}

// volumeEnsure creates a NAMED Docker volume with its compose driver /
// driver_opts / labels (POST /volumes/create). Docker's volume-create is
// idempotent — a repeat call for an existing name returns 200/201 rather than a
// conflict — so this is safe to call on every Apply. cornus's own management
// label is written last so it always wins on a key clash. Anonymous volumes
// (empty Name) never reach here: dockerd auto-provisions them from the container
// mount, and there is nothing extra to configure. It is only worth a call when
// the volume carries a driver/opts/labels; a plain named volume is still
// auto-created by the container mount as before.
func (c *engineClient) volumeEnsure(ctx context.Context, v api.VolumeSpec) error {
	if v.Name == "" {
		return nil
	}
	body := map[string]any{"Name": v.Name}
	if v.Driver != "" {
		body["Driver"] = v.Driver
	}
	if len(v.DriverOpts) > 0 {
		body["DriverOpts"] = v.DriverOpts
	}
	labels := map[string]string{}
	for k, val := range v.Labels {
		labels[k] = val
	}
	labels[deploy.LabelManaged] = "true"
	body["Labels"] = labels
	resp, err := c.do(ctx, http.MethodPost, "/volumes/create", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp, http.StatusCreated, http.StatusOK)
}

// volumeInspect returns a named Docker volume's host-side Mountpoint (GET
// /volumes/{name}) — a path on the DAEMON's own host. It is meaningful only to
// the daemon and to containers it is bind-mounted into; the cornus server never
// opens it directly, which is what lets ApplyWithMounts (mounts.go) work even
// when the server does not share a filesystem with the daemon at all.
func (c *engineClient) volumeInspect(ctx context.Context, name string) (mountpoint string, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/volumes/"+name, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, http.StatusOK); err != nil {
		return "", err
	}
	var out struct {
		Mountpoint string `json:"Mountpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Mountpoint, nil
}

// volumeRemove removes a named Docker volume (DELETE /volumes/{name}). A missing
// volume (404) is treated as success, matching the delete-if-exists contract of
// deploy.VolumeRemover; a volume still in use (409) surfaces as an error.
func (c *engineClient) volumeRemove(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/volumes/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return checkStatus(resp, http.StatusNoContent, http.StatusOK)
}

// toCreateBody converts a DeploySpec into a Docker container create request.
func toCreateBody(spec api.DeploySpec) createBody {
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	sortStrings(env)

	exposed := map[string]struct{}{}
	bindings := map[string][]portBinding{}
	for _, p := range spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		key := strconv.Itoa(p.Container) + "/" + proto
		exposed[key] = struct{}{}
		// Host==0 means expose-only (no host publish requested), matching the
		// containerd backend's toCNIPorts. Emitting a HostPort:"0" binding would
		// make dockerd publish the port on a random host port, unexpectedly
		// exposing it on all host interfaces — so bind only when a host port is
		// actually requested.
		if p.Host != 0 {
			bindings[key] = append(bindings[key], portBinding{HostIP: p.HostIP, HostPort: strconv.Itoa(p.Host)})
		}
	}

	binds := make([]string, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		bind := m.Source + ":" + m.Target
		// Collect the bind options (read-only, then the SELinux relabel token) and
		// append them comma-joined: "src:dst", "src:dst:ro", "src:dst:z",
		// "src:dst:ro,z".
		var opts []string
		if m.ReadOnly {
			opts = append(opts, "ro")
		}
		if m.SELinux == "z" || m.SELinux == "Z" {
			opts = append(opts, m.SELinux)
		}
		if len(opts) > 0 {
			bind += ":" + strings.Join(opts, ",")
		}
		binds = append(binds, bind)
	}

	// Managed volumes become "volume"-type mounts. A named volume passes its
	// (project-scoped) name as Source, so Docker shares one persistent volume
	// across every container that names it; an anonymous volume leaves Source
	// empty, so Docker provisions fresh per-container storage for the target.
	mounts := make([]mountSpec, 0, len(spec.Volumes))
	for _, v := range spec.Volumes {
		mounts = append(mounts, mountSpec{
			Type:     "volume",
			Source:   v.Name,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		})
	}

	// User labels (compose `labels`) come first; cornus's own management labels
	// and origin lineage are written last so they always win on a key clash.
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[deploy.LabelManaged] = "true"
	labels[deploy.LabelApp] = spec.Name
	for k, v := range deploy.OriginToLabels(spec.Origin) {
		labels[k] = v
	}

	body := createBody{
		Image:        spec.Image,
		Env:          env,
		Cmd:          spec.Command,
		Entrypoint:   spec.Entrypoint,
		User:         spec.User,
		WorkingDir:   spec.WorkingDir,
		Hostname:     spec.Hostname,
		StopSignal:   spec.StopSignal,
		Tty:          spec.TTY,
		OpenStdin:    spec.StdinOpen,
		ExposedPorts: exposed,
		Labels:       labels,
		HostConfig: hostConfig{
			Binds:          binds,
			Mounts:         mounts,
			PortBindings:   bindings,
			RestartPolicy:  restartPolicy{Name: deploy.RestartPolicy(spec), MaximumRetryCount: spec.RestartMaxAttempts},
			Privileged:     spec.Privileged,
			Init:           spec.Init,
			ReadonlyRootfs: spec.ReadOnly,
			// Security & networking keys pass straight through to the Docker
			// Engine API (security_opt entries verbatim — Docker parses them).
			CapAdd:      spec.CapAdd,
			CapDrop:     spec.CapDrop,
			SecurityOpt: spec.SecurityOpt,
			GroupAdd:    spec.GroupAdd,
			Sysctls:     spec.Sysctls,
			ExtraHosts:  spec.ExtraHosts,
			Dns:         spec.DNSServers,
			DnsSearch:   spec.DNSSearch,
			DnsOptions:  spec.DNSOptions,
		},
	}
	// stop_grace_period -> Docker Config.StopTimeout (whole seconds).
	if secs, ok := deploy.StopGracePeriodSeconds(spec); ok {
		body.StopTimeout = &secs
	}

	// Health probe -> Docker container Config.Healthcheck (durations in ns).
	if hc := spec.Healthcheck; hc != nil && len(hc.Test) > 0 {
		body.Healthcheck = &healthConfig{
			Test:          hc.Test,
			Interval:      parseDurationNanos(hc.Interval),
			Timeout:       parseDurationNanos(hc.Timeout),
			StartPeriod:   parseDurationNanos(hc.StartPeriod),
			StartInterval: parseDurationNanos(hc.StartInterval),
			Retries:       hc.Retries,
		}
	}

	// Resource limits -> HostConfig NanoCpus / Memory; a memory reservation ->
	// HostConfig MemoryReservation. A CPU reservation (ReservedCPU) has no Docker
	// equivalent — Docker offers only CPU shares and hard limits, not a guaranteed
	// floor — so it is intentionally dropped here.
	if r := spec.Resources; r != nil {
		if r.CPULimit > 0 {
			body.HostConfig.NanoCpus = int64(r.CPULimit * 1e9)
		}
		if r.MemoryLimit > 0 {
			body.HostConfig.Memory = r.MemoryLimit
		}
		if r.ReservedMemory > 0 {
			body.HostConfig.MemoryReservation = r.ReservedMemory
		}
	}

	// Resource & host-namespace keys -> HostConfig.
	for _, u := range spec.Ulimits {
		body.HostConfig.Ulimits = append(body.HostConfig.Ulimits, ulimit{Name: u.Name, Soft: u.Soft, Hard: u.Hard})
	}
	// tmpfs entries are "path[:options]"; Docker's HostConfig.Tmpfs is a
	// path -> options map, so split on the FIRST colon.
	for _, t := range spec.Tmpfs {
		path, opts, _ := strings.Cut(t, ":")
		if body.HostConfig.Tmpfs == nil {
			body.HostConfig.Tmpfs = map[string]string{}
		}
		body.HostConfig.Tmpfs[path] = opts
	}
	// devices are "host:container[:perms]" (perms default "rwm").
	for _, d := range spec.Devices {
		host, container, perms := parseDevice(d)
		body.HostConfig.Devices = append(body.HostConfig.Devices, deviceMapping{
			PathOnHost:        host,
			PathInContainer:   container,
			CgroupPermissions: perms,
		})
	}
	body.HostConfig.ShmSize = spec.ShmSize
	body.HostConfig.PidMode = spec.PIDMode
	body.HostConfig.IpcMode = spec.IPCMode

	// The first user-defined network is joined at create (NetworkMode + its
	// endpoint aliases, so Docker's embedded DNS serves the names from boot);
	// additional networks are connected after create, before start —
	// multi-network create bodies are not portable across engine versions. The
	// full network list is recorded on a label so Delete can GC networks whose
	// last member is gone.
	if len(spec.Networks) > 0 {
		primary := spec.Networks[0]
		body.HostConfig.NetworkMode = primary.Name
		body.NetworkingConfig = &networkingConfig{EndpointsConfig: map[string]endpointSettings{
			primary.Name: endpointConfigFor(primary),
		}}
		names := make([]string, 0, len(spec.Networks))
		for _, n := range spec.Networks {
			names = append(names, n.Name)
		}
		labels[labelNetworks] = strings.Join(names, ",")
	}
	return body
}

// parseDevice splits a compose device mapping "host:container[:perms]" into its
// components. A missing container path defaults to the host path; missing perms
// default to "rwm" (read, write, mknod), matching Docker/Compose.
func parseDevice(s string) (host, container, perms string) {
	parts := strings.SplitN(s, ":", 3)
	host = parts[0]
	container = host
	perms = "rwm"
	if len(parts) >= 2 && parts[1] != "" {
		container = parts[1]
	}
	if len(parts) == 3 && parts[2] != "" {
		perms = parts[2]
	}
	return host, container, perms
}

// splitRefTag splits an image reference into the pull name and tag, defaulting
// to "latest" when no tag/digest is present.
func splitRefTag(ref string) (name, tag string) {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		// digest reference: fromImage carries the digest as the tag
		return ref[:at], ref[at+1:]
	}
	// Find a ':' that is part of the tag, not the registry port.
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}

// parseDurationNanos parses a Go duration string ("30s", "1m30s") into
// nanoseconds for the Docker healthcheck body. An empty or unparseable value
// yields 0, which tells Docker to use its default for that field.
func parseDurationNanos(s string) int64 {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return int64(d)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
