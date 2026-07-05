//go:build linux

package incushost

import (
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	incusapi "github.com/lxc/incus/v6/shared/api"
)

// imageSource maps a cornus image reference to an Incus instance source that
// pulls the OCI image directly from its registry.
//
// Incus (6.3+) runs OCI images via an OCI-protocol remote: it converts
// (flattens) the image with skopeo + umoci, so those must be on the daemon
// host's PATH. The source therefore carries Protocol="oci", the registry as
// Server, and repository:tag as Alias — the API form of
// `incus remote add <r> <server> --protocol=oci` + `incus launch <r>:<alias>`.
//
// PHASE-0 RISK: cornus images normally live in cornus's own registry at a
// localhost:PORT ref served over plain HTTP. Whether Incus pulls that directly
// (plain HTTP, this ref shape) must be confirmed against a real incusd; if not,
// the image is side-loaded into Incus's image store first (see
// IncusTarget.PrepareImage / a future import path). This function isolates that
// decision. CORNUS_INCUS_INSECURE_REGISTRIES lists registry hosts to address
// over http:// instead of https://.
func imageSource(ref string) (incusapi.InstanceSource, error) {
	if strings.TrimSpace(ref) == "" {
		return incusapi.InstanceSource{}, fmt.Errorf("incus: empty image reference")
	}
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		return incusapi.InstanceSource{}, fmt.Errorf("incus: parsing image reference %q: %w", ref, err)
	}
	registry := parsed.Context().RegistryStr()
	repo := parsed.Context().RepositoryStr()
	scheme := "https"
	if insecureRegistry(registry) {
		scheme = "http"
	}
	return incusapi.InstanceSource{
		Type:     "image",
		Protocol: "oci",
		Server:   scheme + "://" + registry,
		Alias:    repo + ":" + parsed.Identifier(),
	}, nil
}

// insecureRegistry reports whether host should be addressed over http:// — a
// localhost/127.0.0.1 registry (cornus's own default) or one listed in
// CORNUS_INCUS_INSECURE_REGISTRIES (comma/space separated host[:port]).
func insecureRegistry(host string) bool {
	h := host
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	for _, f := range strings.FieldsFunc(os.Getenv("CORNUS_INCUS_INSECURE_REGISTRIES"), func(r rune) bool {
		return r == ',' || r == ' '
	}) {
		if f == host || f == h {
			return true
		}
	}
	return false
}
