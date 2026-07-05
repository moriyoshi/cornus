package ingressemu

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
)

// CertificateSource describes one user-supplied TLS key pair. When Pattern is
// empty, every DNS SAN in the leaf certificate becomes a selector pattern.
type CertificateSource struct {
	Pattern  string
	CertFile string
	KeyFile  string
}

type certificateSelector struct {
	exact map[string]*tls.Certificate
	wild  map[string]*tls.Certificate
}

func loadCertificateSelector(sources []CertificateSource) (*certificateSelector, error) {
	s := &certificateSelector{exact: map[string]*tls.Certificate{}, wild: map[string]*tls.Certificate{}}
	for _, source := range sources {
		if source.CertFile == "" || source.KeyFile == "" {
			return nil, fmt.Errorf("ingressemu: certificate and key files must be provided together")
		}
		pair, err := tls.LoadX509KeyPair(source.CertFile, source.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("ingressemu: load certificate %s: %w", source.CertFile, err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("ingressemu: parse certificate %s: %w", source.CertFile, err)
		}
		pair.Leaf = leaf
		patterns := leaf.DNSNames
		if source.Pattern != "" {
			patterns = []string{source.Pattern}
		}
		if len(patterns) == 0 {
			return nil, fmt.Errorf("ingressemu: certificate %s has no DNS SAN; set an explicit pattern", source.CertFile)
		}
		for _, pattern := range patterns {
			pattern = normalizeCertificateHost(pattern)
			if source.Pattern != "" {
				probe := strings.TrimPrefix(pattern, "*.")
				if strings.HasPrefix(pattern, "*.") {
					probe = "probe." + probe
				}
				if err := leaf.VerifyHostname(probe); err != nil {
					return nil, fmt.Errorf("ingressemu: pattern %q is not covered by certificate %s", pattern, source.CertFile)
				}
			}
			target := s.exact
			key := pattern
			if strings.HasPrefix(pattern, "*.") {
				target = s.wild
				key = strings.TrimPrefix(pattern, "*")
			} else if strings.Contains(pattern, "*") {
				return nil, fmt.Errorf("ingressemu: invalid certificate pattern %q", pattern)
			}
			if _, exists := target[key]; exists {
				return nil, fmt.Errorf("ingressemu: duplicate certificate pattern %q", pattern)
			}
			target[key] = &pair
		}
	}
	return s, nil
}

func normalizeCertificateHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func (s *certificateSelector) certificate(serverName string) *tls.Certificate {
	host := normalizeCertificateHost(serverName)
	if cert := s.exact[host]; cert != nil {
		return cert
	}
	var best string
	for suffix := range s.wild {
		prefix := strings.TrimSuffix(host, suffix)
		if strings.HasSuffix(host, suffix) && prefix != "" && !strings.Contains(strings.TrimSuffix(prefix, "."), ".") && len(suffix) > len(best) {
			best = suffix
		}
	}
	return s.wild[best]
}
