package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"

	_ "cornus/pkg/creddelivery/awsimds"
	_ "cornus/pkg/creddelivery/generic"
	_ "cornus/pkg/credential/static"
)

// endpointCredSpec is a deploy whose only attachment is an endpoint-kind
// credential: a listener the server binds inside the workload's own network
// namespace. Like the env case, nothing runs beside the workload.
func endpointCredSpec() deploywire.DeployAttachSpec {
	return deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "shell",
			Image: "img",
			Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
				Name:       "db",
				Backend:    "static",
				Config:     map[string]string{"value": "s3cr3t"},
				Deliveries: []api.CredentialDelivery{{Kind: "endpoint"}},
			}}},
		},
		CredentialSources: []deploywire.CredentialBacking{{
			Name: "db", Backend: "static", Config: map[string]string{"value": "s3cr3t"},
		}},
	}
}

// endpointBackend is a co-located backend that can bind in a workload's network
// namespace. InstanceNetns returns an error because no workload exists in these
// tests — which is deliberate: it exercises the retry path rather than requiring
// a real namespace, and the assertions here are about ROUTING, not binding.
type endpointBackend struct {
	fakeAttachingBackend
	applies chan api.DeploySpec
}

func (e *endpointBackend) Remote() bool                                  { return false }
func (e *endpointBackend) BindsCredentialEndpoints(context.Context) bool { return true }
func (e *endpointBackend) InstanceNetns(context.Context, string, int) (string, error) {
	return "", context.Canceled
}

// Apply records the spec the co-located path built. The endpoint case reaches
// plain Apply, not ApplyWithAttachments — that IS the property under test.
func (e *endpointBackend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	st, err := e.fakeAttachingBackend.Apply(ctx, spec)
	select {
	case e.applies <- spec:
	default:
	}
	return st, err
}

// TestEndpointCredentialsDoNotRequireAdvertiseURL is the endpoint analogue of the
// original bug, and the point of this whole change.
//
// An endpoint delivery used to be unconditionally caretaker-bound: NeedsCaretaker
// returned true for it whatever the backend could do, on the reasoning that a
// listener must exist inside the workload's network namespace and only something
// in the pod can put it there. That is true of kubernetes and false of a host
// backend, where the server can enter the namespace itself — so the deploy was
// refused for want of the address of a caretaker that need not exist.
func TestEndpointCredentialsDoNotRequireAdvertiseURL(t *testing.T) {
	allowNetnsEntry(t)
	eb := &endpointBackend{
		fakeAttachingBackend: fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)},
		applies:              make(chan api.DeploySpec, 1),
	}
	srv := newTestServer(t, eb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")
	t.Setenv("CORNUS_AGENT_IMAGE", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, endpointCredSpec())

	select {
	case spec := <-eb.applies:
		// Reached the backend, so the deploy was not gated on an advertise URL.
		// The env is the other half: an endpoint nothing advertises is one the
		// workload cannot find, which would be a quieter version of the same bug.
		found := ""
		for k, v := range spec.Env {
			if strings.HasPrefix(k, "CORNUS_CREDENTIAL") {
				found = k + "=" + v
			}
		}
		if found == "" {
			t.Fatalf("the deploy reached the backend but its environment advertises no credential endpoint: %v", spec.Env)
		}
		if spec.Credentials != nil {
			t.Error("the credential block must be cleared once realized, or the backend logs that it ignored a credential it in fact delivered")
		}
	case e := <-errs:
		t.Fatalf("an endpoint-only deploy was refused with no advertise URL set, "+
			"but this backend binds the listener itself and nothing dials back: %s", e)
	case <-ctx.Done():
		t.Fatal("backend never received an apply")
	}
}

// TestEndpointAddressAssignment pins the plan the workload's environment is built
// from. Addresses must be settled before the container is created, so this runs
// with no workload in existence at all — which is exactly the property that lets
// the env be correct on a backend where the namespace does not yet exist.
func TestEndpointAddressAssignment(t *testing.T) {
	spec := api.DeploySpec{
		Name: "app",
		Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{
			{Name: "db", Deliveries: []api.CredentialDelivery{{Kind: "endpoint"}}},
			{Name: "api", Deliveries: []api.CredentialDelivery{{Kind: "endpoint"}}},
		}},
	}
	ce, err := prepareCredentialEndpoints(context.Background(), spec)
	if err != nil {
		t.Fatalf("prepareCredentialEndpoints: %v", err)
	}
	if len(ce.endpoints) != 2 {
		t.Fatalf("endpoints = %+v, want 2", ce.endpoints)
	}
	// Distinct ports. Two credentials on one port is the failure this catches:
	// the second listener would fail to bind and that credential would simply
	// never be served, while the first looked perfectly healthy.
	if ce.endpoints[0].Addr == ce.endpoints[1].Addr {
		t.Fatalf("both endpoints were assigned %s; the second could never bind", ce.endpoints[0].Addr)
	}
	for _, ep := range ce.endpoints {
		if !strings.HasPrefix(ep.Addr, "127.0.0.1:") {
			t.Errorf("endpoint %s bound at %s, want loopback — the namespace is the "+
				"authorization, and a non-loopback bind is reachable from outside it", ep.Name, ep.Addr)
		}
	}
	// Every endpoint must be discoverable: a listener nothing advertises is a
	// listener nothing uses.
	for _, ep := range ce.endpoints {
		found := false
		for _, v := range ce.env {
			if strings.Contains(v, ep.Addr) {
				found = true
			}
		}
		if !found {
			t.Errorf("no environment variable advertises %s at %s", ep.Name, ep.Addr)
		}
	}
}

// TestWellKnownEndpointTakesTheCanonicalAddress covers the IMDS shape, which is
// the case with NO environment variable at all — an unmodified SDK finds its
// credentials only because the address is the one it already looks for. A silent
// fallback to a loopback port would leave that SDK pointed at nothing.
func TestWellKnownEndpointTakesTheCanonicalAddress(t *testing.T) {
	spec := api.DeploySpec{
		Name: "app",
		Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
			Name:       "aws",
			Deliveries: []api.CredentialDelivery{{Kind: "endpoint", Provider: "aws-imds", WellKnown: true}},
		}}},
	}
	ce, err := prepareCredentialEndpoints(context.Background(), spec)
	if err != nil {
		t.Fatalf("prepareCredentialEndpoints: %v", err)
	}
	if len(ce.endpoints) != 1 {
		t.Fatalf("endpoints = %+v, want 1", ce.endpoints)
	}
	ep := ce.endpoints[0]
	if !ep.WellKnown {
		t.Fatal("the delivery asked for a well-known address and did not get one")
	}
	if !strings.HasPrefix(ep.Addr, "169.254.169.254:") {
		t.Fatalf("well-known address = %s, want the link-local IMDS address", ep.Addr)
	}
}

// TestEndpointEnvRefusesToOverwriteTheDeployment pins that a collision fails the
// deploy rather than winning it. Overwriting would produce a workload that comes
// up healthy pointed at cornus's endpoint instead of the one its author named —
// indistinguishable from success until something reads the wrong credential.
func TestEndpointEnvRefusesToOverwriteTheDeployment(t *testing.T) {
	spec := api.DeploySpec{
		Name: "app",
		Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
			Name:       "db",
			Deliveries: []api.CredentialDelivery{{Kind: "endpoint"}},
		}}},
	}
	ce, err := prepareCredentialEndpoints(context.Background(), spec)
	if err != nil {
		t.Fatalf("prepareCredentialEndpoints: %v", err)
	}
	var name string
	for k := range ce.env {
		name = k
		break
	}
	spec.Env = map[string]string{name: "http://the-authors-own-endpoint"}
	if _, err := ce.withEnv(spec); err == nil {
		t.Fatalf("%s was set by the deployment and silently overwritten; "+
			"the workload would come up healthy reading the wrong endpoint", name)
	}
}

// TestNoEndpointDeliveriesPlansNothing keeps the planning step inert for the
// deploys that do not use it — the common case.
func TestNoEndpointDeliveriesPlansNothing(t *testing.T) {
	spec := api.DeploySpec{
		Name: "app",
		Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
			Name:       "db",
			Deliveries: []api.CredentialDelivery{{Kind: "env", EnvVar: "T"}, {Kind: "file", Path: "/c/f"}},
		}}},
	}
	ce, err := prepareCredentialEndpoints(context.Background(), spec)
	if err != nil {
		t.Fatalf("prepareCredentialEndpoints: %v", err)
	}
	if ce != nil {
		t.Fatalf("planned %+v for a spec with no endpoint delivery", ce.endpoints)
	}
	if _, err := (*credentialEndpoints)(nil).withEnv(spec); err != nil {
		t.Fatalf("withEnv on a nil plan must be a no-op: %v", err)
	}
}

// allowNetnsEntry makes the capability probe succeed for one test, so a routing
// assertion is about routing rather than about whether the developer's machine
// happens to be root. The refusal is covered by its own test below.
func allowNetnsEntry(t *testing.T) {
	t.Helper()
	prev := canEnterNetns
	canEnterNetns = func() error { return nil }
	t.Cleanup(func() { canEnterNetns = prev })
}

// TestEndpointDeliveryIsRefusedWithoutThePrivilege is the counterpart, and it
// exists because of what an out-of-container run actually did.
//
// Binding inside a workload's namespace needs CAP_SYS_ADMIN; an unprivileged
// `cornus serve` on the host cannot even read a root-owned container's
// /proc/<pid>/ns/net. Before this check the deploy SUCCEEDED — the workload
// started, the serve loop retried forever, and the credential simply never
// appeared. A workload running without the credential it asked for, reported as
// a healthy deploy, is the worst of the available outcomes.
func TestEndpointDeliveryIsRefusedWithoutThePrivilege(t *testing.T) {
	prev := canEnterNetns
	canEnterNetns = func() error { return errNoNetnsPrivilege }
	defer func() { canEnterNetns = prev }()

	eb := &endpointBackend{
		fakeAttachingBackend: fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)},
		applies:              make(chan api.DeploySpec, 1),
	}
	srv := newTestServer(t, eb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, endpointCredSpec())

	select {
	case <-eb.applies:
		t.Fatal("the deploy was accepted without the privilege to bind the endpoint; the workload " +
			"would run and its credential would never arrive")
	case e := <-errs:
		if !strings.Contains(e, "network namespace") {
			t.Fatalf("the refusal must name the privilege it needs, got: %s", e)
		}
	case <-ctx.Done():
		t.Fatal("deploy neither succeeded nor failed")
	}
}

var errNoNetnsPrivilege = errNetnsProbe("cannot enter a network namespace (needs CAP_SYS_ADMIN)")

type errNetnsProbe string

func (e errNetnsProbe) Error() string { return string(e) }
