package api

import "testing"

func TestIngressSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    *IngressSpec
		wantErr bool
	}{
		{"nil is valid", nil, false},
		{"empty host ok (auto-derived)", &IngressSpec{Enabled: true}, false},
		{"valid host", &IngressSpec{Hosts: []string{"app.example.com"}}, false},
		{"valid single label", &IngressSpec{Hosts: []string{"localhost"}}, false},
		{"multiple valid hosts", &IngressSpec{Hosts: []string{"a.example.com", "b.example.com"}}, false},
		{"invalid host with space", &IngressSpec{Hosts: []string{"not a host"}}, true},
		{"one invalid host among valid", &IngressSpec{Hosts: []string{"ok.example.com", "-bad.example.com"}}, true},
		{"valid domain override", &IngressSpec{Enabled: true, Domain: "preview.example.com"}, false},
		{"invalid domain override", &IngressSpec{Enabled: true, Domain: "not a domain"}, true},
		{"valid pathType", &IngressSpec{Hosts: []string{"a.example.com"}, PathType: "Exact"}, false},
		{"invalid pathType", &IngressSpec{Hosts: []string{"a.example.com"}, PathType: "Bogus"}, true},
		{"negative port", &IngressSpec{Hosts: []string{"a.example.com"}, Port: -1}, true},
		{"tls without secret or issuer is Validate-ok (backend enforces)", &IngressSpec{Hosts: []string{"a.example.com"}, TLS: &IngressTLS{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
