package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"

	// The CLIENT side of the relay runs the source backend; register static.
	_ "cornus/pkg/credential/static"
)

// attachErrors drives a deploy-attach session against srv and returns every
// terminal error the server reported, so a test can assert on the MESSAGE rather
// than only on whether the backend was reached. A deploy that succeeds returns
// no errors — the caller distinguishes the two by which it expected.
func attachErrors(t *testing.T, ctx context.Context, wsBase string, as deploywire.DeployAttachSpec) <-chan string {
	t.Helper()
	errs := make(chan string, 8)
	go func() {
		defer close(errs)
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as, nil, func(e deploywire.Event) {
			if e.Err != "" {
				select {
				case errs <- e.Err:
				default:
				}
			}
		}, nil, wire.ClientTransport{})
	}()
	return errs
}

// envCredSpec is a deploy whose only attachment is an env-kind credential: the
// server fetches the value once over the held session and the container gets it
// in its environment. Nothing here needs a process running beside the workload.
func envCredSpec() deploywire.DeployAttachSpec {
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

// TestEnvOnlyCredentialsDoNotRequireAdvertiseURL is the regression test for the
// reported failure: an in-container dockerhost deploy declaring credentials was
// refused with
//
//	client-local mounts and client-sourced credentials on the dockerhost backend
//	require CORNUS_ADVERTISE_URL (the cornus URL the caretaker dials back on)
//
// even though an env-kind delivery is resolved by the server at deploy time and
// no caretaker ever dials back. The gate keyed on "are there credentials at all",
// not on "does anything here need a relay", so it demanded the address of a
// caretaker that would never exist — and supplying one only moved the failure to
// the backend's own credential rejection.
//
// The assertion is that the backend is REACHED with the value resolved, with
// CORNUS_ADVERTISE_URL explicitly empty.
func TestEnvOnlyCredentialsDoNotRequireAdvertiseURL(t *testing.T) {
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	// The whole point: unset, as it is on a co-located server that never needed it.
	t.Setenv("CORNUS_ADVERTISE_URL", "")
	t.Setenv("CORNUS_AGENT_IMAGE", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, envCredSpec())

	select {
	case creds := <-fb.creds:
		if len(creds) != 1 || len(creds[0].EnvVars) != 1 {
			t.Fatalf("attach credentials = %+v, want one source with one resolved env var", creds)
		}
		if got := creds[0].EnvVars[0]; got.Var != "DB_TOKEN" || got.Value != "s3cr3t" {
			t.Fatalf("resolved env var = %+v, want DB_TOKEN=s3cr3t", got)
		}
		if len(creds[0].Deliveries) != 0 {
			t.Errorf("runtime deliveries = %+v, want none — an env delivery leaves nothing to serve", creds[0].Deliveries)
		}
	case e := <-errs:
		t.Fatalf("deploy was refused with no advertise URL set, but nothing here dials back: %s", e)
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithAttachments")
	}
}

// TestRuntimeCredentialDeliveryStillRequiresAdvertiseURL is the other half, and
// the reason the gate is narrowed rather than deleted: an endpoint delivery IS
// served by a caretaker, which does dial back, so it must still be refused up
// front. The message must name the DELIVERY KIND — "client-sourced credentials"
// alone was the misdirection that sent the original report looking at server
// configuration when the answer was in the spec.
func TestRuntimeCredentialDeliveryStillRequiresAdvertiseURL(t *testing.T) {
	fb := &fakeAttachingBackend{creds: make(chan []deploy.AttachCredential, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	as := envCredSpec()
	// An omitted Kind means "endpoint" per api.CredentialDelivery, which is the
	// shape a compose file most easily lands on by accident.
	as.Spec.Credentials.Sources[0].Deliveries = []api.CredentialDelivery{{Provider: "generic"}}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, as)

	select {
	case creds := <-fb.creds:
		t.Fatalf("endpoint delivery reached the backend with no relay to serve it: %+v", creds)
	case e := <-errs:
		if !strings.Contains(e, "CORNUS_ADVERTISE_URL") {
			t.Errorf("error = %q, want it to name CORNUS_ADVERTISE_URL", e)
		}
		if !strings.Contains(e, "endpoint") {
			t.Errorf("error = %q, want it to name the endpoint delivery kind", e)
		}
	case <-ctx.Done():
		t.Fatal("deploy neither failed nor reached the backend")
	}
}

// TestAdvertiseGateIgnoresEnvCredentialsWhenNamingEgress pins the message, not
// just the outcome. An egress deploy that ALSO carries an env credential still
// needs the URL — for the egress — and the error must say so without listing the
// credential, which needs nothing. Naming an attachment that is not the reason
// sends the operator to change the wrong thing.
func TestAdvertiseGateIgnoresEnvCredentialsWhenNamingEgress(t *testing.T) {
	fb := &fakeAttachingBackend{
		creds:  make(chan []deploy.AttachCredential, 1),
		egress: make(chan *deploy.AttachEgress, 1),
	}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", "")

	as := envCredSpec()
	as.Spec.Egress = &api.EgressSpec{Mode: "proxy", Default: "client"}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errs := attachErrors(t, ctx, wsBase, as)

	select {
	case <-fb.creds:
		t.Fatal("egress deploy reached the backend with no advertise URL")
	case e := <-errs:
		if !strings.Contains(e, "client-side egress") {
			t.Errorf("error = %q, want it to name client-side egress as the reason", e)
		}
		if strings.Contains(e, "credentials") {
			t.Errorf("error = %q, must not name the env credential — it needs no relay", e)
		}
	case <-ctx.Done():
		t.Fatal("deploy neither failed nor reached the backend")
	}
}
