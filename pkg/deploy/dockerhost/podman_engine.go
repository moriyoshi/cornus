package dockerhost

// podmanEngine implements Engine against Podman's NATIVE libpod REST API.
//
// Not the Docker-compat endpoints. That choice is the whole reason this type
// exists rather than a flag on engineClient, and it is not stylistic: three
// OPEN, compat-only Podman defects land on three of the four Docker-format
// obligations deploy.Backend imposes on us — compat container stats inflates CPU
// for containers in a pod, compat attach echoes the request body into the
// stream, and compat archive PUT diverges from Docker. cornus passes stats
// through verbatim, bridges attach raw, and implements CopyTo on that archive
// endpoint, so building on compat would have inherited all three in the paths
// hardest to notice were wrong.
//
// The measured surface is in .agents/docs/LTM/podman-libpod-api-findings.md.
// The headline is that libpod is MORE Docker-compatible than the compat layer
// where it counts: logs, exec-start and attach are stdcopy-framed byte-for-byte,
// and the archive trio speaks Docker's own X-Docker-Container-Path-Stat header
// and tar format. Those are pass-throughs. Stats is the one translation.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cornus/pkg/runtimeendpoint"
)

// podmanEngine speaks libpod over a resolved endpoint.
type podmanEngine struct {
	http *http.Client
	// base is the scheme+authority for request URLs.
	base string
	// prefix is the mandatory libpod version segment, e.g. "/v5.0.0". Every
	// libpod route carries one; see discoverAPIPrefix.
	prefix string
	// dial opens a raw connection for hijacked streams (exec-start, attach).
	dial       func(ctx context.Context) (net.Conn, error)
	hostHeader string
	remote     bool
}

// libpodPingPath is the ONE unversioned libpod route, and therefore the only one
// that can be called before the version prefix is known. Measured: every other
// /libpod/... path without a version segment returns 404.
const libpodPingPath = "/libpod/_ping"

// libpodVersionHeader carries the server's libpod API version on the ping
// response, e.g. "5.8.2".
const libpodVersionHeader = "Libpod-API-Version"

// newPodmanEngine connects to a resolved podman endpoint and settles the API
// version prefix before any other call is made.
func newPodmanEngine(ctx context.Context, ep runtimeendpoint.Endpoint) (*podmanEngine, error) {
	e := &podmanEngine{
		// Timeout 0: image pulls and log follows stream for a long time.
		http:       &http.Client{Transport: otelTransport(ep.Transport()), Timeout: 0},
		base:       ep.BaseURL(),
		dial:       ep.Dial,
		hostHeader: ep.HostHeader(),
		remote:     ep.NonLocal(),
	}
	prefix, err := e.discoverAPIPrefix(ctx)
	if err != nil {
		return nil, err
	}
	e.prefix = prefix
	return e, nil
}

// discoverAPIPrefix asks the daemon its libpod API version and pins the prefix
// to that MAJOR, e.g. "/v5.0.0" for any 5.x server.
//
// This is discovered rather than hardcoded because of how libpod handles a
// version it does not like: it does not reject the request, it silently serves a
// DIFFERENT PAYLOAD. Container inspect emits the v4 schema — Entrypoint as a
// space-joined string, StopSignal as an integer — for any prefix below 5.0.0.
// A stale or guessed prefix therefore yields wrong-shaped JSON with no error
// anywhere, which is the worst failure mode available. Measured on Podman 5.8.2.
//
// Pinning to the MAJOR rather than the exact version is deliberate: libpod's
// payload types are additive within a major line and break at major bumps, so
// the major is the granularity that actually describes the contract.
func (e *podmanEngine) discoverAPIPrefix(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+libpodPingPath, nil)
	if err != nil {
		return "", err
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("podman: cannot reach the libpod API at %s: %w", e.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("podman: libpod ping returned %s; is this a Podman API socket?", resp.Status)
	}
	v := resp.Header.Get(libpodVersionHeader)
	if v == "" {
		// A Docker daemon answers /_ping but not /libpod/_ping, and something else
		// entirely might answer both. Refusing here is much better than proceeding
		// with a guessed prefix against an unknown server.
		return "", fmt.Errorf("podman: the API at %s answered %s without a %s header, "+
			"so it is not a libpod endpoint (a Docker daemon does this)",
			e.base, libpodPingPath, libpodVersionHeader)
	}
	major, err := majorVersion(v)
	if err != nil {
		return "", fmt.Errorf("podman: cannot read %s %q: %w", libpodVersionHeader, v, err)
	}
	return fmt.Sprintf("/v%d.0.0", major), nil
}

// majorVersion extracts the leading major from a "5.8.2"-style version.
func majorVersion(v string) (int, error) {
	head, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0, fmt.Errorf("%q is not a version", v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%q has no usable major version", v)
	}
	return n, nil
}

// url builds a full libpod URL: base + version prefix + path.
//
// Every caller goes through here so the version segment cannot be forgotten at
// one site — an unversioned libpod path is a 404, which at least fails loudly,
// but only after the request has been built and sent.
func (e *podmanEngine) url(path string) string { return e.base + e.prefix + "/libpod" + path }

// nonLocal implements Engine.
func (e *podmanEngine) nonLocal() bool { return e.remote }

// usernsRemapped is always false for podman: it REPORTS its mapping over /info
// (idMappings), so there is nothing to refuse — Backend.IDMap reads the real map
// instead of asking this question.
func (e *podmanEngine) usernsRemapped(context.Context) (bool, error) { return false, nil }
