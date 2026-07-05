package compose

import (
	"strings"
	"testing"
)

func TestTranslateIngressFullBlock(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress:
      host: app.example.com
      path: /api
      path_type: Prefix
      port: 80
      class_name: nginx
      annotations:
        nginx.ingress.kubernetes.io/rewrite-target: /
      tls:
        secret_name: my-cert
        cluster_issuer: letsencrypt-prod
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	in := plans["web"].Spec.Ingress
	if in == nil {
		t.Fatal("Ingress is nil")
	}
	if !in.Enabled {
		t.Fatal("Ingress presence must set Enabled")
	}
	if len(in.Hosts) != 1 || in.Hosts[0] != "app.example.com" {
		t.Fatalf("ingress hosts = %v", in.Hosts)
	}
	if in.Path != "/api" || in.PathType != "Prefix" || in.Port != 80 || in.ClassName != "nginx" {
		t.Fatalf("ingress scalars = %+v", in)
	}
	if in.Annotations["nginx.ingress.kubernetes.io/rewrite-target"] != "/" {
		t.Fatalf("annotations = %v", in.Annotations)
	}
	if in.TLS == nil || in.TLS.SecretName != "my-cert" || in.TLS.ClusterIssuer != "letsencrypt-prod" {
		t.Fatalf("tls = %+v", in.TLS)
	}
}

func TestTranslateIngressBareEnable(t *testing.T) {
	// The bare `x-cornus-ingress: {}` form enables ingress with every field
	// defaulted (host auto-derived server-side) — the preview-env ergonomics.
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress: {}
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	in := plans["web"].Spec.Ingress
	if in == nil || !in.Enabled {
		t.Fatalf("bare ingress must enable, got %+v", in)
	}
	if len(in.Hosts) != 0 {
		t.Fatalf("bare ingress must leave hosts empty for auto-derivation, got %v", in.Hosts)
	}
}

func TestTranslateIngressHostAndHostsUnion(t *testing.T) {
	// `host` scalar sugar is unioned with the `hosts` list (host first); "@" rides
	// through verbatim (the backend resolves the apex).
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress:
      host: "@"
      hosts:
        - www.example.com
        - api.example.com
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := plans["web"].Spec.Ingress.Hosts
	want := []string{"@", "www.example.com", "api.example.com"}
	if len(got) != len(want) {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", got, want)
		}
	}
}

func TestBareIngressDerivesPerProjectSubdomain(t *testing.T) {
	// The bare-enable form derives "<service>.<project>" as the subdomain, so the
	// same service in two different projects gets distinct hostnames (the backend
	// then appends ".<domain>"). A flat "<project>-<service>" name would not.
	compose := `
name: PLACEHOLDER
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress: {}
`
	subFor := func(project string) string {
		file := writeCompose(t, strings.Replace(compose, "PLACEHOLDER", project, 1))
		project0, err := Load(file)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		plans, err := project0.Plan(project)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		in := plans["web"].Spec.Ingress
		if in == nil {
			t.Fatal("ingress nil")
		}
		if len(in.Hosts) != 0 {
			t.Fatalf("bare ingress must leave hosts empty, got %v", in.Hosts)
		}
		return in.Subdomain
	}
	a := subFor("pr-123")
	b := subFor("pr-124")
	if a != "web.pr-123" {
		t.Fatalf("subdomain = %q, want web.pr-123", a)
	}
	if b != "web.pr-124" {
		t.Fatalf("subdomain = %q, want web.pr-124", b)
	}
	if a == b {
		t.Fatalf("two projects must derive distinct subdomains (both %q)", a)
	}
}

func TestTranslateIngressSubdomainOverride(t *testing.T) {
	// An explicit subdomain wins over the "<service>.<project>" default.
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress:
      subdomain: custom
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plans["web"].Spec.Ingress.Subdomain; got != "custom" {
		t.Fatalf("subdomain = %q, explicit value must win", got)
	}
}

func TestTranslateIngressBareBool(t *testing.T) {
	// `x-cornus-ingress: true` is accepted as the bare-enable form too.
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress: true
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	in := plans["web"].Spec.Ingress
	if in == nil || !in.Enabled {
		t.Fatalf("bare bool ingress must enable, got %+v", in)
	}
}

func TestProjectLevelIngressDefaultsInherited(t *testing.T) {
	// A project-level x-cornus-ingress supplies domain/class/issuer defaults that
	// each OPTED-IN service inherits by field; a service value wins, and a service
	// WITHOUT its own block is not auto-exposed.
	file := writeCompose(t, `
name: proj
x-cornus-ingress:
  domain: preview.example.com
  class_name: nginx
  tls:
    cluster_issuer: letsencrypt-prod
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress: {}
  api:
    image: api:latest
    ports:
      - "9000:90"
    x-cornus-ingress:
      class_name: traefik
  worker:
    image: worker:latest
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan("proj")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// web inherits every project default.
	web := plans["web"].Spec.Ingress
	if web == nil || web.Domain != "preview.example.com" || web.ClassName != "nginx" {
		t.Fatalf("web ingress = %+v (should inherit project domain/class)", web)
	}
	if web.TLS == nil || web.TLS.ClusterIssuer != "letsencrypt-prod" {
		t.Fatalf("web tls = %+v (should inherit project issuer)", web.TLS)
	}

	// api overrides the class but still inherits domain + issuer.
	api := plans["api"].Spec.Ingress
	if api == nil || api.ClassName != "traefik" {
		t.Fatalf("api class = %+v (service value must win)", api)
	}
	if api.Domain != "preview.example.com" {
		t.Fatalf("api domain = %q (should still inherit the project default)", api.Domain)
	}

	// worker declared no ingress -> not auto-exposed by the project default.
	if plans["worker"].Spec.Ingress != nil {
		t.Fatalf("worker must NOT get an ingress (opt-in per service)")
	}
}

func TestTranslateIngressInvalidHostRejected(t *testing.T) {
	file := writeCompose(t, `
name: proj
services:
  web:
    image: web:latest
    ports:
      - "8080:80"
    x-cornus-ingress:
      host: "not a host"
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := project.Plan("proj"); err == nil {
		t.Fatal("expected Plan to reject an invalid ingress host")
	}
}
