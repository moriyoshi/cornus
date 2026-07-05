// Package builderctr manages a containerized cornus build engine.
//
// It exists because the in-process BuildKit engine cannot run unprivileged:
// BuildKit mounts every snapshot it reads or executes, and mount(2) requires
// CAP_SYS_ADMIN. On a host where `cornus serve` runs as an ordinary user, every
// build fails — historically with a confusing `lchown ...: operation not
// permitted` while reading the Dockerfile, because BuildKit rewrites a local
// mount's uid/gid to 0/0 and the unprivileged receiver cannot chown to root.
//
// Rather than require the operator to run all of cornus privileged, Ensure
// starts a privileged cornus container that performs builds on the server's
// behalf; the server then relays build sessions to it (see pkg/server
// relayBuildAttach). The container is the SAME cornus image, so no separate
// builder artifact has to be published or kept in sync.
package builderctr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// DefaultName is the managed builder container's name. It is stable so a
	// restarted server adopts the running builder (and its warm cache) instead of
	// starting a second one.
	DefaultName = "cornus-builder"
	// DefaultAddr is the address the builder listens on. It is bound to loopback
	// and, because the builder runs with host networking, must not collide with
	// the cornus server's own port.
	DefaultAddr = "127.0.0.1:5099"
	// PublishedImage is the project's released cornus image. It is NOT the
	// default: by default the builder image is built from the running binary (see
	// selfimage.go), so the builder is always the exact same cornus as the server
	// and no registry access or version matching is involved. This ref is here for
	// operators who would rather pin the published image via Options.Image.
	PublishedImage = "ghcr.io/moriyoshi/cornus:latest"
	// dataDir is the builder's data dir inside the container (the image's VOLUME).
	dataDir = "/var/lib/cornus"
	// configLabel records the configuration a managed builder was created with, so
	// a builder left over from a different server configuration is recreated
	// instead of silently reused (see Options.fingerprint).
	configLabel = "cornus.builder.config"
	// storageDir keeps the builder's registry storage explicit. Without it the
	// server defaults to host-native re-export and tries to load results into a
	// local Docker daemon, which a builder container does not have — the build
	// then succeeds and dies at export with "failed to copy to tar".
	storageDir = dataDir + "/registry"
)

// Options configures the managed builder container.
type Options struct {
	// Image is the cornus image to run. Empty — the default — builds a throwaway
	// image containing the RUNNING binary, so the builder is byte-identical to
	// this server and needs no registry access. Set it to pin a published image.
	Image string
	// BaseImage is the base for the self-built image; empty matches the host
	// distribution (see hostBaseImage). Ignored when Image is set.
	BaseImage string
	// DockerExport mirrors the delegating server's registry mode: true when that
	// registry re-exports the local Docker daemon (host-native on dockerhost), so
	// the /v2/* registry is READ-ONLY and a build result is delivered by loading it
	// into the daemon rather than pushing it.
	//
	// This has to be mirrored because build sessions are relayed to the builder
	// verbatim, so the BUILDER decides how to export. Left false, it would resolve
	// its own mode independently, push the result at the delegating server's
	// registry, and get 405 Method Not Allowed from a read-only re-export
	// registry. Set true, the builder shares the host's Docker socket and loads the
	// image into that same daemon — the behavior the server would have had.
	DockerExport bool
	// Name is the container name; empty selects DefaultName.
	Name string
	// Addr is the builder's listen address; empty selects DefaultAddr.
	Addr string
	// Volume names the Docker volume holding the builder's build cache. Empty
	// derives it from Name. A dedicated volume matters: the builder runs as root,
	// so sharing the unprivileged server's data dir would leave root-owned
	// snapshot dirs (mode 0700) the server can no longer traverse.
	Volume string
}

func (o Options) name() string { return orDefault(o.Name, DefaultName) }

// fingerprint identifies the configuration a managed builder was created with, so
// Ensure can tell "already running" from "already running the RIGHT thing". Only
// fields that change how the container behaves belong here.
func (o Options) fingerprint() string {
	return fmt.Sprintf("addr=%s;docker-export=%t;image=%s;base=%s;volume=%s",
		o.addr(), o.DockerExport, o.Image, o.BaseImage, o.volume())
}

// dockerSocketPath returns the unix socket the Docker daemon listens on, or ""
// when DOCKER_HOST names something a container cannot be given by bind mount
// (e.g. tcp://).
func dockerSocketPath() string {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		return "/var/run/docker.sock"
	}
	if p, ok := strings.CutPrefix(host, "unix://"); ok {
		return p
	}
	return ""
}
func (o Options) addr() string   { return orDefault(o.Addr, DefaultAddr) }
func (o Options) volume() string { return orDefault(o.Volume, o.name()+"-cache") }

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// Ensure starts (or adopts) the builder container and returns its base URL, e.g.
// "ws://127.0.0.1:5099". It is idempotent: a builder that is already running and
// serving is reused as-is, so repeated calls are cheap and the build cache stays
// warm across server restarts.
func Ensure(ctx context.Context, opts Options) (string, error) {
	url := "ws://" + opts.addr()

	if opts.DockerExport && dockerSocketPath() == "" {
		return "", fmt.Errorf("builder container: the registry re-exports the local Docker daemon, "+
			"so the builder must reach that daemon, but DOCKER_HOST=%q is not a unix socket this "+
			"container can share; set --storage on the server, or --builder-url to a builder you manage",
			os.Getenv("DOCKER_HOST"))
	}

	c, err := newClient()
	if err != nil {
		return "", fmt.Errorf("builder container: %w", err)
	}

	st, err := c.containerState(ctx, opts.name())
	if err != nil {
		return "", err
	}

	// A managed builder must be running the CONFIGURATION we want, not merely
	// running. Its export behavior is decided by its own flags (see Options.
	// DockerExport), so a builder left over from a different server configuration
	// would silently export the wrong way. The fingerprint label makes that
	// detectable; a mismatch is recreated rather than reused.
	if st != stateAbsent {
		want := opts.fingerprint()
		got, err := c.containerLabel(ctx, opts.name(), configLabel)
		if err != nil {
			return "", err
		}
		if got != want {
			if err := c.containerRemove(ctx, opts.name()); err != nil {
				return "", err
			}
			st = stateAbsent
		}
	}

	// Only when we are not managing a container of our own does a responsive
	// address mean "someone already provided a builder" — a hand-started one is
	// taken at face value. Probing before the checks above would happily adopt our
	// own stale, wrongly-configured container.
	if st == stateAbsent && serving(ctx, opts.addr()) {
		return url, nil
	}

	switch {
	case st == stateRunning:
		// Running with the right config but not answering yet (still starting up) —
		// fall through to wait.
	case st == stateStopped:
		// A stopped container may predate a config change; recreate rather than
		// restart so the flags below are always what is actually running.
		if err := c.containerRemove(ctx, opts.name()); err != nil {
			return "", err
		}
		fallthrough
	default:
		// Default: build a throwaway image around the running binary, so the
		// builder is the exact same cornus as this server and no registry is
		// involved. An explicit Image pins a published one instead.
		ref := opts.Image
		if ref == "" {
			if ref, err = c.ensureSelfImage(ctx, opts.BaseImage); err != nil {
				return "", err
			}
		} else if err := c.ensureImage(ctx, ref); err != nil {
			return "", err
		}
		if err := c.createAndStart(ctx, opts, ref); err != nil {
			return "", err
		}
	}

	if err := waitServing(ctx, opts.addr(), 90*time.Second); err != nil {
		return "", fmt.Errorf("builder container %q did not become ready: %w", opts.name(), err)
	}
	return url, nil
}

// serving reports whether a cornus server answers the registry ping at addr.
func serving(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v2/", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return resp.StatusCode < 500
}

// waitServing polls until the builder answers or the deadline passes. The first
// start pulls the image and initializes BuildKit, so this is generous.
func waitServing(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if serving(ctx, addr) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Remove stops and deletes the managed builder container. Its cache volume is
// left behind so a later builder starts warm; callers wanting a clean slate
// remove the volume separately.
func Remove(ctx context.Context, opts Options) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	return c.containerRemove(ctx, opts.name())
}

// --- minimal Docker Engine API client -------------------------------------
//
// Deliberately tiny: this package needs image-presence, create, start, remove
// and inspect, nothing else. It mirrors pkg/deploy/dockerhost's DOCKER_HOST
// handling (unix and tcp) rather than pulling in the moby client.

type client struct {
	http *http.Client
	host string
}

func newClient() (*client, error) {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	switch {
	case strings.HasPrefix(host, "unix://"):
		sock := strings.TrimPrefix(host, "unix://")
		return &client{
			http: &http.Client{Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", sock)
				},
			}}, // no timeout: an image pull can be long
			host: "http://docker",
		}, nil
	case strings.HasPrefix(host, "tcp://"):
		addr := strings.TrimPrefix(host, "tcp://")
		return &client{http: &http.Client{}, host: "http://" + addr}, nil
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST %q", host)
	}
}

func (c *client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
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

func statusErr(resp *http.Response, okCodes ...int) error {
	for _, code := range okCodes {
		if resp.StatusCode == code {
			return nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return fmt.Errorf("docker api: %s: %s", resp.Status, strings.TrimSpace(string(b)))
}

type containerStatus int

const (
	stateAbsent containerStatus = iota
	stateStopped
	stateRunning
)

func (c *client) containerState(ctx context.Context, name string) (containerStatus, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+name+"/json", nil)
	if err != nil {
		return stateAbsent, fmt.Errorf("docker unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return stateAbsent, nil
	}
	if err := statusErr(resp, http.StatusOK); err != nil {
		return stateAbsent, err
	}
	var out struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return stateAbsent, err
	}
	if out.State.Running {
		return stateRunning, nil
	}
	return stateStopped, nil
}

// containerLabel reads one label off a container; "" when absent or gone.
func (c *client) containerLabel(ctx context.Context, name, label string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+name+"/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if err := statusErr(resp, http.StatusOK); err != nil {
		return "", err
	}
	var out struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Config.Labels[label], nil
}

// ensureImage pulls the image unless the daemon already has it, so an air-gapped
// host with a locally built image never needs the registry.
func (c *client) ensureImage(ctx context.Context, ref string) error {
	resp, err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	pull, err := c.do(ctx, http.MethodPost, "/images/create?fromImage="+name+"&tag="+tag, nil)
	if err != nil {
		return err
	}
	defer pull.Body.Close()
	if err := statusErr(pull, http.StatusOK); err != nil {
		return err
	}
	// The pull streams progress; it is complete only when the stream ends.
	_, err = io.Copy(io.Discard, pull.Body)
	return err
}

func (c *client) createAndStart(ctx context.Context, opts Options, image string) error {
	// --data-dir is a global flag and must precede the subcommand. It is passed
	// explicitly rather than relying on the image's CORNUS_DATA so this works for a
	// self-built image and a published one alike.
	cmd := []string{"--data-dir", dataDir, "serve", "--addr", opts.addr()}
	// Belt-and-braces against a builder delegating onward to itself. It runs
	// privileged, so its own mount(2) probe already succeeds and auto never
	// engages; this is set as an ENV rather than a flag because an older published
	// image would reject an unknown flag and fail to start, while an unknown env
	// var is simply ignored.
	env := []string{"CORNUS_BUILDER_AUTO=false"}
	mounts := []map[string]any{{
		"Type":   "volume",
		"Source": opts.volume(),
		"Target": dataDir,
	}}

	if opts.DockerExport {
		// Mirror the delegating server: re-export the Docker daemon rather than
		// keeping a registry. Deliberately NO --storage — that is what selects
		// host-native re-export, under which the builder loads the finished image
		// into the daemon instead of pushing it at a read-only registry.
		env = append(env, "CORNUS_DEPLOY_BACKEND=dockerhost")
		mounts = append(mounts, map[string]any{
			"Type":   "bind",
			"Source": dockerSocketPath(),
			"Target": "/var/run/docker.sock",
		})
	} else {
		// Explicit storage: see storageDir. It also pins the builder OUT of
		// host-native re-export, so it pushes the result at the target registry.
		cmd = append(cmd, "--storage", storageDir)
	}

	body := map[string]any{
		"Image": image,
		"Cmd":   cmd,
		"Env":   env,
		"Labels": map[string]string{
			"cornus.managed": "true",
			"cornus.role":    "builder",
			configLabel:      opts.fingerprint(),
		},
		"HostConfig": map[string]any{
			// mount(2) and overlayfs are exactly what the host process lacks.
			"Privileged": true,
			// Host networking keeps image refs meaning the same thing inside the
			// builder as on the host, so a target like localhost:5000/app resolves
			// to the same registry either side.
			"NetworkMode":   "host",
			"Mounts":        mounts,
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
		},
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/create?name="+opts.name(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusErr(resp, http.StatusCreated); err != nil {
		return fmt.Errorf("create builder container: %w", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return err
	}

	start, err := c.do(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil)
	if err != nil {
		return err
	}
	defer start.Body.Close()
	if err := statusErr(start, http.StatusNoContent, http.StatusNotModified); err != nil {
		return fmt.Errorf("start builder container: %w", err)
	}
	return nil
}

func (c *client) containerRemove(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/containers/"+name+"?force=1", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return statusErr(resp, http.StatusNoContent, http.StatusOK)
}
