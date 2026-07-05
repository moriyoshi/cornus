package server

import (
	"strings"
	"testing"

	"cornus/pkg/config"
)

func TestValidateDeployBackend(t *testing.T) {
	for _, ok := range append([]string{""}, knownDeployBackends...) {
		if err := validateDeployBackend(ok); err != nil {
			t.Errorf("validateDeployBackend(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"docker", "Docker", "dockerd", "kube", "k8", "nonsense"} {
		err := validateDeployBackend(bad)
		if err == nil {
			t.Errorf("validateDeployBackend(%q) = nil, want an error", bad)
			continue
		}
		if !strings.Contains(err.Error(), bad) {
			t.Errorf("error for %q does not name the offending value: %v", bad, err)
		}
		if !strings.Contains(err.Error(), "dockerhost") {
			t.Errorf("error for %q does not list the valid values: %v", bad, err)
		}
	}
}

// TestTypoedBackendIsRejectedNotDegraded is the regression this validation
// exists for. "docker" is not a backend name: the factory's switch falls through
// to dockerhost, but isHostBackend does NOT match it, so the registry silently
// dropped from host-native re-export to a classic CAS. That combination —
// right backend, wrong registry semantics, no diagnostic — sent builds pushing
// blobs into a store the operator never meant to use. It must fail loudly.
func TestTypoedBackendIsRejectedNotDegraded(t *testing.T) {
	t.Setenv("CORNUS_DEPLOY_BACKEND", "docker")

	if _, err := resolveRegistrySource(config.Config{}); err == nil {
		t.Fatal("resolveRegistrySource accepted CORNUS_DEPLOY_BACKEND=docker")
	}
	// The startup path cmd/cornus serve takes must reject it too, so the failure
	// happens before anything is served.
	if _, err := RegistryKeepsNoContentStore(config.Config{}); err == nil {
		t.Fatal("RegistryKeepsNoContentStore accepted CORNUS_DEPLOY_BACKEND=docker")
	}

	// Guard the silent-degradation shape explicitly: were it accepted, this is the
	// combination that used to result.
	if isHostBackend("docker") {
		t.Fatal("isHostBackend(\"docker\") is true; this test's premise no longer holds")
	}
}

// TestValidBackendsStillResolve pins that the validation does not reject any
// name the factory actually supports.
func TestValidBackendsStillResolve(t *testing.T) {
	for _, b := range append([]string{""}, knownDeployBackends...) {
		t.Setenv("CORNUS_DEPLOY_BACKEND", b)
		if _, err := resolveRegistrySource(config.Config{}); err != nil {
			t.Errorf("resolveRegistrySource with backend %q = %v, want nil", b, err)
		}
	}
}
