package main

import "testing"

func TestNormalizeIngressConduitMode(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"native", "native", false},
		{"Emulate", "emulate", false},
		{"off", "", false},
		{"none", "", false},
		{"", "", false},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		got, err := normalizeIngressConduitMode(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("normalizeIngressConduitMode(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("normalizeIngressConduitMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseIngressControllerFlag(t *testing.T) {
	c, err := parseIngressControllerFlag("ingress-nginx/ingress-nginx-controller")
	if err != nil {
		t.Fatal(err)
	}
	if c.Namespace != "ingress-nginx" || c.Service != "ingress-nginx-controller" || c.HTTPPort != 80 || c.HTTPSPort != 443 {
		t.Fatalf("default ports: %+v", c)
	}
	c, err = parseIngressControllerFlag("ns/svc:8080/8443")
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPPort != 8080 || c.HTTPSPort != 8443 {
		t.Fatalf("explicit ports: %+v", c)
	}
	for _, bad := range []string{"noslash", "/svc", "ns/", "ns/svc:bad", "ns/svc:99999/443"} {
		if _, err := parseIngressControllerFlag(bad); err == nil {
			t.Errorf("parseIngressControllerFlag(%q) should error", bad)
		}
	}
}
