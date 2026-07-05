// Package server wires cornus's subsystems behind a single HTTP server:
// the OCI registry under /v2/*, and the build and deploy APIs under /.cornus/v1/*.
package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"os"

	"github.com/google/go-containerregistry/pkg/name"
	"go.opentelemetry.io/otel/trace"

	"cornus/pkg/api"
	"cornus/pkg/blockcache"
	"cornus/pkg/build/builder"
	"cornus/pkg/config"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/barehost"
	"cornus/pkg/deploy/containerdhost"
	"cornus/pkg/deploy/dockerhost"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/deploy/kubernetes"
	"cornus/pkg/egresspolicy"
	"cornus/pkg/hub"
	"cornus/pkg/imageref"
	"cornus/pkg/kubehub"
	"cornus/pkg/logging"
	"cornus/pkg/observability"
	"cornus/pkg/registry"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/storage"
	"cornus/pkg/supervisor"
)

// Server is the unified cornus HTTP server.
type Server struct {
	cfg   config.Config
	store *storage.Backend
	mux   *http.ServeMux

	// registrySource is the validated CORNUS_REGISTRY_SOURCE mode ("" for the
	// persistent CAS, "docker-daemon", or "containerd"). routes wires the matching
	// re-export source.
	registrySource string
	// daemonImageAPI, when non-nil, makes the /v2/* registry re-export the local
	// Docker daemon's image store on a miss (host-native on dockerhost, read-only).
	// Constructed and validated in New; wired by routes.
	daemonImageAPI registry.DockerImageAPI
	// containerdStore, when non-nil, backs the /v2/* registry with a host
	// containerd's content+image store (host-native on containerd) — a full
	// read-write view, so a push imports into the store. Built in New; wired by routes.
	containerdStore registry.Store

	// TLSCertFile and TLSKeyFile, when both set, make Run serve HTTPS
	// (ListenAndServeTLS); otherwise Run serves plaintext HTTP. Set by the
	// caller (cmd/cornus serve) before Run.
	TLSCertFile string
	TLSKeyFile  string

	// TLSClientCAFile, when set together with TLS serving, is a PEM CA bundle used
	// to verify client certificates. Run configures VerifyClientCertIfGiven (NOT
	// RequireAndVerify), so probes and bearer-only clients still work without a
	// cert, but a presented cert must chain to this CA. A verified cert's
	// CommonName becomes the caller identity (see authenticator.mtls).
	TLSClientCAFile string

	// ready reflects real readiness: it flips true once Run has bound its
	// listener and is about to serve, and back to false during shutdown. The
	// /readyz probe reports 503 until it is true. /healthz stays a pure
	// liveness check independent of this flag.
	ready atomic.Bool

	// sup supervises the server's own long-lived internal goroutines (today:
	// the periodic GC loop; tunnelManager/hub loops are natural future
	// additions) — a panic or transient failure in one restarts it in place
	// instead of taking down the whole process. Constructed in New (not Run) so
	// it exists even for tests that build a Server without calling Run;
	// supCancel ends every supervised child's context, and closeResources
	// calls it then Wait()s so shutdown is synchronous.
	sup       *supervisor.Supervisor
	supCancel context.CancelFunc

	// buildSem bounds concurrent /.cornus/v1/build executions against the shared build
	// engine. It is a counting semaphore (buffered channel); a build blocks
	// (queues) until a slot is free. Sized by CORNUS_BUILD_CONCURRENCY or
	// NumCPU (see buildConcurrency).
	buildSem chan struct{}

	// deployLocks serialises concurrent /.cornus/v1/deploy applies (and deletes) of the
	// same name so they do not race on the backend's delete-then-create. Keyed
	// by deployment name; different names proceed concurrently. Each entry is
	// reference-counted so a name's mutex is removed once no request holds or is
	// waiting on it — the map does not grow without bound as unique/ephemeral
	// deployment names churn.
	deployLocksMu sync.Mutex
	deployLocks   map[string]*deployLock

	backendMu  sync.Mutex
	backend    deploy.Backend
	newBackend func() (deploy.Backend, error)

	engineMu sync.Mutex
	engine   *builder.Engine

	// gcInterval is the CORNUS_GC_INTERVAL period of the background storage-GC
	// scheduler (gcschedule.go); zero means the feature is off (no ticker, no
	// supervised child). gcTicker is stopped by stopPeriodicGC; the loop itself
	// is a supervised child of sup (see closeResources), so its shutdown is
	// cancel-and-wait, not a dedicated stop/done channel pair. gcRunning is the
	// no-overlap guard.
	gcInterval time.Duration
	gcTicker   *time.Ticker
	gcRunning  atomic.Bool
	// gcRun overrides the GC core the scheduler invokes (tests only); nil means
	// the real runGC.
	gcRun func(context.Context) (gcResponse, error)
	// gcGate, when non-nil, is the CORNUS_GC_LEASE leader gate consulted before
	// each periodic run (gcschedule.go): only the Lease holder sweeps, so
	// multi-replica deployments can enable the interval everywhere. nil (the
	// default, env unset) runs every tick unconditionally.
	gcGate func(context.Context) (bool, error)

	// mounts tracks live deploy-attach sessions so pod mount-agents can be
	// bridged to a caller's 9P export (kubernetes live mounts).
	mounts *mountRegistry

	// remoteCompanions maps an app instance identity ("name/replica") to its
	// live remote-companion/caretaker connection, so ForwardPort can reroute
	// through it (dockerhost/containerdhost remote mode) — see
	// pkg/caretaker's PortForwardRole and pkg/remotecompanion. Shared with the
	// active backend at construction time (defaultBackendFactory) since the
	// registry is populated by handleCaretakerUnified (pkg/server) but read by
	// the backend's ForwardPort (pkg/deploy/dockerhost, .../containerdhost) —
	// pkg/remotecompanion is a neutral package to avoid an import cycle.
	remoteCompanions *remotecompanion.Registry
	// execAgentChannels maps an app instance identity to the currently-active
	// `cornus exec --forward-agent` client channel, so relayAgentMuxed can
	// relay a caretaker's AgentRelayRole connection to the real local agent.
	// Purely server-internal (no backend needs it).
	execAgentChannels *remotecompanion.Registry

	// egressGateway enables the client-side-egress GATEWAY terminus: this server
	// acts as the egress node for durable / --detach workloads, dialing their
	// gateway-routed destinations directly. Off by default (CORNUS_EGRESS_GATEWAY);
	// a caretaker's gateway request is dropped when disabled.
	egressGateway bool
	// egressPolicy is the optional operator ceiling over gateway egress
	// (CORNUS_EGRESS_POLICY, a JSON EgressSpec): a destination it routes to "deny" is
	// refused regardless of the deployment's own policy. nil means no ceiling (the
	// operator opted in via egressGateway and permits any gateway destination).
	egressPolicy egresspolicy.Policy

	// hub is the workload-to-workload overlay registry (a hub.Store, swappable for a
	// distributed backend): spokes register the services they host and open data
	// streams the hub relays (ARCHITECTURE.md "Workload-to-workload hub"). It is
	// hub.NewRegistry (in-memory, single-replica default) unless CORNUS_HUB_REDIS
	// selects the distributed RedisStore or CORNUS_HUB_STORE=kube selects the
	// Kubernetes-native KubeStore, either of which makes the hub multi-replica
	// (ARCHITECTURE.md "Multi-replica hub").
	hub hub.Store
	// forwardToken is this server's own full credential (CORNUS_AUTH_TOKEN), sent as
	// a bearer token on inter-replica /.cornus/v1/hub/forward dials so the peer replica's
	// auth accepts them. Empty when auth is off (no header sent). Multi-replica only.
	forwardToken string
	// forwardTLS, when non-nil (CORNUS_HUB_FORWARD_CA), is the TLS config for
	// inter-replica /.cornus/v1/hub/forward dials: the configured CA appended to the
	// system roots. nil dials with the system trust store only (see hub.go).
	forwardTLS *tls.Config
	// catalog fans hub-catalog changes out to watching spokes; created lazily on
	// first use so it binds the final hub store (see hub_watch.go catalogWatch).
	catalogOnce sync.Once
	catalog     *catalogNotifier
	// policy, when non-nil, is the hub's central connection policy (caller identity
	// → callee service), enforced at relay time. nil allows all (dev default).
	policy *hub.Policy

	// tracer and metrics are cornus's own OTel instruments for build/deploy
	// operations. They come from the global providers, which are no-ops unless
	// telemetry was enabled at startup (see pkg/observability).
	tracer  trace.Tracer
	metrics instruments

	// auth guards the mux with opt-in bearer authentication. When no auth env is
	// configured it is disabled and Handler wraps the mux with a pure pass-through,
	// so the server behaves exactly as it did before auth existed.
	auth *authenticator

	// apiPolicy authorizes per-identity API actions (deploy, build), keyed on the
	// caller identity (mTLS CommonName or JWT sub). nil when CORNUS_API_POLICY is
	// unset, which allows all (dev default).
	apiPolicy *apiPolicy

	// tunnels hosts public tunnels to deployments (pkg/tunnel), one per
	// deployment name. Always non-nil; the backend is selected by
	// CORNUS_TUNNEL_BACKEND (default ngrok) with CORNUS_TUNNEL_AUTHTOKEN as an
	// optional server-side default credential.
	tunnels *tunnelManager

	// fileCache is the server-side per-file block cache for on-demand remote
	// file reads over 9P (pkg/blockcache). nil when CORNUS_FILE_CACHE is off, in
	// which case the kernel-9p mount paths blindly pipe frames as before.
	fileCache *blockcache.Cache
}

// New constructs a Server backed by the given config and storage backend. It
// returns an error when the hub policy env vars (or CORNUS_GC_INTERVAL) are set
// but malformed, so a misconfiguration is a hard startup failure rather than a
// silent open.
func New(cfg config.Config, st *storage.Backend) (*Server, error) {
	policy, err := loadHubPolicy()
	if err != nil {
		return nil, err
	}
	auth, err := newAuthenticator()
	if err != nil {
		return nil, err
	}
	apiPol, err := loadAPIPolicy()
	if err != nil {
		return nil, err
	}
	// Interaction warning: an explicit "pull" rule in the API policy wins over
	// CORNUS_REGISTRY_ANONYMOUS_PULL (anonymous callers have no identity, and a
	// configured policy fails closed on the empty identity — see registryAuthz).
	// Setting both is almost certainly a misconfiguration, so say so once here.
	if apiPol.MentionsAction("pull") && auth.anonPull {
		ctx := context.Background()
		logging.FromContext(ctx).WarnContext(ctx, "CORNUS_API_POLICY mentions the \"pull\" action while CORNUS_REGISTRY_ANONYMOUS_PULL is enabled; the explicit pull policy wins, so anonymous registry pulls will be denied")
	}
	store, err := newHubStore(cfg)
	if err != nil {
		return nil, err
	}
	forwardTLS, err := loadHubForwardTLS()
	if err != nil {
		return nil, err
	}
	gcInterval, err := gcIntervalFromEnv()
	if err != nil {
		return nil, err
	}
	gcGate, err := gcLeaseGateFromEnv(gcInterval)
	if err != nil {
		return nil, err
	}
	tunnels, err := newTunnelManager(os.Getenv("CORNUS_TUNNEL_BACKEND"), os.Getenv("CORNUS_TUNNEL_AUTHTOKEN"))
	if err != nil {
		return nil, err
	}
	egressPolicy, err := parseEgressGatewayPolicy(os.Getenv("CORNUS_EGRESS_POLICY"))
	if err != nil {
		return nil, err // fail closed on a malformed operator egress policy
	}
	// The registry source (CORNUS_REGISTRY_SOURCE / host-native default): when a
	// host store is re-exported through /v2/*, resolve it fail-closed and build any
	// client here so a bad DOCKER_HOST or an incompatible deploy backend is a hard
	// startup error, not a silent 404.
	regPlan, err := resolveRegistrySource(cfg)
	if err != nil {
		return nil, err
	}
	regSource := regPlan.source
	var daemonImageAPI registry.DockerImageAPI
	if regSource == registrySourceDockerDaemon {
		daemonImageAPI, err = registry.NewDockerImageAPI()
		if err != nil {
			return nil, err
		}
	}
	var containerdStore registry.Store
	if regSource == registrySourceContainerd {
		// Back /v2/* with the host containerd's content+image store directly: a
		// read-write view, so a build that pushes to /v2/* imports into the store
		// the containerd deploy backend runs from (no build-worker special-casing
		// needed). Dialed lazily, so a bad address surfaces on first use.
		containerdStore, err = registry.NewContainerdStore(
			os.Getenv("CORNUS_CONTAINERD_ADDRESS"), os.Getenv("CORNUS_CONTAINERD_NAMESPACE"), cfg.UploadsDir())
		if err != nil {
			return nil, err
		}
	}
	var fileCache *blockcache.Cache
	if cfg.FileCacheEnabled {
		fcStore, err := blockcache.NewDiskStore(cfg.FileCacheDir, cfg.FileCacheChunkSize)
		if err != nil {
			return nil, fmt.Errorf("file cache: %w", err)
		}
		fileCache = blockcache.New(fcStore, cfg.FileCacheChunkSize)
	}
	supCtx, supCancel := context.WithCancel(context.Background())
	s := &Server{
		egressGateway:     envTruthy(os.Getenv("CORNUS_EGRESS_GATEWAY")),
		egressPolicy:      egressPolicy,
		cfg:               cfg,
		store:             st,
		mux:               http.NewServeMux(),
		buildSem:          make(chan struct{}, buildConcurrency()),
		deployLocks:       map[string]*deployLock{},
		mounts:            newMountRegistry(),
		remoteCompanions:  remotecompanion.NewRegistry(),
		execAgentChannels: remotecompanion.NewRegistry(),
		hub:               store,
		forwardToken:      os.Getenv("CORNUS_AUTH_TOKEN"),
		forwardTLS:        forwardTLS,
		policy:            policy,
		tracer:            observability.Tracer(),
		metrics:           newInstruments(),
		auth:              auth,
		apiPolicy:         apiPol,
		gcInterval:        gcInterval,
		gcGate:            gcGate,
		tunnels:           tunnels,
		fileCache:         fileCache,
		sup:               supervisor.New(supCtx, nil),
		supCancel:         supCancel,
		registrySource:    regSource,
		daemonImageAPI:    daemonImageAPI,
		containerdStore:   containerdStore,
	}
	// newBackend is set after s exists (not in the struct literal above) so its
	// closure can capture s.remoteCompanions — the active backend needs it to
	// reroute ForwardPort through a remote-mode companion.
	s.newBackend = func() (deploy.Backend, error) { return defaultBackendFactory(cfg, s.remoteCompanions) }
	s.routes()
	s.registerFileCacheMetrics()
	return s, nil
}

// newHubStore selects the hub registry backend, in precedence order:
//
//   - CORNUS_HUB_REDIS (a redis:// URL) → the distributed RedisStore (portable
//     multi-replica; see ARCHITECTURE.md "Multi-replica design rationale",
//     backend option B);
//   - else CORNUS_HUB_STORE=kube → the Kubernetes-native KubeStore (the
//     recommended multi-replica store on the kubernetes backend, reusing the API
//     server instead of Redis; "Backend option D");
//   - else the in-memory Registry — the single-replica default, byte-for-byte the
//     prior behavior.
//
// Both distributed stores take CORNUS_REPLICA_ID (falling back to POD_NAME, the
// hostname, then a random id) and a forward base URL peers dial to forward a delivery
// to this replica. A malformed/unreachable Redis URL, or a KubeStore that cannot
// reach the cluster or install its CRD, is a hard startup error (fail closed).
func newHubStore(cfg config.Config) (hub.Store, error) {
	replicaID := os.Getenv("CORNUS_REPLICA_ID")
	if replicaID == "" {
		replicaID = defaultReplicaID()
	}
	if redisURL := os.Getenv("CORNUS_HUB_REDIS"); redisURL != "" {
		return hub.NewRedisStore(context.Background(), redisURL, replicaID, os.Getenv("CORNUS_HUB_FORWARD_URL"))
	}
	if os.Getenv("CORNUS_HUB_STORE") == "kube" {
		return kubehub.NewFromEnv(context.Background(), namespaceFromEnv(), replicaID, hubForwardAddr(cfg))
	}
	return hub.NewRegistry(), nil
}

// namespaceFromEnv mirrors the kubernetes deploy backend's namespace resolution.
func namespaceFromEnv() string {
	if ns := os.Getenv("CORNUS_K8S_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

// hubForwardAddr is the inter-replica base URL peers dial to forward a delivery to
// this replica: CORNUS_HUB_FORWARD_URL when set, else ws://$POD_IP:<port> derived
// from the downward-API Pod IP and the server's listen port (empty when POD_IP is
// unset, which leaves remote-delivery forwarding unavailable to this replica).
func hubForwardAddr(cfg config.Config) string {
	if u := os.Getenv("CORNUS_HUB_FORWARD_URL"); u != "" {
		return u
	}
	podIP := os.Getenv("POD_IP")
	if podIP == "" {
		return ""
	}
	port := "5000"
	if _, p, err := net.SplitHostPort(cfg.HTTPAddr); err == nil && p != "" {
		port = p
	}
	return "ws://" + net.JoinHostPort(podIP, port)
}

// defaultReplicaID derives a replica id when CORNUS_REPLICA_ID is unset: the
// downward-API POD_NAME, then the hostname (stable per pod), or a random hex id when
// neither is available.
func defaultReplicaID() string {
	if p := os.Getenv("POD_NAME"); p != "" {
		return p
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "replica-" + hex.EncodeToString(b[:])
	}
	return "replica"
}

// buildConcurrency is the number of concurrent /.cornus/v1/build executions permitted.
// CORNUS_BUILD_CONCURRENCY overrides the NumCPU default; a non-positive or
// unparseable value falls back to the default.
func buildConcurrency() int {
	if raw := os.Getenv("CORNUS_BUILD_CONCURRENCY"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 1
}

// deployLock is a reference-counted per-name mutex. waiters counts the requests
// that hold or are waiting on mu; it is protected by the Server's deployLocksMu,
// not by mu itself, so the map entry can be removed exactly when the last
// user releases it (see acquireDeployLock/releaseDeployLock).
type deployLock struct {
	mu      sync.Mutex
	waiters int
}

// acquireDeployLock takes the per-name deploy mutex for name, creating the map
// entry on first use, and returns the locked *deployLock. The reference count is
// bumped under deployLocksMu before mu is taken, so a concurrent releaser can
// never delete the entry out from under a waiter. Every acquire must be paired
// with exactly one releaseDeployLock(name, l).
func (s *Server) acquireDeployLock(name string) *deployLock {
	s.deployLocksMu.Lock()
	l := s.deployLocks[name]
	if l == nil {
		l = &deployLock{}
		s.deployLocks[name] = l
	}
	l.waiters++
	s.deployLocksMu.Unlock()
	l.mu.Lock()
	return l
}

// releaseDeployLock unlocks l and drops its reference; when no other request
// holds or is waiting on it, the map entry for name is removed so the map does
// not accumulate a permanent mutex per distinct deployment name ever seen.
func (s *Server) releaseDeployLock(name string, l *deployLock) {
	l.mu.Unlock()
	s.deployLocksMu.Lock()
	l.waiters--
	if l.waiters == 0 {
		// Only delete the entry we still own: a fresh acquire for the same name
		// would have installed a new *deployLock, so guard against clobbering it.
		if s.deployLocks[name] == l {
			delete(s.deployLocks, name)
		}
	}
	s.deployLocksMu.Unlock()
}

// loadHubPolicy builds the hub connection policy from two JSON env vars, each an
// object mapping an identity to a list: CORNUS_HUB_POLICY is the REACH matrix
// (caller → callee services, e.g. {"web":["db","cache"]}); CORNUS_HUB_REGISTER_POLICY
// is the REGISTER matrix (identity → hostable service names). Empty/absent/invalid
// on a dimension leaves it unenforced (allow all on that dimension).
func loadHubPolicy() (*hub.Policy, error) {
	reach, err := parsePolicyEnv("CORNUS_HUB_POLICY")
	if err != nil {
		return nil, err
	}
	register, err := parsePolicyEnv("CORNUS_HUB_REGISTER_POLICY")
	if err != nil {
		return nil, err
	}
	return hub.NewPolicy(reach, register), nil
}

// loadAPIPolicy builds the per-identity API authorization policy from
// CORNUS_API_POLICY (a JSON object mapping identity to a list of allowed actions,
// e.g. {"ci-bot":["deploy","build"],"admin":["*"]}). Absent/empty leaves it
// unconfigured (allow-all); malformed JSON is a hard error (fail closed), exactly
// like the hub policy.
func loadAPIPolicy() (*apiPolicy, error) {
	rules, err := parsePolicyEnv("CORNUS_API_POLICY")
	if err != nil {
		return nil, err
	}
	return newAPIPolicy(rules), nil
}

// parsePolicyEnv parses a policy env var (a JSON object mapping identity to a
// list of names). An empty/absent value leaves that dimension unenforced. Invalid
// JSON is a hard error (fail closed) rather than a silent allow-all, so a
// misconfigured policy never silently disables enforcement.
func parsePolicyEnv(name string) (map[string][]string, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, nil
	}
	var rules map[string][]string
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return rules, nil
}

// defaultBackendFactory selects the deploy backend from CORNUS_DEPLOY_BACKEND
// ("dockerhost" by default, "kubernetes", "containerd", or "bare" — the
// daemonless OCI-runtime backend). companions is the
// server's per-instance companion-connection registry (remote_companion.go's
// pkg/remotecompanion.Registry, populated by handleCaretakerUnified), shared
// with dockerhost/containerdhost so their ForwardPort can reroute through a
// remote-mode companion instead of dialing the instance directly.
func defaultBackendFactory(cfg config.Config, companions *remotecompanion.Registry) (deploy.Backend, error) {
	switch os.Getenv("CORNUS_DEPLOY_BACKEND") {
	case "kubernetes", "k8s":
		return kubernetes.New()
	case "containerd":
		// Same default-deny rationale and MountsDir carve-out as the dockerhost
		// branch below. CORNUS_CONTAINERD_REMOTE opts into the always-on remote
		// companion (see pkg/deploy/containerdhost/mounts_linux.go) — it does NOT
		// make containerd itself remote-reachable (its client dialer is
		// hard-coded to a local unix socket), it only changes how client-local
		// mounts/port-forward/exec-agent-forwarding are realized.
		pol := hostpolicy.FromEnv()
		pol.AllowBindPrefixes = append(pol.AllowBindPrefixes, cfg.MountsDir())
		return containerdhost.New(containerdhost.Config{DataDir: cfg.DataDir}, containerdhost.WithPolicy(pol),
			containerdhost.WithRemote(os.Getenv("CORNUS_CONTAINERD_REMOTE") != ""),
			containerdhost.WithAgentImage(os.Getenv("CORNUS_AGENT_IMAGE")),
			containerdhost.WithCompanionRegistry(companions))
	case "bare":
		// Daemonless: drive an OCI runtime (runc/crun/youki) directly, no
		// containerd/dockerd. Same default-deny policy and MountsDir carve-out
		// as the other host backends; CORNUS_BARE_REMOTE opts into the always-on
		// remote companion (as CORNUS_CONTAINERD_REMOTE does for containerd).
		pol := hostpolicy.FromEnv()
		pol.AllowBindPrefixes = append(pol.AllowBindPrefixes, cfg.MountsDir())
		return barehost.New(barehost.Config{DataDir: cfg.DataDir}, barehost.WithPolicy(pol),
			barehost.WithRemote(os.Getenv("CORNUS_BARE_REMOTE") != ""),
			barehost.WithAgentImage(os.Getenv("CORNUS_AGENT_IMAGE")),
			barehost.WithCompanionRegistry(companions))
	default:
		// Default-deny the host-privilege surface (privileged containers and
		// arbitrary host bind sources), since the HTTP API is unauthenticated —
		// operators opt back in with CORNUS_ALLOW_PRIVILEGED /
		// CORNUS_ALLOW_BIND_SOURCES. The deploy-attach path rewrites
		// client-local mount sources to <DataDir>/mounts/<session>/..., so that
		// server-controlled location is always permitted to keep live
		// client-local mounts working without opting in.
		//
		// CORNUS_DOCKER_REMOTE opts into the always-on remote companion (see
		// pkg/deploy/dockerhost/mounts.go) for a Docker daemon that is not
		// co-located with this server (e.g. DOCKER_HOST=tcp://...); unset, mounts/
		// port-forward/tunnel/exec-agent-forwarding keep using the existing
		// single-host fast paths (or, for exec-agent-forwarding, stay unsupported).
		pol := dockerhost.PolicyFromEnv()
		pol.AllowBindPrefixes = append(pol.AllowBindPrefixes, cfg.MountsDir())
		dockerOpts := []dockerhost.Option{
			dockerhost.WithPolicy(pol),
			dockerhost.WithRemote(os.Getenv("CORNUS_DOCKER_REMOTE") != ""),
			dockerhost.WithAgentImage(os.Getenv("CORNUS_AGENT_IMAGE")),
			dockerhost.WithCompanionRegistry(companions),
		}
		// When /v2/* re-exports the daemon (host-native), pulling a cornus-served ref
		// the daemon already has round-trips back to itself. Skip the pull for such
		// refs (bare or loopback-host), which is where cornus-built and locally-built
		// images live.
		if plan, _ := resolveRegistrySource(cfg); plan.source == registrySourceDockerDaemon {
			dockerOpts = append(dockerOpts, dockerhost.WithSkipPullIfLocal(localRegistryRef))
		}
		return dockerhost.New(dockerOpts...)
	}
}

// localRegistryRef reports whether an image ref is one cornus's own registry
// serves: a bare ref (no registry host) or one whose host is the loopback
// interface (where a co-located cornus registry listens). External refs
// (docker.io, ghcr.io, ...) are not local and must still be pulled.
func localRegistryRef(ref string) bool {
	host, _ := imageref.SplitHostRepo(ref)
	if host == "" { // bare ref → cornus's builtin registry
		return true
	}
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// getBackend lazily constructs the deploy backend so the server can start even
// when no Docker host is reachable (the backend is only needed for /.cornus/v1/deploy).
func (s *Server) getBackend() (deploy.Backend, error) {
	s.backendMu.Lock()
	defer s.backendMu.Unlock()
	if s.backend != nil {
		return s.backend, nil
	}
	b, err := s.newBackend()
	if err != nil {
		return nil, err
	}
	s.backend = b
	return b, nil
}

// getEngine lazily constructs the in-process build engine. It is only created
// on first /.cornus/v1/build, since it allocates a BuildKit worker.
func (s *Server) getEngine() (*builder.Engine, error) {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	if s.engine != nil {
		return s.engine, nil
	}
	e, err := builder.New(builder.Config{Root: s.cfg.CacheDir(), Rootless: s.cfg.Rootless})
	if err != nil {
		return nil, err
	}
	s.engine = e
	return e, nil
}

// registrySource* are the CORNUS_REGISTRY_SOURCE values that make /v2/* re-export
// a local image store instead of the persistent CAS. The concrete source names
// (docker-daemon / containerd) are internal; the operator-facing knob is
// registrySourceHostNative, which resolves to one of them per deploy backend.
const (
	registrySourceDockerDaemon = "docker-daemon"
	registrySourceContainerd   = "containerd"

	// registrySourceHostNative is the CORNUS_REGISTRY_SOURCE value: re-export the
	// deploy backend's own local image store through /v2/*. It resolves to the
	// docker-daemon source under the dockerhost backend and the containerd source
	// under the containerd backend, and is the DEFAULT on those host backends when
	// no content store (--storage) and no mirror are configured.
	registrySourceHostNative = "host-native"
	// registrySourceOff forces the classic persistent CAS even under a host
	// backend where host-native would otherwise be the default.
	registrySourceOff = "off"
)

// registrySourcePlan is the resolved registry configuration.
type registrySourcePlan struct {
	// source is the concrete re-export source to wire: "" (none — classic CAS),
	// registrySourceDockerDaemon, or registrySourceContainerd.
	source string
	// pure is true when the registry keeps NO content store (nil): the host store
	// is authoritative and writes are rejected. false keeps a CAS — classic when
	// source=="", or a union CAS+source when an explicit --storage accompanies a
	// source.
	pure bool
}

// isHostBackend reports whether a deploy backend re-exports a local host store
// (so host-native applies). bare and kubernetes do not.
func isHostBackend(backend string) bool {
	return backend == "" || backend == "dockerhost" || backend == registrySourceContainerd
}

// hostNativeSourceFor maps a host deploy backend to its re-export source.
func hostNativeSourceFor(backend string) (string, error) {
	switch backend {
	case "", "dockerhost":
		return registrySourceDockerDaemon, nil
	case registrySourceContainerd:
		return registrySourceContainerd, nil
	default:
		return "", fmt.Errorf("CORNUS_REGISTRY_SOURCE=host-native requires a host deploy backend (dockerhost or containerd), but CORNUS_DEPLOY_BACKEND is %q", backend)
	}
}

// resolveRegistrySource resolves CORNUS_REGISTRY_SOURCE (fail-closed) into the
// registry plan. host-native — set explicitly, or the DEFAULT on a host backend
// with no explicit --storage and no mirror — re-exports the backend's local
// store; off (or a non-host backend, or an explicit --storage without a source)
// keeps the classic CAS. host-native with no --storage keeps no content store
// (pure); with an explicit --storage it is a union CAS+source. host-native and
// CORNUS_REGISTRY_MIRROR are mutually exclusive.
func resolveRegistrySource(cfg config.Config) (registrySourcePlan, error) {
	src := os.Getenv("CORNUS_REGISTRY_SOURCE")
	backend := os.Getenv("CORNUS_DEPLOY_BACKEND")
	hasStorage := cfg.StorageURL != ""
	hasMirror := os.Getenv("CORNUS_REGISTRY_MIRROR") != ""

	wantHostNative := false
	switch src {
	case registrySourceOff:
		return registrySourcePlan{}, nil
	case registrySourceHostNative:
		wantHostNative = true
	case "":
		// Default: a zero-config host backend re-exports its local store. An
		// explicit --storage (the operator wants a real registry) or a configured
		// mirror opts out, as does a non-host backend.
		wantHostNative = isHostBackend(backend) && !hasStorage && !hasMirror
	default:
		return registrySourcePlan{}, fmt.Errorf("unknown CORNUS_REGISTRY_SOURCE %q (want \"host-native\" or \"off\")", src)
	}
	if !wantHostNative {
		return registrySourcePlan{}, nil
	}
	if hasMirror {
		return registrySourcePlan{}, fmt.Errorf("CORNUS_REGISTRY_SOURCE=host-native and CORNUS_REGISTRY_MIRROR are mutually exclusive")
	}
	source, err := hostNativeSourceFor(backend)
	if err != nil {
		return registrySourcePlan{}, err
	}
	return registrySourcePlan{source: source, pure: !hasStorage}, nil
}

// RegistryKeepsNoContentStore reports whether the resolved registry configuration
// keeps no content store (pure host-native re-export), so the caller (cmd/cornus
// serve) can skip opening a storage backend. It surfaces the same validation
// error server.New would return, so a misconfiguration fails fast.
func RegistryKeepsNoContentStore(cfg config.Config) (bool, error) {
	plan, err := resolveRegistrySource(cfg)
	return plan.pure, err
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	// Server self-description (registry host a client should tag/pull by). Auth-
	// exempt in authenticator.wrap so it can be discovered before credentials.
	s.mux.HandleFunc("/.cornus/v1/info", s.handleInfo)

	// Optional Prometheus scrape endpoint, registered only when the Prometheus
	// pull exporter was installed at startup (CORNUS_METRICS_PROMETHEUS + a
	// telemetry enable gate). When off there is no reader, no handler, and no
	// route — so it stays zero-cost. /metrics is exempt from auth (see
	// authenticator.wrap), mirroring /healthz and /readyz.
	if h := observability.PrometheusHandler(); h != nil {
		s.mux.Handle("/metrics", h)
	}

	// /v2/* — OCI distribution registry. CORNUS_REGISTRY_MIRROR (opt-in) turns a
	// local miss into a pull-through proxy to that upstream (e.g. "docker.io");
	// CORNUS_REGISTRY_MIRROR_CACHE (default true) persists fetched content.
	var regOpts []registry.Option
	if mirrorHost := os.Getenv("CORNUS_REGISTRY_MIRROR"); mirrorHost != "" {
		cache := true
		if v := os.Getenv("CORNUS_REGISTRY_MIRROR_CACHE"); v != "" {
			cache = parseBoolEnv(v)
		}
		regOpts = append(regOpts, registry.WithMirror(&registry.Mirror{Host: mirrorHost, Cache: cache}))
	}
	// The registry's content store: normally the CAS (s.store), or — under
	// host-native re-export — a local runtime's store. docker-daemon keeps a nil
	// store and re-exports read-only on a miss (WithDaemonSource; writes 405).
	// containerd backs the registry with the host containerd store directly, a full
	// read-write view (a push imports into it). An explicit --storage on a host
	// backend keeps the CAS (union / classic).
	var regStore registry.Store
	if s.store != nil {
		regStore = s.store
	}
	switch s.registrySource {
	case registrySourceDockerDaemon:
		regOpts = append(regOpts, registry.WithDaemonSource(s.daemonImageAPI, s.cfg.UploadsDir()))
	case registrySourceContainerd:
		if s.containerdStore != nil {
			regStore = s.containerdStore
		}
	}
	registry.New(regStore, regOpts...).Register(s.mux)

	// /.cornus/v1/* — build and deploy engines.
	s.mux.HandleFunc("/.cornus/v1/build", s.handleBuild)
	s.mux.HandleFunc("/.cornus/v1/build/attach", s.handleBuildAttach)
	s.mux.HandleFunc("/.cornus/v1/deploy", s.handleDeployCollection)
	// More specific than "/.cornus/v1/deploy/" so Go 1.22 mux precedence routes the
	// WebSocket attach here rather than treating "attach" as a deployment name.
	s.mux.HandleFunc("/.cornus/v1/deploy/attach", s.handleDeployAttach)
	// Unified pod-scoped caretaker connection: a pod's mount ('M'), hub control
	// ('C'), and hub data ('D') streams on one yamux mux, decoupled from any single
	// deploy-attach session.
	s.mux.HandleFunc("/.cornus/v1/caretaker/attach", s.handleCaretakerUnified)
	// The overlay's live service directory (discovery/status).
	s.mux.HandleFunc("/.cornus/v1/hub/catalog", s.handleHubCatalog)
	// Inter-replica delivery forwarding (multi-replica hub): a peer replica dials
	// here to hand off a relay for a delivery service THIS replica owns. Under /.cornus/v1/,
	// so the auth middleware requires a full credential (no caretaker-scoped token).
	s.mux.HandleFunc("/.cornus/v1/hub/forward", s.handleHubForward)
	// Inter-replica mount forwarding (multi-replica): a peer replica dials here to
	// hand off a caretaker mount stream for a deploy-attach session THIS replica
	// holds. Same trust model as /.cornus/v1/hub/forward — under /.cornus/v1/, so the auth
	// middleware requires a full credential (no caretaker-scoped token).
	s.mux.HandleFunc("/.cornus/v1/mount/forward", s.handleMountForward)
	// On-demand storage reclamation (registry CAS GC + localcache prune). A
	// destructive admin op gated on the API policy "gc" action.
	s.mux.HandleFunc("/.cornus/v1/gc", s.handleGC)
	// Exec lifecycle (inspect + WS exec-start). More specific than "/.cornus/v1/deploy/"
	// so it wins mux precedence over the {name} route.
	s.mux.HandleFunc("/.cornus/v1/deploy/exec/", s.handleExecItem)
	s.mux.HandleFunc("/.cornus/v1/deploy/", s.handleDeployItem)
	s.mux.HandleFunc("/.cornus/v1/volume/", s.handleVolumeItem)
}

// Handler returns the root http.Handler: the mux wrapped in the auth middleware,
// wrapped in turn in OpenTelemetry HTTP instrumentation (a span + the standard
// server metrics per request). Auth sits INSIDE otel so a rejected request (401)
// is still traced. Both wraps are no-op passthroughs when disabled — telemetry
// off, or no auth env configured — and both preserve http.Hijacker/Flusher so the
// /.cornus/v1/*/attach WebSocket and streaming endpoints keep working.
func (s *Server) Handler() http.Handler {
	return otelHandler(s.auth.wrap(s.registryAuthz(s.mux)))
}

// registryAuthz gates registry access under /v2/* on the caller identity via the
// API policy. It sits inside the auth middleware, so the identity (mTLS
// CommonName or JWT sub) is already on the request context. When no API policy
// is configured it is a pure pass-through (allow-all, zero cost).
//
// Writes (push / delete) always require the "push" action under a configured
// policy. Reads (pull) are OPT-IN: only when the policy explicitly mentions the
// "pull" action for some identity do GET/HEAD reads (except the /v2/ ping, which
// carries no image data and is how clients probe the API) require the caller to
// be allowed "pull". When no rule anywhere mentions "pull", reads stay governed
// by authentication exactly as before the action existed — so existing policies
// (including {"admin":["*"]}) cannot suddenly lock out pulls.
//
// Precedence versus CORNUS_REGISTRY_ANONYMOUS_PULL: the explicit pull policy
// wins. Anonymous pull only skips AUTHENTICATION; an anonymous caller has no
// identity and a configured policy denies the empty identity (fail closed), so
// mentioning "pull" in the policy shuts anonymous pulls out. New warns at
// startup when both are set.
func (s *Server) registryAuthz(next http.Handler) http.Handler {
	if s.apiPolicy == nil {
		return next
	}
	pullEnforced := s.apiPolicy.MentionsAction("pull")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		isRegistry := p == "/v2" || strings.HasPrefix(p, "/v2/")
		if isRegistry && isRegistryWrite(r.Method) && !s.apiPolicy.Allow(Identity(r), "push") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to push"})
			return
		}
		if pullEnforced && isRegistry && isRegistryRead(r.Method) && !isRegistryPing(p) && !s.apiPolicy.Allow(Identity(r), "pull") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: identity not permitted to pull"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isRegistryWrite reports whether an HTTP method mutates the registry (blob upload
// start/chunk/commit, cross-repo mount, manifest put, and blob/manifest delete).
func isRegistryWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// isRegistryRead reports whether an HTTP method is a registry read (pull:
// manifest/blob GET and existence-check HEAD).
func isRegistryRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// isRegistryPing reports whether path is the bare /v2/ API version check, which
// clients (docker login included) probe before any real operation. It carries
// no image data and is exempt from pull authz.
func isRegistryPing(path string) bool {
	return path == "/v2" || path == "/v2/"
}

// Mux exposes the underlying ServeMux so subsystems can register routes.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// Run starts the HTTP server and blocks until ctx is cancelled, then shuts
// down gracefully. When TLSCertFile and TLSKeyFile are both set it serves HTTPS,
// otherwise plaintext HTTP. On return (either an early serve error or a
// ctx-driven shutdown) it releases the lazily-built build engine and deploy
// backend so their on-disk locks (e.g. the BuildKit data-dir lock) do not leak.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.HTTPAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	defer s.closeResources()

	// Bind the listener up front so readiness reflects a real, accepting socket.
	ln, err := net.Listen("tcp", s.cfg.HTTPAddr)
	if err != nil {
		return err
	}
	tlsEnabled := s.TLSCertFile != "" && s.TLSKeyFile != ""

	// Build a reloading TLS config: the server cert (and, when a client-cert CA is
	// configured for mTLS, the CA pool) are served through GetCertificate /
	// GetConfigForClient callbacks that re-read the files on mtime change. So an
	// external rotator (cert-manager, Vault, ...) can renew the cert in place and it
	// takes effect without a cornus restart. A bad path is a hard startup error.
	// VerifyClientCertIfGiven (not RequireAndVerify) keeps /healthz, /readyz and
	// bearer-only clients working without a cert, while a presented cert must chain
	// to the CA — its CommonName then becomes the caller identity (authenticator.mtls).
	if tlsEnabled {
		cfg, err := tlsConfig(s.TLSCertFile, s.TLSKeyFile, s.TLSClientCAFile)
		if err != nil {
			return err
		}
		srv.TLSConfig = cfg
	}

	errCh := make(chan error, 1)
	go func() {
		var err error
		if tlsEnabled {
			// Cert/key are supplied via TLSConfig.GetCertificate, so pass empty paths.
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	// The listener is bound and we are about to serve: report ready, and start
	// the periodic storage-GC scheduler if CORNUS_GC_INTERVAL configured one (a
	// no-op otherwise). Its first run comes after one full interval — never at
	// startup — and closeResources (deferred above) stops it on the way out.
	s.ready.Store(true)
	s.startPeriodicGC()

	// Recover the deploy backend's workloads on startup for a backend that owns its
	// own process supervision (bare): after a host reboot its workloads must be
	// rebuilt (rootfs re-mounted, netns/CNI re-pinned) at startup, not lazily on the
	// first API request — otherwise a rebooted host with cornus running but idle
	// leaves the workloads down. Constructing the backend runs its reconcile pass
	// (barehost.New -> reconcile -> recoverInstance). Best-effort in a goroutine so a
	// slow or failed rebuild never blocks serving; getBackend caches only on success,
	// so the first real request retries a failure. Gated to bare because the other
	// backends either resurrect workloads via their own daemon (docker/containerd) or
	// have no such reboot concern (kubernetes), and eager construction there would do
	// unwanted work (e.g. cluster discovery) at every startup.
	if os.Getenv("CORNUS_DEPLOY_BACKEND") == "bare" {
		go func() {
			if _, err := s.getBackend(); err != nil {
				logging.FromContext(ctx).WarnContext(ctx, "startup deploy-backend reconcile skipped", "error", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		s.ready.Store(false)
		return err
	case <-ctx.Done():
		s.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// closeResources stops the server's supervised background loops (today: the
// periodic-GC loop) and releases the lazily-constructed build engine and
// deploy backend if they were built. Safe to call when neither exists
// (nil-guarded) and safe to call more than once: supCancel and sup.Wait are
// both idempotent (a context.CancelFunc is a no-op after the first call, and
// waiting on an already-drained child returns immediately).
func (s *Server) closeResources() {
	// Cancel every supervised child and wait for them to drain first, so no
	// sweep (or any future supervised loop) can race the resource teardown
	// below or start after shutdown. Then release the GC ticker's own timer
	// resource, now that nothing is reading from it.
	s.supCancel()
	s.sup.Wait()
	s.stopPeriodicGC()

	// Tear down any live public tunnels before the deploy backend they bridge to.
	if s.tunnels != nil {
		s.tunnels.closeAll()
	}

	s.engineMu.Lock()
	if s.engine != nil {
		_ = s.engine.Close()
		s.engine = nil
	}
	s.engineMu.Unlock()

	s.backendMu.Lock()
	if s.backend != nil {
		_ = s.backend.Close()
		s.backend = nil
	}
	s.backendMu.Unlock()

	// The distributed hub store (RedisStore) holds a heartbeat goroutine and a Redis
	// client to release; the in-memory Registry is not an io.Closer, so this no-ops
	// in the single-replica default.
	if c, ok := s.hub.(io.Closer); ok {
		_ = c.Close()
	}

	if s.fileCache != nil {
		_ = s.fileCache.Close()
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// handleInfo serves GET /.cornus/v1/info: the server's self-description, including the
// registry host a client should tag images with and bake into deploy pull refs.
// Auth-exempt so a client can discover it before presenting credentials.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	info := s.advertisedRegistry(r.Context())
	info.Ingress = s.advertisedIngress(r.Context())
	writeJSON(w, http.StatusOK, info)
}

// advertisedIngress resolves the ingress facts reported by /.cornus/v1/info: the
// deploy backend's introspection (deploy.IngressAdvertiser, implemented only by the
// kubernetes backend). Best-effort: any backend construction or introspection error,
// or a non-kubernetes backend, yields nil so the client falls back to emulation.
func (s *Server) advertisedIngress(ctx context.Context) *api.IngressInfo {
	// Only the kubernetes backend introspects ingress; avoid constructing a host
	// backend just to discover it does not (mirrors advertisedRegistry's selector).
	switch os.Getenv("CORNUS_DEPLOY_BACKEND") {
	case "kubernetes", "k8s":
	default:
		return nil
	}
	b, err := s.getBackend()
	if err != nil {
		return nil
	}
	adv, ok := b.(deploy.IngressAdvertiser)
	if !ok {
		return nil
	}
	info, err := adv.AdvertisedIngress(ctx)
	if err != nil {
		return nil
	}
	return info
}

// advertisedRegistry resolves the registry host/scheme reported by /.cornus/v1/info: the
// explicit CORNUS_ADVERTISE_REGISTRY override first, else the deploy backend's own
// introspection (deploy.RegistryAdvertiser, implemented by the kubernetes backend),
// else empty — in which case the client falls back to its endpoint host. Best-
// effort: any backend construction or introspection error yields an empty result
// rather than failing the request.
func (s *Server) advertisedRegistry(ctx context.Context) api.ServerInfo {
	if v := os.Getenv("CORNUS_ADVERTISE_REGISTRY"); v != "" {
		host, scheme := parseAdvertiseRegistry(v)
		if scheme == "" {
			scheme = s.registryScheme()
		}
		return api.ServerInfo{RegistryHost: host, RegistryScheme: scheme}
	}
	// Only the kubernetes backend introspects an advertised host; avoid constructing
	// a host backend (dockerhost/containerd) in the build hot path just to discover
	// it does not advertise. Mirrors defaultBackendFactory's selector.
	switch os.Getenv("CORNUS_DEPLOY_BACKEND") {
	case "kubernetes", "k8s":
	default:
		return api.ServerInfo{}
	}
	b, err := s.getBackend()
	if err != nil {
		return api.ServerInfo{}
	}
	adv, ok := b.(deploy.RegistryAdvertiser)
	if !ok {
		return api.ServerInfo{}
	}
	info, err := adv.AdvertisedRegistry(ctx)
	if err != nil {
		return api.ServerInfo{}
	}
	return info
}

// localPushTarget redirects a build's push destination to this server's own
// co-located registry over loopback when the target names the server's advertised
// registry — the address a cluster node pulls from but which the in-pod build
// engine cannot reach (e.g. a NodePort's localhost:<nodePort>, unbound inside the
// pod). The repository path and tag/digest are preserved, so the image the node
// later pulls by the advertised host resolves to the same content in the same
// registry. A target for any other registry (an external push) is returned
// unchanged, as is any target when push is false or no advertised host is known —
// so the single-node quick start, which advertises nothing, is unaffected.
func (s *Server) localPushTarget(ctx context.Context, target string, push bool) string {
	if !push || target == "" {
		return target
	}
	adv := s.advertisedRegistry(ctx).RegistryHost
	if adv == "" {
		return target
	}
	loop := s.loopbackRegistry()
	if loop == "" {
		return target
	}
	return redirectPushRef(target, adv, loop)
}

// localPushTargets redirects target and every entry of tags the same way
// localPushTarget does — same advertised-registry match, same rewrite to the
// co-located loopback registry — but resolves the advertised host and loopback
// address only once for the whole set. This matters for a compose build-group
// (several services deduplicated onto one BuildKit build, sharing one Target
// plus the other members' tags): redirecting each tag independently would call
// advertisedRegistry's live backend introspection once per tag instead of once
// per build, and — before this existed — the additional tags were not
// redirected at all, so only the group's primary Target ever reached the
// loopback registry while its sibling tags stayed pointed at the unreachable
// advertised host from inside the build pod. tags may be nil; the returned
// slice is nil too when tags is empty.
func (s *Server) localPushTargets(ctx context.Context, target string, tags []string, push bool) (string, []string) {
	if !push {
		return target, tags
	}
	adv := s.advertisedRegistry(ctx).RegistryHost
	if adv == "" {
		return target, tags
	}
	loop := s.loopbackRegistry()
	if loop == "" {
		return target, tags
	}
	if target != "" {
		target = redirectPushRef(target, adv, loop)
	}
	if len(tags) == 0 {
		return target, tags
	}
	redirected := make([]string, len(tags))
	for i, t := range tags {
		redirected[i] = redirectPushRef(t, adv, loop)
	}
	return target, redirected
}

// redirectPushRef rewrites ref to loop (the co-located loopback registry) when
// ref's registry host equals adv, preserving the repository path and tag/digest
// identifier. A ref for any other registry, or one that fails to parse, is
// returned unchanged.
func redirectPushRef(ref, adv, loop string) string {
	if ref == "" {
		return ref
	}
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil || parsed.Context().RegistryStr() != adv {
		return ref
	}
	sep := ":"
	if _, ok := parsed.(name.Digest); ok {
		sep = "@"
	}
	return loop + "/" + parsed.Context().RepositoryStr() + sep + parsed.Identifier()
}

// loopbackRegistry is this server's own registry address on the loopback interface,
// derived from its listen address (CORNUS_ADDR). The build engine treats loopback as
// plain HTTP, so a redirected push reaches the co-located registry without TLS.
func (s *Server) loopbackRegistry() string {
	_, port, err := net.SplitHostPort(s.cfg.HTTPAddr)
	if err != nil || port == "" {
		return ""
	}
	return "127.0.0.1:" + port
}

// registryScheme reports the scheme the server's own registry listener speaks:
// https when a TLS certificate is configured, else http.
func (s *Server) registryScheme() string {
	if s.TLSCertFile != "" && s.TLSKeyFile != "" {
		return "https"
	}
	return "http"
}

// parseAdvertiseRegistry splits an optional scheme off a CORNUS_ADVERTISE_REGISTRY
// value: "https://reg:5000" -> ("reg:5000", "https"); "reg:5000" -> ("reg:5000", "").
func parseAdvertiseRegistry(v string) (host, scheme string) {
	if rest, ok := strings.CutPrefix(v, "https://"); ok {
		return strings.TrimRight(rest, "/"), "https"
	}
	if rest, ok := strings.CutPrefix(v, "http://"); ok {
		return strings.TrimRight(rest, "/"), "http"
	}
	return strings.TrimRight(v, "/"), ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
