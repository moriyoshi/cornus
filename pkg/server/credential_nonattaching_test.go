package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"

	_ "cornus/pkg/creddelivery/generic"
	_ "cornus/pkg/credential/static"
)

// nonAttachingBackend is a co-located backend with NO attachment path of its
// own: not an AttachingBackend, not an EgressBackend, no caretaker of any kind.
// That is incus exactly — its companion is a sibling INSTANCE, which cannot join
// the app's namespaces, so it carries neither mounts nor egress.
//
// Before this change such a backend could not receive a credential at all: an
// env-only deploy needs nothing but a merge into spec.Env and a plain Apply, and
// it still fell through the dispatch to "not yet supported".
type nonAttachingBackend struct {
	fakeBackend
	applies   chan api.DeploySpec
	endpoints bool
}

func (n *nonAttachingBackend) Name() string { return "incus" }
func (n *nonAttachingBackend) Remote() bool { return false }

func (n *nonAttachingBackend) BindsCredentialEndpoints(context.Context) bool { return n.endpoints }
func (n *nonAttachingBackend) InstanceNetns(context.Context, string, int) (string, error) {
	// No workload exists in these tests; the serve loop retries, and the
	// assertions here are about ROUTING.
	return "", context.Canceled
}

func (n *nonAttachingBackend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	st, err := n.fakeBackend.Apply(ctx, spec)
	select {
	case n.applies <- spec:
	default:
	}
	return st, err
}

func envOnlySpec() deploywire.DeployAttachSpec {
	return deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:  "shell",
			Image: "img",
			Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{
				Name:       "db",
				Backend:    "static",
				Config:     map[string]string{"value": "s3cr3t"},
				Deliveries: []api.CredentialDelivery{{Kind: "env", EnvVar: "DB_TOKEN"}},
			}}},
		},
		CredentialSources: []deploywire.CredentialBacking{{
			Name: "db", Backend: "static", Config: map[string]string{"value": "s3cr3t"},
		}},
	}
}

// TestEnvCredentialsReachABackendWithNoAttachmentPath is the incus fix.
//
// An env delivery is resolved by the server at deploy time and merged into the
// container's environment; nothing runs beside the workload and nothing dials
// back. A backend therefore does not need an attachment path to receive one —
// but the dispatch only had routes for backends that had one, so incus was
// refused a credential it was perfectly capable of carrying.
func TestEnvCredentialsReachABackendWithNoAttachmentPath(t *testing.T) {
	nb := &nonAttachingBackend{applies: make(chan api.DeploySpec, 1)}
	srv, _ := newMappedTestServer(t, nb, identityMapper)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, envOnlySpec())

	select {
	case spec := <-nb.applies:
		if got := spec.Env["DB_TOKEN"]; got != "s3cr3t" {
			t.Fatalf("DB_TOKEN = %q, want the value the client minted; env = %v", got, spec.Env)
		}
		if spec.Credentials != nil {
			t.Error("the credential block must be cleared once realized, or the backend warns it ignored a credential it in fact delivered")
		}
	case e := <-errs:
		t.Fatalf("an env-only credential deploy was refused on a backend with no attachment "+
			"path, though it needs none — the value is fixed into the container environment "+
			"at create: %s", e)
	case <-ctx.Done():
		t.Fatal("backend never received an apply")
	}
}

// TestEndpointCredentialsReachABackendWithNoAttachmentPath is the same for the
// endpoint kind, which incus can also host: the server binds the listener inside
// the instance's network namespace from the host, so the sibling-instance limit
// that stops the COMPANION carrying mounts or egress does not apply.
func TestEndpointCredentialsReachABackendWithNoAttachmentPath(t *testing.T) {
	allowNetnsEntry(t)
	nb := &nonAttachingBackend{applies: make(chan api.DeploySpec, 1), endpoints: true}
	srv, _ := newMappedTestServer(t, nb, identityMapper)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	as := envOnlySpec()
	as.Spec.Credentials.Sources[0].Deliveries = []api.CredentialDelivery{{Kind: "endpoint"}}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, as)

	select {
	case spec := <-nb.applies:
		found := false
		for k := range spec.Env {
			if strings.HasPrefix(k, "CORNUS_CREDENTIAL") {
				found = true
			}
		}
		if !found {
			t.Fatalf("no environment variable advertises the credential endpoint: %v", spec.Env)
		}
	case e := <-errs:
		t.Fatalf("an endpoint credential deploy was refused on a backend that binds in the "+
			"workload's namespace: %s", e)
	case <-ctx.Done():
		t.Fatal("backend never received an apply")
	}
}

// TestUnrealizableDeliveryNamesTheKind pins the refusal an operator actually
// reads. A backend that realizes two kinds and refuses the third is now normal,
// so "client-sourced credentials are not supported by the incus backend" points
// at server configuration when the answer is one line of the spec.
func TestUnrealizableDeliveryNamesTheKind(t *testing.T) {
	// endpoints=false and no CredentialBinder: a file delivery has nowhere to go.
	nb := &nonAttachingBackend{applies: make(chan api.DeploySpec, 1)}
	srv, _ := newMappedTestServer(t, nb, identityMapper)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	as := envOnlySpec()
	as.Spec.Credentials.Sources[0].Deliveries = []api.CredentialDelivery{{Kind: "file", Path: "/creds/db.json"}}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, as)

	select {
	case <-nb.applies:
		t.Fatal("a file delivery was accepted by a backend that cannot place one; the workload " +
			"would come up healthy with no credential")
	case e := <-errs:
		if !strings.Contains(e, "file delivery") {
			t.Fatalf("the refusal must name the delivery KIND so an operator knows which line "+
				"of their spec to change, got: %s", e)
		}
	case <-ctx.Done():
		t.Fatal("deploy neither succeeded nor failed")
	}
}

// TestSpecCaretakerKindsForANonBindingBackend is the unit-level companion: with
// no capability at all, a file delivery is caretaker-bound and an env one is not.
func TestSpecCaretakerKindsForANonBindingBackend(t *testing.T) {
	spec := envOnlySpec().Spec
	if got := deploy.SpecCaretakerKinds(spec, deploy.ServerDelivers{}); len(got) != 0 {
		t.Fatalf("env delivery needs no caretaker, got %v", got)
	}
}
