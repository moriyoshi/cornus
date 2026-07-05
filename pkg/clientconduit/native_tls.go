package clientconduit

import (
	"fmt"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/ingressemu"
)

// MaterializeNativeTLS loads client-local certificate rules into transport-only
// Kubernetes Secret payloads. It is a no-op outside native ingress mode.
func MaterializeNativeTLS(spec *api.DeploySpec, cfg Config) error {
	if spec == nil || spec.Ingress == nil || cfg.Ingress == nil || cfg.Ingress.Mode != IngressNative || len(cfg.Ingress.Certificates) == 0 {
		return nil
	}
	if len(spec.Ingress.Hosts) == 0 {
		return fmt.Errorf("native ingress TLS with managed certificates requires explicit ingress hosts")
	}
	hosts := make([]string, 0, len(spec.Ingress.Hosts))
	for _, host := range spec.Ingress.Hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if host == "@" {
			return fmt.Errorf("native ingress TLS with managed certificates cannot resolve host %q client-side; use the concrete apex hostname", host)
		}
		hosts = append(hosts, host)
	}
	managed, err := ingressemu.MaterializeNativeCertificates(spec.Name, hosts, cfg.Ingress.Certificates)
	if err != nil {
		return err
	}
	if spec.Ingress.TLS == nil {
		spec.Ingress.TLS = &api.IngressTLS{}
	}
	spec.Ingress.TLS.ManagedCertificates = managed
	return nil
}

// MarkClientEmulatedIngress flags spec's ingress as client-emulated when the
// session's ingress mode is "emulate", so the deploy backend skips the cluster
// Ingress object (and its managed TLS Secret) — the client realizes the ingress
// itself as a reverse proxy reached through the conduit (Conduit.AddIngress). It is
// the emulate-mode counterpart of MaterializeNativeTLS and a no-op in any other
// mode (native keeps the real server Ingress; off/none leave the spec untouched so
// a plain deploy still gets a real Ingress). Idempotent.
func MarkClientEmulatedIngress(spec *api.DeploySpec, cfg Config) {
	if spec == nil || spec.Ingress == nil || cfg.Ingress == nil || cfg.Ingress.Mode != IngressEmulate {
		return
	}
	spec.Ingress.ClientEmulated = true
}
