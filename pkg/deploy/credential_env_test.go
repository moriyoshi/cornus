package deploy

import (
	"strings"
	"testing"

	"cornus/pkg/api"
)

// TestSpecCaretakerKindsNamesTheOmittedDefault pins the kind vocabulary
// the server's refusal message is built from. An omitted `kind:` means "endpoint"
// (api.CredentialDelivery), and a message that renders it as an empty string is
// how an operator ends up reading "credentials with  delivery".
func TestSpecCaretakerKindsNamesTheOmittedDefault(t *testing.T) {
	for _, tc := range []struct {
		name       string
		deliveries []api.CredentialDelivery
		want       string // strings.Join(kinds, ",")
	}{
		{"env only is not runtime", []api.CredentialDelivery{{Kind: "env", EnvVar: "T"}}, ""},
		{"omitted kind reads as endpoint", []api.CredentialDelivery{{Provider: "generic"}}, "endpoint"},
		{"file is runtime", []api.CredentialDelivery{{Kind: "file", Path: "/c"}}, "file"},
		{"mixed reports only the runtime half", []api.CredentialDelivery{
			{Kind: "env", EnvVar: "T"}, {Kind: "file", Path: "/c"},
		}, "file"},
		{"distinct and sorted", []api.CredentialDelivery{
			{Kind: "file", Path: "/a"}, {Provider: "generic"}, {Kind: "file", Path: "/b"},
		}, "endpoint,file"},
		{"no deliveries at all", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := api.DeploySpec{Credentials: &api.CredentialSpec{
				Sources: []api.CredentialSource{{Name: "c", Deliveries: tc.deliveries}},
			}}
			if got := strings.Join(SpecCaretakerKinds(spec, ServerDelivers{}), ","); got != tc.want {
				t.Errorf("runtime kinds = %q, want %q", got, tc.want)
			}
		})
	}
	// A spec with no credentials block at all must not report kinds — the server
	// distinguishes this from "env-only" and routes it differently.
	if got := SpecCaretakerKinds(api.DeploySpec{}, ServerDelivers{}); got != nil {
		t.Errorf("runtime kinds for a credential-less spec = %v, want nil", got)
	}
}

// TestSpecCaretakerKindsFollowsEachCapabilityIndependently pins that the two
// capabilities are read separately.
//
// It matters because they split the backends in OPPOSITE directions —
// containerd can be entered but has no server-written bind today, while a remote
// dockerhost has neither — so a capability struct read as "host backend, yes or
// no" would be wrong for at least one real configuration. Each row here is a
// configuration that actually exists.
func TestSpecCaretakerKindsFollowsEachCapabilityIndependently(t *testing.T) {
	spec := api.DeploySpec{Credentials: &api.CredentialSpec{
		Sources: []api.CredentialSource{{Name: "c", Deliveries: []api.CredentialDelivery{
			{Kind: "env", EnvVar: "T"},
			{Kind: "file", Path: "/c/f"},
			{Kind: "endpoint"},
		}}},
	}}
	for _, tc := range []struct {
		name string
		can  ServerDelivers
		want string
	}{
		{"kubernetes / incus: neither", ServerDelivers{}, "endpoint,file"},
		{"containerd: enters a netns, no server bind yet", ServerDelivers{Endpoints: true}, "file"},
		{"a backend that binds paths but not namespaces", ServerDelivers{Files: true}, "endpoint"},
		{"dockerhost and bare, local: both", ServerDelivers{Files: true, Endpoints: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(SpecCaretakerKinds(spec, tc.can), ","); got != tc.want {
				t.Errorf("caretaker kinds = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWithCredentialEnvRejectsEgressProxyCollision is the guard against a silent
// wrong value. In proxy mode the caretaker's proxy vars are authoritative and
// deliberately overwrite caller-set ones, so a credential delivered into
// HTTPS_PROXY would be discarded at container start with the workload coming up
// healthy and nothing in any log saying so. The deploy has to fail instead.
func TestWithCredentialEnvRejectsEgressProxyCollision(t *testing.T) {
	creds := []AttachCredential{{Name: "c", EnvVars: []CredentialEnvVar{{Var: "HTTPS_PROXY", Value: "http://cred"}}}}

	proxy := api.DeploySpec{Egress: &api.EgressSpec{Mode: "proxy"}}
	_, err := WithCredentialEnv(proxy, creds)
	if err == nil {
		t.Fatal("a credential delivered into HTTPS_PROXY must be refused in proxy mode, not silently overwritten")
	}
	if !strings.Contains(err.Error(), "HTTPS_PROXY") {
		t.Errorf("error = %v, want it to name the colliding variable", err)
	}

	// Transparent mode captures at the network namespace and sets no proxy env,
	// so the same variable name is merely proxy-SHAPED and must be allowed. A
	// blanket name ban here would refuse a deploy that works.
	transparent := api.DeploySpec{Egress: &api.EgressSpec{Mode: "transparent"}}
	got, err := WithCredentialEnv(transparent, creds)
	if err != nil {
		t.Fatalf("transparent mode sets no proxy env; the name must be allowed: %v", err)
	}
	if got.Env["HTTPS_PROXY"] != "http://cred" {
		t.Errorf("env = %v, want the credential value merged", got.Env)
	}
}

// TestWithCredentialEnvDoesNotMutateTheCallersSpec pins that the merge copies.
// The caller's spec is the session's, reread by every other attachment helper;
// writing through it would leak one deploy's secret into the next read of the
// same session.
func TestWithCredentialEnvDoesNotMutateTheCallersSpec(t *testing.T) {
	orig := api.DeploySpec{Env: map[string]string{"KEEP": "1"}}
	creds := []AttachCredential{{Name: "c", EnvVars: []CredentialEnvVar{{Var: "TOKEN", Value: "s"}}}}

	got, err := WithCredentialEnv(orig, creds)
	if err != nil {
		t.Fatalf("WithCredentialEnv: %v", err)
	}
	if _, leaked := orig.Env["TOKEN"]; leaked {
		t.Error("the caller's Env map was mutated; the secret leaks into every other reader of this spec")
	}
	if got.Env["TOKEN"] != "s" || got.Env["KEEP"] != "1" {
		t.Errorf("merged env = %v, want both KEEP and TOKEN", got.Env)
	}
}

// TestRealizeCredentialsClearsTheBlockOnlyWhenItRealizedSomething pins the
// warning interaction. warnUnsupported logs "the workload sees none of the
// declared credentials" whenever spec.Credentials is set, so the block must be
// cleared once the env deliveries land — and must NOT be cleared when there was
// no attachment at all, which is the stateless case the warning exists for.
func TestRealizeCredentialsClearsTheBlockOnlyWhenItRealizedSomething(t *testing.T) {
	spec := api.DeploySpec{Credentials: &api.CredentialSpec{Sources: []api.CredentialSource{{Name: "db"}}}}

	realized, err := RealizeCredentials(spec, []AttachCredential{{
		Name: "db", EnvVars: []CredentialEnvVar{{Var: "DB_TOKEN", Value: "s"}},
	}}, "dockerhost", "no caretaker here")
	if err != nil {
		t.Fatalf("RealizeCredentials: %v", err)
	}
	if realized.Credentials != nil {
		t.Error("credential block survived realization; the backend would warn that a delivered credential was ignored")
	}
	if realized.Env["DB_TOKEN"] != "s" {
		t.Errorf("env = %v, want DB_TOKEN merged", realized.Env)
	}

	untouched, err := RealizeCredentials(spec, nil, "dockerhost", "no caretaker here")
	if err != nil {
		t.Fatalf("RealizeCredentials(no attachments): %v", err)
	}
	if untouched.Credentials == nil {
		t.Error("credential block cleared with nothing realized; the stateless-apply warning would go silent")
	}
}
