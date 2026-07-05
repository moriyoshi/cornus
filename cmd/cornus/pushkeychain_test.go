package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// dockerConfigWith writes a Docker config.json holding one registry credential
// and points the process at it DETERMINISTICALLY.
//
// Both variables are needed, and the reason is in go-containerregistry's
// resolution order (authn/keychain.go): it decides whether a Docker config exists
// by looking at $HOME/.docker/config.json FIRST, and only falls back to
// $DOCKER_CONFIG when that is absent — but the subsequent config.Load() reads
// $DOCKER_CONFIG regardless. Setting only DOCKER_CONFIG therefore behaves
// differently on a developer machine that has a real ~/.docker/config.json than
// on a clean CI box. Setting HOME to an empty temp dir removes that dependence.
//
// REGISTRY_AUTH_FILE and XDG_RUNTIME_DIR are cleared because the same function
// falls back to Podman's auth.json through them; an unrelated file on the host
// must not be able to satisfy — or break — this test.
func dockerConfigWith(t *testing.T, registry, user, pass string) {
	t.Helper()
	dir := t.TempDir()
	cfg := map[string]any{
		"auths": map[string]any{
			registry: map[string]any{
				"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + pass)),
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir()) // empty: no ~/.docker/config.json
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

// TestBearerForRegistryDelegatesToDockerCredentials is the half of the push
// keychain's contract that the existing TestBearerForRegistry could not observe.
//
// That test asserts the cornus token does not leak to a non-destination host, and
// then deliberately declines to require any particular authenticator for it —
// "what DefaultKeychain yields depends on the developer's Docker config". On a
// machine with no Docker login, delegation and the old hardcoded
// `return authn.Anonymous, nil` produce the SAME answer, so restoring the bug
// passed. The bug it guards is real: a cross-registry `crane.Copy` whose SOURCE is
// private failed even when `docker login` could authenticate it.
//
// Installing a deterministic credential is what makes the two distinguishable —
// anonymous is no longer the correct answer for that host, so a resolver that
// hardcodes it fails.
func TestBearerForRegistryDelegatesToDockerCredentials(t *testing.T) {
	const (
		srcHost = "privateregistry.thirdparty.com"
		user    = "copy-bot"
		pass    = "s3cret-from-docker-login"
	)
	dockerConfigWith(t, srcHost, user, pass)

	kc := &bearerForRegistry{registry: "localhost:5000", token: "cornus-token"}

	ref, err := name.ParseReference(srcHost + "/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kc.Resolve(ref.Context())
	if err != nil {
		t.Fatalf("Resolve(source): %v", err)
	}
	cfg, err := got.Authorization()
	if err != nil {
		t.Fatalf("Authorization(): %v", err)
	}

	if cfg.Username != user || cfg.Password != pass {
		t.Errorf("source host resolved to %+v, want the Docker-config credential %s/%s.\n"+
			"The keychain is not delegating to authn.DefaultKeychain, so a cross-registry copy from a "+
			"private source fails even though `docker login` would authenticate it.", cfg, user, pass)
	}
	// The whole point of the type: the cornus token is for the destination only.
	if cfg.RegistryToken == "cornus-token" || cfg.Password == "cornus-token" {
		t.Errorf("the cornus bearer token leaked to the SOURCE registry: %+v", cfg)
	}
}

// TestBearerForRegistryStillPrefersTheDestinationBearer is the positive control
// for the test above: with a Docker credential now installed, the destination host
// must STILL get the cornus bearer rather than falling through to it. A resolver
// that delegated everything would satisfy the delegation test and break pushes.
func TestBearerForRegistryStillPrefersTheDestinationBearer(t *testing.T) {
	// A credential for the DESTINATION host, which must lose to the bearer.
	dockerConfigWith(t, "localhost:5000", "docker-user", "docker-pass")

	kc := &bearerForRegistry{registry: "localhost:5000", token: "cornus-token"}
	ref, err := name.ParseReference("localhost:5000/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kc.Resolve(ref.Context())
	if err != nil {
		t.Fatalf("Resolve(dest): %v", err)
	}
	b, ok := got.(*authn.Bearer)
	if !ok {
		t.Fatalf("Resolve(dest) = %#v, want *authn.Bearer: the destination must use the cornus token, "+
			"not whatever `docker login` left for that host", got)
	}
	if b.Token != "cornus-token" {
		t.Errorf("dest bearer = %q, want the cornus token", b.Token)
	}
}

// TestBearerForRegistryFallsBackToAnonymousWithoutCredentials pins the third case,
// so the delegation above cannot be mistaken for "always returns a credential":
// with no Docker config at all, an unrelated host must still resolve, anonymously,
// rather than erroring and failing the copy.
func TestBearerForRegistryFallsBackToAnonymousWithoutCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // empty dir: no config.json
	t.Setenv("REGISTRY_AUTH_FILE", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	kc := &bearerForRegistry{registry: "localhost:5000", token: "cornus-token"}
	ref, err := name.ParseReference("someregistry.example.com/app:v1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kc.Resolve(ref.Context())
	if err != nil {
		t.Fatalf("Resolve with no Docker config must not error: %v", err)
	}
	cfg, err := got.Authorization()
	if err != nil {
		t.Fatalf("Authorization(): %v", err)
	}
	if cfg.Username != "" || cfg.Password != "" || cfg.RegistryToken != "" || cfg.Auth != "" {
		t.Errorf("expected anonymous with no Docker config, got %+v", cfg)
	}
}
