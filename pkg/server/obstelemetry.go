package server

import (
	"context"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// Making `x-cornus-telemetry: {}` mean "send it to cornus".
//
// The telemetry spec's endpoint used to be mandatory, which meant the whole
// workload-telemetry feature only paid off for users who already ran an OTLP
// backend. With a store on the server there is now a sensible default, and this
// is where it is applied: an active telemetry spec that names no endpoint gets
// the server's own OTLP receiver.
//
// It happens on the SERVER rather than in deploy.BuildTelemetryWiring because
// the server is the first participant that knows whether a store exists at all.
// Rewriting the spec before Apply also means every backend keeps consuming a spec
// with a concrete endpoint, exactly as before — no backend learns about the
// store, and there is no second code path to keep in sync.

// otlpReceiverPath is the prefix an OTLP/HTTP exporter appends `/v1/{signal}` to.
const otlpReceiverPath = "/.cornus/v1/otlp"

// normalizeTelemetry fills in this server's own OTLP endpoint when a workload
// asked for telemetry without naming one. It edits spec in place and is a no-op
// for every other case.
//
// A spec that cannot be defaulted is left untouched rather than rejected here:
// BuildTelemetryWiring reports the missing endpoint with the remedy, and keeping
// one error message for one condition beats two that disagree.
func (s *Server) normalizeTelemetry(ctx context.Context, spec *api.DeploySpec, backendName string) {
	t := spec.Telemetry
	if !t.Active() {
		return
	}
	// The mux decision is made here, before any backend sees the spec, because
	// this is the only place that knows BOTH facts it depends on: whether cornus
	// is the destination, and which backend will realize the workload.
	defer s.defaultTelemetryMux(ctx, spec, backendName)
	if strings.TrimSpace(t.Endpoint) != "" {
		return
	}
	log := logging.FromContext(ctx)
	if !s.obsEnabled() {
		log.WarnContext(ctx, "workload requested telemetry with no endpoint, but this server has no observability store to default to",
			"deployment", spec.Name,
			"remedy", "start the server with --obs (and a build including -tags imbh), or set an explicit telemetry endpoint")
		return
	}
	// The workload's Collector reaches cornus from inside the pod or the app's
	// network namespace, so the address has to be the one cornus advertises to
	// sidecars — the same variable every other caretaker role dials. A listen
	// address like ":5000" is not usable from there, so guessing from it would
	// produce a wiring that fails at runtime instead of at deploy time.
	adv := advertiseURL()
	if adv == "" {
		log.WarnContext(ctx, "workload requested telemetry with no endpoint and this server has a store, but CORNUS_ADVERTISE_URL is unset so the workload has no address to export to",
			"deployment", spec.Name,
			"remedy", "set CORNUS_ADVERTISE_URL to the URL a workload can reach this server at, or set an explicit telemetry endpoint")
		return
	}

	t.Endpoint = strings.TrimSuffix(adv, "/") + otlpReceiverPath
	// The receiver serves OTLP over HTTP only; there is no gRPC listener to point
	// at, so the protocol is fixed here rather than left to the spec's default of
	// grpc. This also decides which loopback port the app is told to use.
	t.Protocol = "http/protobuf"
	if strings.HasPrefix(strings.ToLower(t.Endpoint), "http://") {
		// A plaintext advertise URL means plaintext export; saying so explicitly
		// keeps the Collector from trying to negotiate TLS against it.
		t.Insecure = true
	}
	log.InfoContext(ctx, "workload telemetry defaulted to this server's observability store",
		"deployment", spec.Name, "endpoint", t.Endpoint)

	// Auth is deliberately not synthesized here. If this server requires a bearer
	// token, the Collector needs one too, and minting a workload-scoped credential
	// is a different (and security-relevant) decision than picking a default URL.
	// Warn rather than emit a wiring that will 401 in the background where nobody
	// is watching.
	if s.auth != nil && s.auth.enabled() && len(t.Headers) == 0 {
		log.WarnContext(ctx, "this server requires authentication but the workload's telemetry export carries no credential; its exports will be rejected",
			"deployment", spec.Name,
			"remedy", "add an authorization header under x-cornus-telemetry.headers (or --telemetry-header)")
	}
}

// defaultTelemetryMux decides whether this workload's telemetry rides the
// caretaker connection, for a spec that did not say.
//
// It defaults ON, because when cornus is the destination the mux is simply the
// better path: it needs no reachable URL, no route from the pod, and no
// credential of its own, and it cannot be broken by a NetworkPolicy that the
// direct dial silently depends on. The direct HTTP dial remains available with an
// explicit false, for the rare case of wanting to observe the export as ordinary
// traffic.
//
// Two conditions gate it, and each corresponds to a way the mux would otherwise
// be wrong rather than merely unnecessary:
//
//   - The user did not decide. An explicit choice always wins.
//   - The destination is THIS server. Riding a caretaker connection cannot deliver
//     to a third-party backend, because there is no connection to it.
//
// There is deliberately no backend condition. Every backend that runs a
// telemetry caretaker can carry the exports over it, and the case the mux solves
// — the workload's network having no route to the server — is not a Kubernetes
// peculiarity: a remote docker host (CORNUS_DOCKER_REMOTE) and an isolated
// container network hit it just as squarely. The one backend without telemetry at
// all (incus) never reaches here.
func (s *Server) defaultTelemetryMux(ctx context.Context, spec *api.DeploySpec, backendName string) {
	t := spec.Telemetry
	if !t.Active() || t.MuxDecided() {
		return
	}
	if !s.telemetryTargetsSelf(t.Endpoint) {
		return
	}
	on := true
	t.ViaMux = &on
	logging.FromContext(ctx).InfoContext(ctx, "workload telemetry will ride the caretaker connection",
		"deployment", spec.Name, "backend", backendName)
}

// telemetryTargetsSelf reports whether an export endpoint points at this server's
// own OTLP receiver.
//
// It compares against the advertised URL rather than trying to resolve hosts: the
// advertised URL is exactly what normalizeTelemetry writes and what the caretaker
// dials, so string agreement is the same question, and a DNS-resolving comparison
// would be both slower and able to answer "yes" for an address the caretaker
// cannot actually reach.
func (s *Server) telemetryTargetsSelf(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	adv := advertiseURL()
	if adv == "" {
		return false
	}
	return strings.TrimSuffix(endpoint, "/") == strings.TrimSuffix(adv, "/")+otlpReceiverPath
}
