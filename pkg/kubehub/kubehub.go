// Package kubehub is a Kubernetes-native hub.Store: the multi-replica hub service
// registry for cornus's kubernetes backend, using the API server itself as the
// shared, watchable, lease-backed KV instead of Redis. It lives in its own package
// (not pkg/hub) so that pkg/hub — which the caretaker links — stays free
// of client-go.
//
// A KubeStore writes each provider a spoke registers as a namespaced HubEndpoint
// custom resource (a small CRD the store self-installs), so a dial-direct service
// registered on any replica is reachable from every replica. A DELIVERY service,
// however, holds a process-local *yamux.Session to the hosting spoke — only the
// owning replica can open the ingress stream — so a Lookup on a peer replica returns
// a ForwardAddr disposition instead and server.hubRelay forwards the relay to the
// owner (see /.cornus/v1/hub/forward), exactly like the RedisStore.
//
// Liveness is a native coordination.k8s.io Lease per replica, its RenewTime bumped
// by a heartbeat: Lookup and Catalog drop providers whose owner replica's Lease is
// missing or stale, the native analogue of the RedisStore alive-TTL. On top of that,
// each HubEndpoint CR carries an ownerReference to the replica's own Pod (downward
// API), so a hard crash still garbage-collects the CRs when the Pod is deleted.
//
// Index: the merged view is maintained push-based by a dynamic shared informer over
// the HubEndpoint GVR and a typed informer over the store's Leases; Lookup/Catalog
// read the in-memory index without a per-call LIST. A direct-LIST resync warms the
// caches at construction (and is what the unit tests exercise deterministically).
// This replica's OWN providers are additionally tracked synchronously (the disjoint
// partition it authoritatively owns), so a Lookup right after Register sees them
// without waiting on the informer.
//
// See .agents/docs/ARCHITECTURE.md ("Multi-replica design rationale", backend option D).
package kubehub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"cornus/pkg/hub"
)

const (
	// crdGroup/crdVersion/crdKind name the HubEndpoint CRD cornus self-installs.
	crdGroup   = "cornus.dev"
	crdVersion = "v1"
	crdKind    = "HubEndpoint"
	crdPlural  = "hubendpoints"

	// labelService marks a HubEndpoint CR (and is the informer's existence filter);
	// labelOwner records the sanitized replica id; leaseLabel marks our Leases;
	// peerKeyLabel and peerKeyAnnotation mark reserved public-key records.
	labelService      = "cornus.dev/hub-service"
	labelOwner        = "cornus.dev/hub-owner"
	leaseLabel        = "cornus.dev/hub-lease"
	peerKeyLabel      = "cornus.dev/hub-peer-key"
	peerKeyAnnotation = "cornus.dev/hub-peer-public-key"

	// leaseDuration is how long a replica is considered live past its last heartbeat;
	// heartbeatEvery must be comfortably shorter so a live replica never lapses.
	leaseDuration  = 15 * time.Second
	heartbeatEvery = 5 * time.Second

	// writeTimeout bounds ONE API-server write. Writes are serialized (wmu), so an
	// unbounded call during an API-server outage would stall every other spoke's
	// registration behind it.
	//
	// It is deliberately LONGER than pkg/hub's 3s (an API-server write goes through
	// admission and etcd; a Redis pipeline does not) and deliberately SHORTER than
	// 5s, which is what it used to be. The binding constraint is the invariant the
	// beatAttempts comment below states in words:
	//
	//	beatAttempts*writeTimeout + backoffSum  <  leaseDuration
	//
	// At 5s that is 15.3s against a 15s lease — one exhausted heartbeat tick could
	// outlive the very Lease it was renewing, which is precisely the "must not
	// consume a whole liveness window" the comment forbids. At 4s it is 12.3s.
	// TestHeartbeatRetryFitsInsideLivenessWindow enforces this, so the two constants
	// cannot be edited apart again.
	//
	// beatAttempts/beatBackoff are the bounded retry for one Lease renewal: the
	// heartbeat is the write whose silent failure has the most delayed symptom (every
	// provider this replica owns disappears from its peers leaseDuration later), so a
	// single conflict or blip must not consume a whole liveness window.
	beatAttempts = 3
	beatBackoff  = 100 * time.Millisecond
)

// writeTimeout is a var rather than a const only so a test can shrink it. Proving
// that a hung API server is ABANDONED rather than waited on forever means actually
// reaching the deadline, and doing that at 4s would put four seconds of sleep in
// the suite. Nothing outside a test writes it; see the const block above for the
// value's derivation and the invariant that binds it to leaseDuration.
var writeTimeout = 4 * time.Second

// hubEndpointGVR is the HubEndpoint custom resource; crdGVR is the CRD resource
// itself (cluster-scoped), used for self-install via the dynamic client so no
// apiextensions clientset dependency is needed.
var (
	hubEndpointGVR = schema.GroupVersionResource{Group: crdGroup, Version: crdVersion, Resource: crdPlural}
	crdGVR         = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

// provider is one registered endpoint, decoded from a HubEndpoint CR (or held for
// this replica's own partition). mode is "direct" (dial addr) or "deliver" (open an
// ingress stream to owner's spoke). owner is the registering replica; forwardAddr is
// that replica's inter-replica base URL a peer dials for a remote delivery.
type provider struct {
	objName     string
	connID      string
	service     string
	mode        string
	addr        string
	owner       string
	forwardAddr string
	protocol    string
}

// leaseInfo is the liveness window decoded from a replica's Lease.
type leaseInfo struct {
	renew    time.Time
	duration time.Duration
}

func (l leaseInfo) live(now time.Time) bool {
	return !l.renew.IsZero() && !l.renew.Add(l.duration).Before(now)
}

// KubeStore is the Kubernetes-native distributed hub.Store.
type KubeStore struct {
	dyn         dynamic.Interface
	cs          kubernetes.Interface
	namespace   string
	replicaID   string
	forwardAddr string
	// ownerRef, when non-nil, is this replica's Pod; every HubEndpoint CR (and the
	// Lease) carries it so a hard Pod delete garbage-collects them.
	ownerRef *metav1.OwnerReference

	// ctx governs the heartbeat and the CR writes; Close cancels it. stopCh stops the
	// informers.
	ctx    context.Context
	cancel context.CancelFunc
	stopCh chan struct{}

	// beatEvery is the heartbeat/reconcile period (heartbeatEvery; shortened by tests).
	beatEvery time.Duration

	// wmu serializes every API-server mutation — the inline CR write/delete of
	// Register/RegisterDeliver/RemoveConn and the reconcile pass — so ops on one
	// object hit the API server in the order they were enqueued. Without that total
	// order a retried write could land after the RemoveConn that superseded it and
	// resurrect a CR for a spoke that is gone. The read path (Lookup/Catalog, served
	// from the in-memory index) never takes it.
	wmu sync.Mutex

	mu sync.Mutex
	// own is this replica's authoritative partition: service -> objName -> provider,
	// visible to Lookup synchronously (before the informer observes the CR).
	own map[string]map[string]provider
	// owned maps a spoke connID to the objNames it registered, for RemoveConn.
	owned map[string]map[string]string
	// muxes holds this replica's own delivery sessions keyed by objName (the
	// process-local state a peer replica cannot see and must forward to reach).
	muxes map[string]*yamux.Session
	// index is the informer-maintained merged view of ALL replicas' providers:
	// service -> objName -> provider. Lookup reads peers from here.
	index map[string]map[string]provider
	// leases is the informer-maintained liveness view: replicaID -> window.
	leases map[string]leaseInfo
	// rr is the per-name round-robin cursor across live providers.
	rr map[string]int
	// pending is the reconcile queue: objName -> the DESIRED cluster state whose
	// write has not landed yet. Every mutation enqueues; a successful write clears,
	// so it is empty in the healthy case. An API-server outage leaves it populated
	// and the heartbeat loop re-drives it until it drains — which is what makes a
	// failed Register recoverable (the own partition stays authoritative locally and
	// is re-asserted) instead of silently lost.
	pending map[string]pendingOp
	// seq numbers pending ops so a write superseded while in flight clears nothing.
	seq uint64
	// beatErr is the last Lease renewal outcome (nil = the Lease is fresh).
	beatErr error
}

// pendingOp is the desired cluster state for one HubEndpoint object: write rec, or
// (del) delete it.
type pendingOp struct {
	rec provider
	del bool
	seq uint64
}

// NewFromEnv builds a KubeStore from in-cluster config (falling back to the local
// kubeconfig, the same path the kubernetes deploy backend uses). namespace is the
// deploy namespace; replicaID names this replica's partition and Lease; forwardAddr
// is the base URL peers dial to forward a delivery to it. POD_NAME/POD_NAMESPACE
// (downward API), when set, provide the Pod ownerReference for crash GC. Any
// construction failure (no cluster, CRD install) is a hard startup error.
func NewFromEnv(ctx context.Context, namespace, replicaID, forwardAddr string) (*KubeStore, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("kubehub: load config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubehub: clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubehub: dynamic client: %w", err)
	}
	return New(ctx, dyn, cs, namespace, replicaID, forwardAddr, os.Getenv("POD_NAME"), os.Getenv("POD_NAMESPACE"))
}

// New builds a KubeStore over explicit clients. It self-installs the CRD, resolves
// the Pod ownerReference (best-effort), warms the index with a LIST, starts the
// informers and the heartbeat, and writes the first Lease. A CRD install failure is
// returned as a hard error.
func New(ctx context.Context, dyn dynamic.Interface, cs kubernetes.Interface, namespace, replicaID, forwardAddr, podName, podNamespace string) (*KubeStore, error) {
	s := newStore(dyn, cs, namespace, replicaID, forwardAddr)
	if err := s.ensureCRD(ctx); err != nil {
		return nil, err
	}
	s.ownerRef = s.resolveOwnerRef(ctx, podName, podNamespace)
	if err := s.beat(s.ctx); err != nil {
		return nil, fmt.Errorf("kubehub: initial lease: %w", err)
	}
	s.resync(s.ctx)
	s.startInformers()
	go s.heartbeat()
	return s, nil
}

// newStore builds the in-memory shell (no cluster I/O), shared by New and tests.
func newStore(dyn dynamic.Interface, cs kubernetes.Interface, namespace, replicaID, forwardAddr string) *KubeStore {
	if namespace == "" {
		namespace = "default"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &KubeStore{
		dyn:         dyn,
		cs:          cs,
		namespace:   namespace,
		replicaID:   replicaID,
		forwardAddr: forwardAddr,
		ctx:         ctx,
		cancel:      cancel,
		stopCh:      make(chan struct{}),
		beatEvery:   heartbeatEvery,
		own:         map[string]map[string]provider{},
		owned:       map[string]map[string]string{},
		muxes:       map[string]*yamux.Session{},
		index:       map[string]map[string]provider{},
		leases:      map[string]leaseInfo{},
		rr:          map[string]int{},
		pending:     map[string]pendingOp{},
	}
}

// logger is resolved per call so a logging.Init (or a test's slog.SetDefault) that
// runs after construction still takes effect.
func (s *KubeStore) logger() *slog.Logger {
	return slog.Default().With("component", "hub", "store", "kube", "replica", s.replicaID)
}

func loadConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

// endpointName is the DNS-safe, deterministic object name for a provider: a hash of
// replicaID:connID:service. Providers are disjoint per replica, so the name is
// unique and re-registration idempotently targets the same object (no contention).
func endpointName(replicaID, connID, service string) string {
	sum := sha256.Sum256([]byte(replicaID + "\x00" + connID + "\x00" + service))
	return "he-" + hex.EncodeToString(sum[:])[:32]
}

// leaseName is the DNS-safe Lease name for a replica.
func leaseName(replicaID string) string {
	sum := sha256.Sum256([]byte(replicaID))
	return "cornus-hub-" + hex.EncodeToString(sum[:])[:16]
}

// peerKeyName is the deterministic HubEndpoint name reserved for one replica's
// public verification key. It is distinct from provider endpoint names.
func peerKeyName(replicaID string) string {
	sum := sha256.Sum256([]byte(replicaID))
	return "hk-" + hex.EncodeToString(sum[:])[:32]
}

// sanitizeLabel makes s a valid label value (<=63 chars, alnum-bounded), for the
// existence-filtered informer labels. The real value always lives in the CR spec.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	v := strings.Trim(b.String(), "-_.")
	if v == "" {
		v = "x"
	}
	if len(v) > 63 {
		v = strings.Trim(v[:63], "-_.")
	}
	return v
}

// --- hub.Store ---------------------------------------------------------------

// Register adds a dial-direct provider (any replica can dial addr) for name. The
// error reports that the provider is not visible to peer replicas YET; it stays in
// the reconcile queue and the heartbeat re-drives it.
func (s *KubeStore) Register(connID, name, addr, protocol string) error {
	return s.put(connID, provider{
		objName:     endpointName(s.replicaID, connID, name),
		connID:      connID,
		service:     name,
		mode:        "direct",
		addr:        addr,
		owner:       s.replicaID,
		forwardAddr: s.forwardAddr,
		protocol:    protocol,
	}, nil)
}

// RegisterDeliver adds a delivery provider for name and keeps the spoke's mux
// process-local (only this replica can open the ingress stream; peers forward). See
// Register for the error's meaning.
func (s *KubeStore) RegisterDeliver(connID, name string, mux *yamux.Session) error {
	return s.put(connID, provider{
		objName:     endpointName(s.replicaID, connID, name),
		connID:      connID,
		service:     name,
		mode:        "deliver",
		owner:       s.replicaID,
		forwardAddr: s.forwardAddr,
	}, mux)
}

// put records a provider in this replica's own partition (authoritative locally, and
// what the reconcile loop replays from) and writes its CR.
//
// The CR write was previously documented as "fire-and-forget", but it was never
// asynchronous — it has always been a synchronous API-server call on this path, and
// only its ERROR was discarded. So the write keeps its shape (no goroutine, no new
// blocking) and merely reports its outcome; the local record survives a failure,
// which is what lets the reconcile loop re-assert it.
func (s *KubeStore) put(connID string, rec provider, mux *yamux.Session) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	if s.own[rec.service] == nil {
		s.own[rec.service] = map[string]provider{}
	}
	s.own[rec.service][rec.objName] = rec
	if s.owned[connID] == nil {
		s.owned[connID] = map[string]string{}
	}
	s.owned[connID][rec.objName] = rec.service
	if mux != nil {
		s.muxes[rec.objName] = mux
	}
	op := s.enqueueLocked(pendingOp{rec: rec})
	s.mu.Unlock()
	if err := s.applyOpLocked(op); err != nil {
		return fmt.Errorf("kubehub: register service %q in the shared registry (queued for retry): %w", rec.service, err)
	}
	return nil
}

// enqueueLocked records op as the desired cluster state for its object and stamps it
// with the next sequence number. s.mu must be held; the caller must hold wmu too, so
// enqueue order and apply order agree.
func (s *KubeStore) enqueueLocked(op pendingOp) pendingOp {
	s.seq++
	op.seq = s.seq
	s.pending[op.rec.objName] = op
	return op
}

// applyOpLocked performs one pending op against the API server and clears it from the
// queue if it succeeded and was not superseded in the meantime. wmu must be held.
func (s *KubeStore) applyOpLocked(op pendingOp) error {
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	var err error
	if op.del {
		err = s.deleteCR(ctx, op.rec.objName)
	} else {
		err = s.writeCR(ctx, op.rec)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	if cur, ok := s.pending[op.rec.objName]; ok && cur.seq == op.seq {
		delete(s.pending, op.rec.objName)
	}
	s.mu.Unlock()
	return nil
}

// reconcileOne re-drives the pending op for objName, if one is still queued. Reading
// the queue under wmu (not just mu) is what keeps a retry from overtaking a mutation
// enqueued after it.
func (s *KubeStore) reconcileOne(objName string) (applied bool, err error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	op, ok := s.pending[objName]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := s.applyOpLocked(op); err != nil {
		return false, err
	}
	return true, nil
}

// writeCR creates or updates the HubEndpoint CR for a provider.
func (s *KubeStore) writeCR(ctx context.Context, rec provider) error {
	obj := s.endpointObject(rec)
	res := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace)
	_, err := res.Create(ctx, obj, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create HubEndpoint %s: %w", rec.objName, err)
	}
	// Already present (re-registration): overwrite the spec at the current version.
	existing, err := res.Get(ctx, rec.objName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get HubEndpoint %s: %w", rec.objName, err)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	if _, err := res.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update HubEndpoint %s: %w", rec.objName, err)
	}
	return nil
}

// deleteCR removes a provider's HubEndpoint CR. An already-absent object is success:
// the desired state (no such provider) holds.
func (s *KubeStore) deleteCR(ctx context.Context, objName string) error {
	err := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace).Delete(ctx, objName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete HubEndpoint %s: %w", objName, err)
	}
	return nil
}

func (s *KubeStore) endpointObject(rec provider) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": crdGroup + "/" + crdVersion,
		"kind":       crdKind,
		"metadata": map[string]any{
			"name": rec.objName,
			"labels": map[string]any{
				labelService: sanitizeLabel(rec.service),
				labelOwner:   sanitizeLabel(rec.owner),
			},
		},
		"spec": map[string]any{
			"service":     rec.service,
			"mode":        rec.mode,
			"addr":        rec.addr,
			"owner":       rec.owner,
			"forwardAddr": rec.forwardAddr,
			"protocol":    rec.protocol,
			"connID":      rec.connID,
		},
	}}
	if s.ownerRef != nil {
		u.SetOwnerReferences([]metav1.OwnerReference{*s.ownerRef})
	}
	return u
}

// Lookup returns one live provider for name, round-robin across the merged view of
// this replica's own partition plus peers' providers whose owner Lease is live. A
// remote delivery resolves to a ForwardAddr/ForwardName disposition.
func (s *KubeStore) Lookup(name string) (hub.Target, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.liveProvidersLocked(name, now)
	if len(live) == 0 {
		return hub.Target{}, false
	}
	i := s.rr[name] % len(live)
	s.rr[name] = i + 1
	rec := live[i]
	switch rec.mode {
	case "direct":
		return hub.Target{Addr: rec.addr, Protocol: rec.protocol}, true
	case "deliver":
		if rec.owner == s.replicaID {
			mux := s.muxes[rec.objName]
			if mux == nil {
				return hub.Target{}, false
			}
			return hub.Target{Mux: mux}, true
		}
		// Remote delivery: the owner replica holds the spoke session; forward to it.
		return hub.Target{ForwardAddr: rec.forwardAddr, ForwardName: name}, true
	}
	return hub.Target{}, false
}

// liveProvidersLocked returns the live providers for name in a stable order (sorted
// objName): this replica's own partition (always live) plus peers whose owner Lease
// is live. s.mu must be held.
func (s *KubeStore) liveProvidersLocked(name string, now time.Time) []provider {
	seen := map[string]provider{}
	for objName, rec := range s.own[name] {
		seen[objName] = rec
	}
	for objName, rec := range s.index[name] {
		if rec.owner == s.replicaID {
			continue // our own partition is authoritative (already in seen)
		}
		if l, ok := s.leases[rec.owner]; ok && l.live(now) {
			seen[objName] = rec
		}
	}
	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for objName := range seen {
		names = append(names, objName)
	}
	sort.Strings(names)
	out := make([]provider, 0, len(names))
	for _, objName := range names {
		out = append(out, seen[objName])
	}
	return out
}

// Catalog returns the sorted service names that currently have at least one live
// provider anywhere in the cluster.
func (s *KubeStore) Catalog() []string {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]struct{}{}
	for name := range s.own {
		if len(s.own[name]) > 0 {
			set[name] = struct{}{}
		}
	}
	for name := range s.index {
		if len(s.liveProvidersLocked(name, now)) > 0 {
			set[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RemoveConn drops every provider this replica registered under connID (deleting the
// CRs) and forgets its local delivery muxes. Called when the spoke's hub connection
// drops.
//
// A failed delete is NOT harmless and is not simply dropped: this replica keeps
// renewing its Lease, so the stale CR stays "live" in every peer's merged view and
// peers keep routing to a spoke that is gone (unlike a Pod crash, where the
// ownerReference GC and the lapsed Lease retire the whole partition). The deletion is
// therefore queued like a write and re-driven by the heartbeat; the error tells the
// caller the withdrawal has not landed yet.
func (s *KubeStore) RemoveConn(connID string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	objs := s.owned[connID]
	delete(s.owned, connID)
	ops := make([]pendingOp, 0, len(objs))
	for objName, service := range objs {
		delete(s.muxes, objName)
		if m := s.own[service]; m != nil {
			delete(m, objName)
			if len(m) == 0 {
				delete(s.own, service)
			}
		}
		ops = append(ops, s.enqueueLocked(pendingOp{rec: provider{objName: objName, service: service}, del: true}))
	}
	s.mu.Unlock()
	var errs []error
	for _, op := range ops {
		if err := s.applyOpLocked(op); err != nil {
			errs = append(errs, fmt.Errorf("kubehub: withdraw service %q from the shared registry (queued for retry): %w", op.rec.service, err))
		}
	}
	return errors.Join(errs...)
}

// PublishPeerKey writes this replica's public verification key as a reserved
// HubEndpoint object. The object deliberately has no labelService, so provider
// informers and Catalog ignore it. Owning it by the replica Lease gives it the
// same hard-delete garbage collection and liveness boundary as routing records.
func (s *KubeStore) PublishPeerKey(replicaID string, publicKeyPEM []byte) error {
	if replicaID == "" || replicaID != s.replicaID {
		return fmt.Errorf("kubehub: cannot publish peer key for replica %q from replica %q", replicaID, s.replicaID)
	}
	if len(publicKeyPEM) == 0 {
		return errors.New("kubehub: cannot publish an empty peer key")
	}
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	lease, err := s.cs.CoordinationV1().Leases(s.namespace).Get(ctx, leaseName(replicaID), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("kubehub: get owner Lease for peer key %q: %w", replicaID, err)
	}
	controller := false
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": crdGroup + "/" + crdVersion,
		"kind":       crdKind,
		"metadata": map[string]any{
			"name": peerKeyName(replicaID),
			"labels": map[string]any{
				peerKeyLabel: "true",
				labelOwner:   sanitizeLabel(replicaID),
			},
			"annotations": map[string]any{peerKeyAnnotation: string(publicKeyPEM)},
		},
		"spec": map[string]any{
			"mode":  "peer-key",
			"owner": replicaID,
		},
	}}
	obj.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: coordinationv1.SchemeGroupVersion.String(),
		Kind:       "Lease",
		Name:       lease.Name,
		UID:        lease.UID,
		Controller: &controller,
	}})
	res := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace)
	if _, err := res.Create(ctx, obj, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("kubehub: create peer key %q: %w", replicaID, err)
	}
	existing, err := res.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("kubehub: get peer key %q for update: %w", replicaID, err)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	if _, err := res.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("kubehub: update peer key %q: %w", replicaID, err)
	}
	return nil
}

// PeerKey returns a public key only while its replica Lease is present and live.
// The Lease check fails closed before asynchronous garbage collection removes a
// departed replica's reserved HubEndpoint object.
func (s *KubeStore) PeerKey(replicaID string) ([]byte, bool, error) {
	if replicaID == "" {
		return nil, false, errors.New("kubehub: empty peer replica id")
	}
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	lease, err := s.cs.CoordinationV1().Leases(s.namespace).Get(ctx, leaseName(replicaID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("kubehub: get peer Lease %q: %w", replicaID, err)
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != replicaID || !leaseInfoOf(lease).live(time.Now()) {
		return nil, false, nil
	}
	obj, err := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace).Get(ctx, peerKeyName(replicaID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("kubehub: get peer key %q: %w", replicaID, err)
	}
	owner, _, _ := unstructured.NestedString(obj.Object, "spec", "owner")
	key := obj.GetAnnotations()[peerKeyAnnotation]
	if owner != replicaID || key == "" {
		return nil, false, fmt.Errorf("kubehub: malformed peer key record for %q", replicaID)
	}
	return []byte(key), true, nil
}

// Close stops the heartbeat and informers, best-effort deletes this replica's CRs
// and Lease, and is safe to call once.
//
// The cleanup deletes stay deliberately best-effort (errors dropped, nothing queued):
// deleting the Lease is what actually retires this replica's whole partition from
// every peer's view, and if even that fails the Lease goes stale leaseDuration later
// (and the Pod ownerReference GCs the CRs). There is no later tick to retry in, and
// nothing a caller could do with the error.
func (s *KubeStore) Close() error {
	s.cancel()
	close(s.stopCh)

	s.mu.Lock()
	s.pending = map[string]pendingOp{}
	var objNames []string
	for _, m := range s.owned {
		for objName := range m {
			objNames = append(objNames, objName)
		}
	}
	s.own = map[string]map[string]provider{}
	s.owned = map[string]map[string]string{}
	s.muxes = map[string]*yamux.Session{}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace)
	for _, objName := range objNames {
		_ = res.Delete(ctx, objName, metav1.DeleteOptions{})
	}
	_ = res.Delete(ctx, peerKeyName(s.replicaID), metav1.DeleteOptions{})
	_ = s.cs.CoordinationV1().Leases(s.namespace).Delete(ctx, leaseName(s.replicaID), metav1.DeleteOptions{})
	return nil
}

// --- liveness ----------------------------------------------------------------

// heartbeat renews this replica's Lease and re-drives failed CR writes until Close
// cancels s.ctx.
func (s *KubeStore) heartbeat() {
	t := time.NewTicker(s.beatEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick is one heartbeat period: renew the Lease (bounded retry, reporting a change of
// state), then reconcile any CR write that has not landed. The renewal runs FIRST and
// the reconcile is deadline-bounded, so a large backlog can never starve the Lease
// every one of those providers depends on.
func (s *KubeStore) tick() {
	s.noteBeat(s.beatBounded(s.ctx))
	s.reconcile(time.Now().Add(s.beatEvery / 2))
}

// beatBounded retries one Lease renewal a bounded number of times before giving up
// for this tick. leaseDuration is three tick periods, so a tick that burns a little
// on retries still leaves the Lease comfortably fresh. A retry also resolves the
// common transient failure here — a resourceVersion conflict on the Update.
func (s *KubeStore) beatBounded(ctx context.Context) error {
	var err error
	for i := 0; i < beatAttempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(i) * beatBackoff):
			}
		}
		if err = s.beat(ctx); err == nil {
			return nil
		}
	}
	return err
}

// noteBeat records the Lease renewal outcome and logs the TRANSITIONS only (failing,
// recovered) rather than every tick: an API-server outage would otherwise produce a
// log line every heartbeat for its whole duration, and the persisting condition is
// already readable from StoreHealth (which the server exposes as a gauge and on
// /readyz). The failure is logged at ERROR because its consequence is not local: once
// the Lease goes stale, every provider this replica owns vanishes from every peer,
// with no other event to explain it.
func (s *KubeStore) noteBeat(err error) {
	s.mu.Lock()
	prev := s.beatErr
	s.beatErr = err
	s.mu.Unlock()
	switch {
	case err != nil && prev == nil:
		s.logger().Error("hub: Lease renewal failed; this replica's registered services disappear from peer replicas once the Lease goes stale",
			"lease", leaseName(s.replicaID), "duration", leaseDuration, "error", err)
	case err == nil && prev != nil:
		s.logger().Info("hub: Lease renewal recovered; this replica is visible to peer replicas again")
	}
}

// reconcile re-drives every pending op (a CR write whose API call failed) until the
// queue drains or the deadline passes; whatever is left is retried next tick. It is
// the recovery half of the contract: a Register that failed during an API-server
// outage becomes visible cluster-wide again on its own, without the spoke
// re-registering.
func (s *KubeStore) reconcile(deadline time.Time) {
	s.mu.Lock()
	objNames := make([]string, 0, len(s.pending))
	for objName := range s.pending {
		objNames = append(objNames, objName)
	}
	s.mu.Unlock()
	if len(objNames) == 0 {
		return
	}
	sort.Strings(objNames)
	var healed, failed int
	for _, objName := range objNames {
		if time.Now().After(deadline) {
			break
		}
		applied, err := s.reconcileOne(objName)
		switch {
		case err != nil:
			failed++
		case applied:
			healed++
		}
	}
	if healed > 0 {
		s.logger().Info("hub: re-applied hub registry writes that had failed; they are visible to peer replicas again",
			"reapplied", healed, "still.pending", failed)
	}
}

// StoreHealth reports whether this replica's shared state is trustworthy: nil when
// the Lease is fresh and every CR write has landed. See hub.HealthReporter.
func (s *KubeStore) StoreHealth() error {
	s.mu.Lock()
	beatErr, pending := s.beatErr, len(s.pending)
	s.mu.Unlock()
	if beatErr != nil {
		return fmt.Errorf("kubehub: Lease renewal failing (peers drop this replica's services %s after the last good renewal): %w", leaseDuration, beatErr)
	}
	if pending > 0 {
		return fmt.Errorf("kubehub: %d hub registry write(s) not yet visible to peer replicas", pending)
	}
	return nil
}

// beat creates or updates this replica's Lease with a fresh RenewTime.
//
// The writeTimeout bound is load-bearing and was missing. beatBounded is called
// with the STORE's lifetime context, and no Timeout is set on the rest.Config
// (loadConfig returns it untouched), so an API server that accepts the connection
// and then stops responding hung this call forever: beatBounded never returned,
// noteBeat never recorded a failure, and StoreHealth kept reporting healthy while
// the Lease quietly expired and every provider this replica owns vanished from
// every peer. RedisStore.beat has always bounded itself the same way.
func (s *KubeStore) beat(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	leases := s.cs.CoordinationV1().Leases(s.namespace)
	now := metav1.NewMicroTime(time.Now())
	dur := int32(leaseDuration / time.Second)
	name := leaseName(s.replicaID)
	holder := s.replicaID
	existing, err := leases.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		lease := &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{leaseLabel: "true"},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &holder,
				LeaseDurationSeconds: &dur,
				RenewTime:            &now,
			},
		}
		if s.ownerRef != nil {
			lease.OwnerReferences = []metav1.OwnerReference{*s.ownerRef}
		}
		_, err = leases.Create(ctx, lease, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Spec.HolderIdentity = &holder
	existing.Spec.LeaseDurationSeconds = &dur
	existing.Spec.RenewTime = &now
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[leaseLabel] = "true"
	_, err = leases.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// resolveOwnerRef looks up this replica's own Pod so HubEndpoint CRs (and the Lease)
// carry it as an owner: a hard Pod delete then GCs them. Best-effort — nil when
// POD_NAME is unset or the Pod cannot be read.
func (s *KubeStore) resolveOwnerRef(ctx context.Context, podName, podNamespace string) *metav1.OwnerReference {
	if podName == "" {
		return nil
	}
	ns := podNamespace
	if ns == "" {
		ns = s.namespace
	}
	pod, err := s.cs.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	controller := false
	return &metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		UID:        pod.UID,
		Controller: &controller,
	}
}

// --- index (informers + resync) ---------------------------------------------

// startInformers wires the push-based index: a dynamic HubEndpoint informer and a
// typed Lease informer, both filtered to cornus's objects, updating s.index and
// s.leases on every event.
func (s *KubeStore) startInformers() {
	epFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(s.dyn, 0, s.namespace, func(o *metav1.ListOptions) {
		o.LabelSelector = labelService // existence filter
	})
	epInf := epFactory.ForResource(hubEndpointGVR).Informer()
	epInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { s.onEndpoint(obj) },
		UpdateFunc: func(_, obj any) { s.onEndpoint(obj) },
		DeleteFunc: func(obj any) { s.onEndpointDelete(obj) },
	})
	epFactory.Start(s.stopCh)

	leaseFactory := informers.NewSharedInformerFactoryWithOptions(s.cs, 0,
		informers.WithNamespace(s.namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = leaseLabel + "=true" }),
	)
	leaseInf := leaseFactory.Coordination().V1().Leases().Informer()
	leaseInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { s.onLease(obj) },
		UpdateFunc: func(_, obj any) { s.onLease(obj) },
		DeleteFunc: func(obj any) { s.onLeaseDelete(obj) },
	})
	leaseFactory.Start(s.stopCh)
}

func (s *KubeStore) onEndpoint(obj any) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	rec := parseProvider(u)
	s.mu.Lock()
	if s.index[rec.service] == nil {
		s.index[rec.service] = map[string]provider{}
	}
	s.index[rec.service][rec.objName] = rec
	s.mu.Unlock()
}

func (s *KubeStore) onEndpointDelete(obj any) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}
	rec := parseProvider(u)
	s.mu.Lock()
	if m := s.index[rec.service]; m != nil {
		delete(m, rec.objName)
		if len(m) == 0 {
			delete(s.index, rec.service)
		}
	}
	s.mu.Unlock()
}

func (s *KubeStore) onLease(obj any) {
	l, ok := toLease(obj)
	if !ok || l.Spec.HolderIdentity == nil {
		return
	}
	s.mu.Lock()
	s.leases[*l.Spec.HolderIdentity] = leaseInfoOf(l)
	s.mu.Unlock()
}

func (s *KubeStore) onLeaseDelete(obj any) {
	l, ok := toLease(obj)
	if !ok || l.Spec.HolderIdentity == nil {
		return
	}
	s.mu.Lock()
	delete(s.leases, *l.Spec.HolderIdentity)
	s.mu.Unlock()
}

// resync rebuilds the index and lease caches from a direct LIST. It warms the caches
// at construction and is the deterministic path the unit tests drive.
func (s *KubeStore) resync(ctx context.Context) {
	index := map[string]map[string]provider{}
	if list, err := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelService}); err == nil {
		for i := range list.Items {
			rec := parseProvider(&list.Items[i])
			if index[rec.service] == nil {
				index[rec.service] = map[string]provider{}
			}
			index[rec.service][rec.objName] = rec
		}
	}
	leases := map[string]leaseInfo{}
	if list, err := s.cs.CoordinationV1().Leases(s.namespace).List(ctx, metav1.ListOptions{LabelSelector: leaseLabel + "=true"}); err == nil {
		for i := range list.Items {
			l := &list.Items[i]
			if l.Spec.HolderIdentity != nil {
				leases[*l.Spec.HolderIdentity] = leaseInfoOf(l)
			}
		}
	}
	s.mu.Lock()
	s.index = index
	s.leases = leases
	s.mu.Unlock()
}

func leaseInfoOf(l *coordinationv1.Lease) leaseInfo {
	var info leaseInfo
	if l.Spec.RenewTime != nil {
		info.renew = l.Spec.RenewTime.Time
	}
	if l.Spec.LeaseDurationSeconds != nil {
		info.duration = time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second
	}
	return info
}

func parseProvider(u *unstructured.Unstructured) provider {
	get := func(k string) string {
		v, _, _ := unstructured.NestedString(u.Object, "spec", k)
		return v
	}
	return provider{
		objName:     u.GetName(),
		connID:      get("connID"),
		service:     get("service"),
		mode:        get("mode"),
		addr:        get("addr"),
		owner:       get("owner"),
		forwardAddr: get("forwardAddr"),
		protocol:    get("protocol"),
	}
}

func toUnstructured(obj any) (*unstructured.Unstructured, bool) {
	switch v := obj.(type) {
	case *unstructured.Unstructured:
		return v, true
	case cache.DeletedFinalStateUnknown:
		u, ok := v.Obj.(*unstructured.Unstructured)
		return u, ok
	}
	return nil, false
}

func toLease(obj any) (*coordinationv1.Lease, bool) {
	switch v := obj.(type) {
	case *coordinationv1.Lease:
		return v, true
	case cache.DeletedFinalStateUnknown:
		l, ok := v.Obj.(*coordinationv1.Lease)
		return l, ok
	}
	return nil, false
}

// --- CRD self-install --------------------------------------------------------

// ensureCRD idempotently creates the HubEndpoint CRD and waits until it is
// Established (bounded). It uses the dynamic client against the CRD resource, so no
// apiextensions clientset dependency is needed.
func (s *KubeStore) ensureCRD(ctx context.Context) error {
	res := s.dyn.Resource(crdGVR)
	if _, err := res.Create(ctx, crdObject(), metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("kubehub: create CRD: %w", err)
	}
	// Wait for the Established condition (bounded). A fake dynamic client never sets
	// status conditions, so a missing status short-circuits after the object exists.
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := res.Get(ctx, crdPlural+"."+crdGroup, metav1.GetOptions{})
		if err == nil && crdEstablished(got) {
			return nil
		}
		if time.Now().After(deadline) {
			return nil // best-effort: the resource exists; proceed rather than block startup forever
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func crdEstablished(u *unstructured.Unstructured) bool {
	conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "Established" && m["status"] == "True" {
			return true
		}
	}
	return false
}

// crdObject builds the HubEndpoint CustomResourceDefinition (a structural schema).
func crdObject() *unstructured.Unstructured {
	strProps := map[string]any{"type": "string"}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": crdPlural + "." + crdGroup},
		"spec": map[string]any{
			"group": crdGroup,
			"scope": "Namespaced",
			"names": map[string]any{
				"plural":   crdPlural,
				"singular": "hubendpoint",
				"kind":     crdKind,
				"listKind": crdKind + "List",
			},
			"versions": []any{map[string]any{
				"name":    crdVersion,
				"served":  true,
				"storage": true,
				"schema": map[string]any{
					"openAPIV3Schema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"spec": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"service":     strProps,
									"mode":        strProps,
									"addr":        strProps,
									"owner":       strProps,
									"forwardAddr": strProps,
									"protocol":    strProps,
									"connID":      strProps,
								},
							},
						},
					},
				},
			}},
		},
	}}
}
