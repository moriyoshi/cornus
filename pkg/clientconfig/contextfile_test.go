package clientconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// TestMerge covers the field-by-field overlay: a set field in src wins, a
// zero/nil field leaves dst in place, the one-way bools only enable, and the
// tri-state ViaServer can be set to an explicit false.
func TestMerge(t *testing.T) {
	t.Run("set fields win, unset preserve", func(t *testing.T) {
		dst := &Context{Server: "http://base:1", RegistryHost: "base-reg", Token: "base-tok"}
		src := &Context{RegistryHost: "override-reg"} // only registry-host given
		Merge(dst, src)
		if dst.Server != "http://base:1" {
			t.Errorf("Server = %q, want unchanged http://base:1", dst.Server)
		}
		if dst.RegistryHost != "override-reg" {
			t.Errorf("RegistryHost = %q, want override-reg", dst.RegistryHost)
		}
		if dst.Token != "base-tok" {
			t.Errorf("Token = %q, want unchanged base-tok", dst.Token)
		}
	})

	t.Run("insecure-skip-verify is one-way", func(t *testing.T) {
		dst := &Context{TLS: &TLS{InsecureSkipVerify: true, ServerName: "keep"}}
		// A src TLS with the bool false must not turn dst's true back off.
		Merge(dst, &Context{TLS: &TLS{ServerName: "new"}})
		if !dst.TLS.InsecureSkipVerify {
			t.Error("InsecureSkipVerify was cleared; the bool must be one-way")
		}
		if dst.TLS.ServerName != "new" {
			t.Errorf("ServerName = %q, want new", dst.TLS.ServerName)
		}
	})

	t.Run("via-server tri-state accepts explicit false", func(t *testing.T) {
		dst := &Context{ViaServer: boolPtr(true)}
		Merge(dst, &Context{ViaServer: boolPtr(false)})
		if dst.ViaServer == nil || *dst.ViaServer != false {
			t.Errorf("ViaServer = %v, want explicit false", dst.ViaServer)
		}
		// A nil src ViaServer leaves dst untouched.
		dst2 := &Context{ViaServer: boolPtr(true)}
		Merge(dst2, &Context{})
		if dst2.ViaServer == nil || *dst2.ViaServer != true {
			t.Errorf("ViaServer = %v, want unchanged true", dst2.ViaServer)
		}
	})

	t.Run("nested sub-struct is allocated when dst lacks it", func(t *testing.T) {
		dst := &Context{}
		Merge(dst, &Context{PortForward: &PortForward{Namespace: "ns", Service: "svc", RemotePort: 8080}})
		if dst.PortForward == nil || dst.PortForward.Service != "svc" || dst.PortForward.RemotePort != 8080 {
			t.Errorf("PortForward = %+v, want ns/svc/8080", dst.PortForward)
		}
	})
}

// TestFieldClassification checks the sensitive/safe split, the strip, and the
// endpoint/credential predicates the per-project trust model relies on.
func TestFieldClassification(t *testing.T) {
	c := &Context{
		Server:       "http://x",
		RegistryHost: "reg",
		Token:        "t",
		TLS:          &TLS{InsecureSkipVerify: true},
		ViaServer:    boolPtr(true),
	}
	all, sensitive := FieldNames(c)
	// via-server is the only non-sensitive field here.
	wantAll := []string{"server", "registry-host", "token", "tls", "via-server"}
	if !reflect.DeepEqual(all, wantAll) {
		t.Errorf("all fields = %v, want %v", all, wantAll)
	}
	wantSensitive := []string{"server", "registry-host", "token", "tls"}
	if !reflect.DeepEqual(sensitive, wantSensitive) {
		t.Errorf("sensitive fields = %v, want %v", sensitive, wantSensitive)
	}

	if !c.SetsEndpoint() {
		t.Error("SetsEndpoint() = false, want true (server set)")
	}
	if !c.SuppliesCredential() {
		t.Error("SuppliesCredential() = false, want true (token set)")
	}

	stripped := StripSensitive(c)
	if !reflect.DeepEqual(stripped, wantSensitive) {
		t.Errorf("StripSensitive returned %v, want %v", stripped, wantSensitive)
	}
	if c.Server != "" || c.RegistryHost != "" || c.Token != "" || c.TLS != nil {
		t.Errorf("sensitive fields not zeroed: %+v", c)
	}
	if c.ViaServer == nil || *c.ViaServer != true {
		t.Error("StripSensitive must keep the safe via-server field")
	}
	// A registry-host-only context does not set the endpoint and carries no credential.
	rc := &Context{RegistryHost: "reg"}
	if rc.SetsEndpoint() || rc.SuppliesCredential() {
		t.Errorf("registry-host-only: SetsEndpoint=%v SuppliesCredential=%v, want both false",
			rc.SetsEndpoint(), rc.SuppliesCredential())
	}
	// A kube-auth context supplies a credential and sets no direct endpoint by itself.
	kc := &Context{KubeAuth: &KubeAuth{}}
	if !kc.SuppliesCredential() {
		t.Error("kube-auth: SuppliesCredential() = false, want true")
	}
}

// TestLoadContextFileFormatParity proves the same logical context decodes to an
// equal Context from JSON, YAML, and TOML — guarding the TOML JSON round-trip that
// maps TOML keys onto the identical json: field names.
func TestLoadContextFileFormatParity(t *testing.T) {
	want := &Context{
		Server:       "https://demo.invalid:8443",
		RegistryHost: "reg.demo.invalid",
		Token:        "tok",
		ViaServer:    boolPtr(true),
		TLS:          &TLS{InsecureSkipVerify: true, ServerName: "demo.invalid"},
		PortForward:  &PortForward{Namespace: "ns", Service: "svc", RemotePort: 8080},
	}
	files := map[string]string{
		"cornus-context.json": `{
  "server": "https://demo.invalid:8443",
  "registry-host": "reg.demo.invalid",
  "token": "tok",
  "via-server": true,
  "tls": {"insecure-skip-verify": true, "server-name": "demo.invalid"},
  "port-forward": {"namespace": "ns", "service": "svc", "remote-port": 8080}
}`,
		"cornus-context.yaml": `server: https://demo.invalid:8443
registry-host: reg.demo.invalid
token: tok
via-server: true
tls:
  insecure-skip-verify: true
  server-name: demo.invalid
port-forward:
  namespace: ns
  service: svc
  remote-port: 8080
`,
		"cornus-context.toml": `server = "https://demo.invalid:8443"
registry-host = "reg.demo.invalid"
token = "tok"
via-server = true

[tls]
insecure-skip-verify = true
server-name = "demo.invalid"

[port-forward]
namespace = "ns"
service = "svc"
remote-port = 8080
`,
	}
	dir := t.TempDir()
	for name, body := range files {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadContextFile(p)
			if err != nil {
				t.Fatalf("LoadContextFile(%s): %v", name, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("LoadContextFile(%s) mismatch:\n got %+v\nwant %+v", name, got, want)
			}
		})
	}
}

// TestLoadContextFileStrict rejects an unknown key (a typo, or a full config
// document) in every format, so a bad override fails loudly rather than silently
// decoding to an empty context.
func TestLoadContextFileStrict(t *testing.T) {
	cases := map[string]string{
		"unknown.json": `{"server": "http://x", "bogus": true}`,
		"unknown.yaml": "server: http://x\nbogus: true\n",
		"unknown.toml": "server = \"http://x\"\nbogus = true\n",
		// A full config document (contexts:/current-context:) is rejected too.
		"fullconfig.yaml": "current-context: prod\ncontexts:\n  prod:\n    server: http://x\n",
	}
	dir := t.TempDir()
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadContextFile(p); err == nil {
				t.Fatalf("LoadContextFile(%s) = nil error, want strict rejection", name)
			}
		})
	}
}

// TestLoadContextFileResolveRuleValidation rejects an uncompilable SOCKS5 resolve
// pattern, which the strict decode alone would not catch.
func TestLoadContextFileResolveRuleValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cornus-context.yaml")
	body := "conduit:\n  socks5:\n    resolve:\n      - pattern: \"[\"\n        replace: x\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContextFile(p); err == nil {
		t.Fatal("LoadContextFile with an invalid regexp pattern = nil error, want failure")
	}
}

func TestConduitMergeNestedAndImmutable(t *testing.T) {
	disabled := false
	base := &Conduit{
		Mode: "socks5",
		Socks5: &Socks5{
			Listen:            "127.0.0.1:1080",
			ServiceHostSuffix: ".profile.internal",
			Resolve:           []ResolveRule{{Pattern: "profile", Replace: "one"}},
			BareServiceNames:  &disabled,
		},
		Ingress: &Ingress{
			Mode: "emulate",
			Controller: &IngressController{
				Namespace: "profile-ns",
				Service:   "profile-controller",
				HTTPPort:  80,
				HTTPSPort: 443,
			},
			CAFile:    "profile-ca.pem",
			CAKeyFile: "profile-ca.key",
		},
	}
	wantBase := base.Clone()
	enabled := true
	override := &Conduit{
		Socks5: &Socks5{
			Listen:           "127.0.0.1:1088",
			Resolve:          []ResolveRule{{Pattern: "override", Replace: "two"}},
			BareServiceNames: &enabled,
		},
		Ingress: &Ingress{
			Mode:       "native",
			Controller: &IngressController{Service: "override-controller"},
			CAFile:     "override-ca.pem",
		},
	}

	got := base.Merge(override)
	if !reflect.DeepEqual(base, wantBase) {
		t.Fatalf("Merge mutated base: got %+v, want %+v", base, wantBase)
	}
	if got.Mode != "socks5" || got.Socks5.Listen != "127.0.0.1:1088" || got.Socks5.ServiceHostSuffix != ".profile.internal" {
		t.Errorf("merged conduit/socks5 = %+v", got)
	}
	if len(got.Socks5.Resolve) != 1 || got.Socks5.Resolve[0].Pattern != "override" {
		t.Errorf("resolve rules = %+v, want wholesale override", got.Socks5.Resolve)
	}
	if got.Socks5.BareServiceNames == nil || !*got.Socks5.BareServiceNames {
		t.Errorf("bare-service-names = %v, want true", got.Socks5.BareServiceNames)
	}
	if got.Ingress.Mode != "native" || got.Ingress.Controller.Namespace != "profile-ns" || got.Ingress.Controller.Service != "override-controller" {
		t.Errorf("merged ingress/controller = %+v", got.Ingress)
	}
	if got.Ingress.CAFile != "override-ca.pem" || got.Ingress.CAKeyFile != "profile-ca.key" {
		t.Errorf("merged CA = %q/%q", got.Ingress.CAFile, got.Ingress.CAKeyFile)
	}
}
