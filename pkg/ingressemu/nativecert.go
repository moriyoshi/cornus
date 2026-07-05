package ingressemu

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sort"
	"strings"

	"cornus/pkg/api"
)

type nativeCertificateMaterial struct {
	patterns   []string
	certPEM    []byte
	privatePEM []byte
}

// MaterializeNativeCertificates loads and validates certificate sources,
// assigns each concrete native-ingress host with the same SNI precedence as
// emulated ingress, and returns Secret transport payloads grouped by key pair.
// Every host must match because native ingress has no client-side generated
// certificate fallback.
func MaterializeNativeCertificates(deployment string, hosts []string, sources []CertificateSource) ([]api.ManagedIngressCertificate, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	selector, err := loadCertificateSelector(sources)
	if err != nil {
		return nil, err
	}
	materials := make(map[[32]byte]*nativeCertificateMaterial, len(sources))
	for _, source := range sources {
		certPEM, err := os.ReadFile(source.CertFile)
		if err != nil {
			return nil, fmt.Errorf("native ingress TLS: read certificate %s: %w", source.CertFile, err)
		}
		privatePEM, err := os.ReadFile(source.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("native ingress TLS: read private key for certificate %s: %w", source.CertFile, err)
		}
		pair, err := tls.X509KeyPair(certPEM, privatePEM)
		if err != nil {
			return nil, fmt.Errorf("native ingress TLS: load certificate %s: %w", source.CertFile, err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("native ingress TLS: parse certificate %s: %w", source.CertFile, err)
		}
		patterns := leaf.DNSNames
		if source.Pattern != "" {
			patterns = []string{source.Pattern}
		}
		for i := range patterns {
			patterns[i] = normalizeCertificateHost(patterns[i])
		}
		fingerprint := sha256.Sum256(pair.Certificate[0])
		if existing := materials[fingerprint]; existing != nil {
			existing.patterns = append(existing.patterns, patterns...)
			continue
		}
		materials[fingerprint] = &nativeCertificateMaterial{
			patterns: append([]string(nil), patterns...), certPEM: certPEM, privatePEM: privatePEM,
		}
	}

	groups := make(map[*nativeCertificateMaterial][]string)
	order := make([]*nativeCertificateMaterial, 0, len(materials))
	seen := make(map[*nativeCertificateMaterial]bool)
	seenHosts := make(map[string]bool)
	for _, rawHost := range hosts {
		host := normalizeCertificateHost(rawHost)
		if host == "" || seenHosts[host] {
			continue
		}
		seenHosts[host] = true
		selected := selector.certificate(host)
		if selected == nil {
			return nil, fmt.Errorf("native ingress TLS: no certificate matches host %q", rawHost)
		}
		material := materials[sha256.Sum256(selected.Certificate[0])]
		if material == nil {
			return nil, fmt.Errorf("native ingress TLS: internal certificate selection error for host %q", rawHost)
		}
		if !seen[material] {
			seen[material] = true
			order = append(order, material)
		}
		groups[material] = append(groups[material], host)
	}

	result := make([]api.ManagedIngressCertificate, 0, len(order))
	for _, material := range order {
		result = append(result, api.ManagedIngressCertificate{
			Hosts:          append([]string(nil), groups[material]...),
			SecretName:     ManagedCertificateSecretName(deployment, material.patterns),
			CertificatePEM: append([]byte(nil), material.certPEM...),
			PrivateKeyPEM:  append([]byte(nil), material.privatePEM...),
		})
	}
	return result, nil
}

// ManagedCertificateSecretName returns a stable DNS-safe Secret name. Its hash
// depends on selector patterns, not key material, so certificate rotation updates
// the same Secret.
func ManagedCertificateSecretName(deployment string, patterns []string) string {
	normalized := append([]string(nil), patterns...)
	for i := range normalized {
		normalized[i] = normalizeCertificateHost(normalized[i])
	}
	sort.Strings(normalized)
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\x00")))
	prefix := dnsLabel(deployment)
	if prefix == "" {
		prefix = "ingress"
	}
	if len(prefix) > 45 {
		prefix = strings.Trim(prefix[:45], "-")
	}
	return fmt.Sprintf("%s-tls-%x", prefix, sum[:6])
}

func dnsLabel(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}

	}
	return strings.Trim(b.String(), "-")
}
