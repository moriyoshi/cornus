package ingressroute

import (
	"testing"

	"cornus/pkg/api"
)

// TestEnabled pins the canonical server-side ingress gate. Every backend and
// every server path funnels through it, so the two things it must get right are
// (a) an ingress is requested when it says Enabled or names hosts, and (b) a
// CLIENT-EMULATED ingress is NOT a server-side ingress: it is realized entirely
// on the client through the SOCKS5 conduit and asks nothing of any backend.
//
// The ClientEmulated case is the one worth a test. Four host backends used to
// open-code this predicate without that term, so a client-emulated ingress made
// each of them warn that it could not create something the caller had never
// asked it to create.
func TestEnabled(t *testing.T) {
	cases := []struct {
		name string
		in   *api.IngressSpec
		want bool
	}{
		{"nil spec", nil, false},
		{"empty spec", &api.IngressSpec{}, false},
		{"explicitly enabled", &api.IngressSpec{Enabled: true}, true},
		{"hosts imply enabled", &api.IngressSpec{Hosts: []string{"app.example"}}, true},
		{"client-emulated, enabled", &api.IngressSpec{Enabled: true, ClientEmulated: true}, false},
		{"client-emulated with hosts", &api.IngressSpec{Hosts: []string{"app.example"}, ClientEmulated: true}, false},
		{"client-emulated, otherwise empty", &api.IngressSpec{ClientEmulated: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Enabled(tc.in); got != tc.want {
				t.Errorf("Enabled(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
