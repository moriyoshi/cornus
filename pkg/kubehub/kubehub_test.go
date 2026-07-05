package kubehub

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

// newTestStore builds a KubeStore over fake clients WITHOUT starting the informers
// or heartbeat, so tests drive the index deterministically via resync. It exercises
// the same index/liveness logic the informers feed in production.
func newTestStore(t *testing.T, replicaID string) *KubeStore {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		hubEndpointGVR: "HubEndpointList",
	})
	cs := fake.NewSimpleClientset()
	return newStore(dyn, cs, "default", replicaID, "ws://self:5000")
}

// writePeerEndpoint creates a HubEndpoint CR owned by another replica directly in the
// fake dynamic client (as if that peer had registered it).
func writePeerEndpoint(t *testing.T, s *KubeStore, owner, connID, service, mode, addr, forwardAddr string) {
	t.Helper()
	rec := provider{
		objName:     endpointName(owner, connID, service),
		connID:      connID,
		service:     service,
		mode:        mode,
		addr:        addr,
		owner:       owner,
		forwardAddr: forwardAddr,
	}
	peer := &KubeStore{replicaID: owner} // only used so endpointObject omits an ownerRef
	if _, err := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace).Create(context.Background(), peer.endpointObject(rec), metav1.CreateOptions{}); err != nil {
		t.Fatalf("write peer endpoint: %v", err)
	}
}

// writeLease creates a labelled Lease for a replica with the given RenewTime.
func writeLease(t *testing.T, s *KubeStore, replicaID string, renew time.Time) {
	t.Helper()
	dur := int32(leaseDuration / time.Second)
	rt := metav1.NewMicroTime(renew)
	holder := replicaID
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      leaseName(replicaID),
			Namespace: s.namespace,
			Labels:    map[string]string{leaseLabel: "true"},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &dur,
			RenewTime:            &rt,
		},
	}
	if _, err := s.cs.CoordinationV1().Leases(s.namespace).Create(context.Background(), lease, metav1.CreateOptions{}); err != nil {
		t.Fatalf("write lease: %v", err)
	}
}

func getEndpoint(t *testing.T, s *KubeStore, owner, connID, service string) (*unstructured.Unstructured, error) {
	t.Helper()
	return s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace).Get(context.Background(), endpointName(owner, connID, service), metav1.GetOptions{})
}

func TestRegisterWritesCRAndLookupDialDirect(t *testing.T) {
	s := newTestStore(t, "replica-A")
	s.Register("conn1", "db", "10.0.0.5:5432", "tcp")

	// The CR was written.
	u, err := getEndpoint(t, s, "replica-A", "conn1", "db")
	if err != nil {
		t.Fatalf("expected HubEndpoint CR: %v", err)
	}
	if got := parseProvider(u); got.mode != "direct" || got.addr != "10.0.0.5:5432" || got.owner != "replica-A" {
		t.Fatalf("unexpected CR spec: %+v", got)
	}

	// Lookup resolves it dial-direct (own partition, synchronous — no resync needed).
	tgt, ok := s.Lookup("db")
	if !ok {
		t.Fatal("Lookup(db) not found")
	}
	if tgt.Addr != "10.0.0.5:5432" || tgt.Protocol != "tcp" || tgt.Mux != nil || tgt.ForwardAddr != "" {
		t.Fatalf("unexpected target: %+v", tgt)
	}
}

func TestRegisterDeliverLocalMux(t *testing.T) {
	s := newTestStore(t, "replica-A")
	c1, _ := net.Pipe()
	sess, err := yamux.Client(c1, nil)
	if err != nil {
		t.Fatalf("yamux: %v", err)
	}
	defer sess.Close()

	s.RegisterDeliver("conn1", "cache", sess)
	tgt, ok := s.Lookup("cache")
	if !ok {
		t.Fatal("Lookup(cache) not found")
	}
	if tgt.Mux != sess {
		t.Fatalf("expected local Mux target, got %+v", tgt)
	}
	// The delivery CR carries no addr and this replica's forwardAddr.
	u, err := getEndpoint(t, s, "replica-A", "conn1", "cache")
	if err != nil {
		t.Fatalf("expected delivery CR: %v", err)
	}
	if got := parseProvider(u); got.mode != "deliver" || got.addr != "" || got.forwardAddr != "ws://self:5000" {
		t.Fatalf("unexpected delivery CR: %+v", got)
	}
}

func TestPeerDeliverResolvesToForwardAddr(t *testing.T) {
	s := newTestStore(t, "replica-A")
	writePeerEndpoint(t, s, "replica-B", "connX", "api", "deliver", "", "ws://pod-b:5000")
	writeLease(t, s, "replica-B", time.Now()) // live

	s.resync(context.Background())
	tgt, ok := s.Lookup("api")
	if !ok {
		t.Fatal("Lookup(api) not found")
	}
	if tgt.ForwardAddr != "ws://pod-b:5000" || tgt.ForwardName != "api" || tgt.Mux != nil {
		t.Fatalf("expected remote-delivery ForwardAddr target, got %+v", tgt)
	}
}

func TestPeerWithStaleLeaseFilteredOut(t *testing.T) {
	s := newTestStore(t, "replica-A")
	writePeerEndpoint(t, s, "replica-B", "connX", "api", "deliver", "", "ws://pod-b:5000")
	writeLease(t, s, "replica-B", time.Now().Add(-time.Hour)) // stale: renew + duration < now

	s.resync(context.Background())
	if _, ok := s.Lookup("api"); ok {
		t.Fatal("Lookup(api) should be filtered out for a stale owner lease")
	}
	if cat := s.Catalog(); len(cat) != 0 {
		t.Fatalf("Catalog should be empty, got %v", cat)
	}
}

func TestPeerWithNoLeaseFilteredOut(t *testing.T) {
	s := newTestStore(t, "replica-A")
	writePeerEndpoint(t, s, "replica-B", "connX", "api", "direct", "10.0.0.9:80", "ws://pod-b:5000")
	// No Lease for replica-B at all.
	s.resync(context.Background())
	if _, ok := s.Lookup("api"); ok {
		t.Fatal("Lookup(api) should be filtered out when the owner has no lease")
	}
}

func TestRemoveConnDeletesCRs(t *testing.T) {
	s := newTestStore(t, "replica-A")
	s.Register("conn1", "db", "10.0.0.5:5432", "")
	s.Register("conn1", "web", "10.0.0.6:80", "")
	if _, err := getEndpoint(t, s, "replica-A", "conn1", "db"); err != nil {
		t.Fatalf("db CR missing: %v", err)
	}

	s.RemoveConn("conn1")
	if _, ok := s.Lookup("db"); ok {
		t.Fatal("Lookup(db) should be gone after RemoveConn")
	}
	if _, err := getEndpoint(t, s, "replica-A", "conn1", "db"); !apierrors.IsNotFound(err) {
		t.Fatalf("db CR should be deleted, err=%v", err)
	}
	if _, err := getEndpoint(t, s, "replica-A", "conn1", "web"); !apierrors.IsNotFound(err) {
		t.Fatalf("web CR should be deleted, err=%v", err)
	}
}

func TestCatalogListsLiveServices(t *testing.T) {
	s := newTestStore(t, "replica-A")
	s.Register("conn1", "db", "10.0.0.5:5432", "") // own
	s.Register("conn1", "web", "10.0.0.6:80", "")  // own
	writePeerEndpoint(t, s, "replica-B", "connX", "api", "direct", "10.0.0.9:80", "ws://pod-b:5000")
	writeLease(t, s, "replica-B", time.Now()) // live peer
	writePeerEndpoint(t, s, "replica-C", "connY", "ghost", "direct", "10.0.0.10:80", "ws://pod-c:5000")
	// replica-C has no lease -> ghost is filtered.

	s.resync(context.Background())
	cat := s.Catalog()
	want := []string{"api", "db", "web"}
	if len(cat) != len(want) {
		t.Fatalf("Catalog = %v, want %v", cat, want)
	}
	for i := range want {
		if cat[i] != want[i] {
			t.Fatalf("Catalog = %v, want %v", cat, want)
		}
	}
}

func TestBeatCreatesAndRenewsLease(t *testing.T) {
	s := newTestStore(t, "replica-A")
	ctx := context.Background()
	if err := s.beat(ctx); err != nil {
		t.Fatalf("beat create: %v", err)
	}
	l, err := s.cs.CoordinationV1().Leases(s.namespace).Get(ctx, leaseName("replica-A"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("lease not created: %v", err)
	}
	if l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity != "replica-A" {
		t.Fatalf("unexpected holder: %+v", l.Spec.HolderIdentity)
	}
	first := l.Spec.RenewTime.Time
	time.Sleep(2 * time.Millisecond)
	if err := s.beat(ctx); err != nil {
		t.Fatalf("beat renew: %v", err)
	}
	l2, _ := s.cs.CoordinationV1().Leases(s.namespace).Get(ctx, leaseName("replica-A"), metav1.GetOptions{})
	if !l2.Spec.RenewTime.Time.After(first) {
		t.Fatalf("RenewTime not advanced: %v vs %v", l2.Spec.RenewTime.Time, first)
	}
}

func TestOwnDeliveryWithoutMuxNotResolved(t *testing.T) {
	// A delivery provider whose mux was forgotten (RemoveConn cleared it) must not
	// resolve to a live target.
	s := newTestStore(t, "replica-A")
	c1, _ := net.Pipe()
	sess, _ := yamux.Client(c1, nil)
	defer sess.Close()
	s.RegisterDeliver("conn1", "cache", sess)
	s.mu.Lock()
	delete(s.muxes, endpointName("replica-A", "conn1", "cache"))
	s.mu.Unlock()
	if _, ok := s.Lookup("cache"); ok {
		t.Fatal("delivery with no local mux must not resolve")
	}
}

func TestKubeStorePeerKeyRoundTripAndLeaseExpiry(t *testing.T) {
	s := newTestStore(t, "replica-A")
	writeLease(t, s, "replica-A", time.Now())
	want := []byte("PUBLIC KEY PEM")
	if err := s.PublishPeerKey("replica-A", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.PeerKey("replica-A")
	if err != nil || !ok || string(got) != string(want) {
		t.Fatalf("PeerKey = %q, ok=%v, err=%v", got, ok, err)
	}

	obj, err := s.dyn.Resource(hubEndpointGVR).Namespace(s.namespace).Get(context.Background(), peerKeyName("replica-A"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs := obj.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != "Lease" || refs[0].Name != leaseName("replica-A") {
		t.Fatalf("peer key owner references = %+v", refs)
	}
	if names := s.Catalog(); len(names) != 0 {
		t.Fatalf("reserved peer key leaked into Catalog: %v", names)
	}

	lease, err := s.cs.CoordinationV1().Leases(s.namespace).Get(context.Background(), leaseName("replica-A"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	expired := metav1.NewMicroTime(time.Now().Add(-2 * leaseDuration))
	lease.Spec.RenewTime = &expired
	if _, err := s.cs.CoordinationV1().Leases(s.namespace).Update(context.Background(), lease, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.PeerKey("replica-A"); err != nil || ok || got != nil {
		t.Fatalf("expired PeerKey = %q, ok=%v, err=%v", got, ok, err)
	}
}

func TestKubeStoreCannotPublishAnotherReplicasKey(t *testing.T) {
	s := newTestStore(t, "replica-A")
	if err := s.PublishPeerKey("replica-B", []byte("PUBLIC")); err == nil {
		t.Fatal("published another replica's peer key")
	}
}

// TestHeartbeatRetryFitsInsideLivenessWindow enforces in code the constraint the
// beatAttempts comment states in words: one heartbeat tick's bounded retry must
// not consume a whole liveness window. If it can, an exhausted tick outlives the
// very liveness record it was refreshing — every provider this replica owns
// vanishes from every peer while the retry loop is still running, and nothing has
// yet reported a failure.
//
// The constants live in different files from the loop that spends them, which is
// how they drifted apart in the first place (the two hub stores carried 3s and 5s
// with no recorded reason, and the 5s side violated this bound). Asserting the
// relationship rather than the values lets either constant move for a good reason
// and fails only when the combination stops being safe.
func TestHeartbeatRetryFitsInsideLivenessWindow(t *testing.T) {
	// beatBounded sleeps i*beatBackoff before attempt i (i>0), so the backoffs sum
	// to (0+1+...+(beatAttempts-1)) * beatBackoff.
	var backoffSum time.Duration
	for i := 1; i < beatAttempts; i++ {
		backoffSum += time.Duration(i) * beatBackoff
	}
	worst := time.Duration(beatAttempts)*writeTimeout + backoffSum
	if worst >= leaseDuration {
		t.Fatalf("worst-case heartbeat tick is %v, which is not shorter than the %v liveness window: "+
			"an exhausted tick can outlive the record it renews (beatAttempts=%d, writeTimeout=%v, backoffSum=%v)",
			worst, leaseDuration, beatAttempts, writeTimeout, backoffSum)
	}
	// The heartbeat interval must also be comfortably shorter than the window, or a
	// single missed tick lapses the record.
	if heartbeatEvery*2 > leaseDuration {
		t.Fatalf("heartbeatEvery=%v leaves no room for a missed tick inside a %v window", heartbeatEvery, leaseDuration)
	}
}
