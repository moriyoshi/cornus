package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// TestStatelessDeployRejectsClientSourcedCredentials pins that the SERVER refuses
// a credential-bearing deploy on the sessionless endpoint.
//
// A client-sourced credential is minted on the caller's machine for the lifetime
// of a deploy-attach session (api.CredentialSource: "Backend names the
// CLIENT-side source backend ... It runs on the caller's machine"). POST
// /.cornus/v1/deploy has no session, so nothing can mint it and the workload's
// fetch finds no source.
//
// The CLI has always refused the combination in checkDetachable — but that is
// client-side only. A direct API caller, or any non-cornus client, reached
// backend.Apply with credentials set and the deploy SUCCEEDED; the credential
// simply never arrived, surfacing later as an application error a long way from
// the cause. This is the server half of a rule that previously existed only in
// the client.
func TestStatelessDeployRejectsClientSourcedCredentials(t *testing.T) {
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	spec := api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/web:v1",
		Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{
			{Name: "db", Backend: "static"},
		}},
	}
	body, _ := json.Marshal(spec)
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a deploy that cannot possibly broker its credentials must be refused, not accepted", resp.StatusCode)
	}
	var out map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !strings.Contains(out["error"], "client-sourced credentials") {
		t.Errorf("error does not name the reason: %q", out["error"])
	}
}

// TestStatelessDeployAcceptsAnEmptyCredentialBlock guards the boundary: a spec
// carrying a credentials block with no sources asks for nothing and must still
// deploy. Rejecting on `Credentials != nil` alone would break a compose file that
// declares the key and leaves it empty.
func TestStatelessDeployAcceptsAnEmptyCredentialBlock(t *testing.T) {
	srv := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	spec := api.DeploySpec{
		Name:        "web",
		Image:       "localhost:5000/web:v1",
		Credentials: &api.CredentialSpec{},
	}
	body, _ := json.Marshal(spec)
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: an empty credentials block requests nothing", resp.StatusCode)
	}
	var st api.DeployStatus
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st.Name != "web" {
		t.Errorf("deploy did not happen: %+v", st)
	}
}

var _ deploy.Backend = (*fakeBackend)(nil)
