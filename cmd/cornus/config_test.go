package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/cliout"
	"cornus/pkg/clientconfig"
)

// TestConfigNullContextEntry guards against a nil-pointer panic when the config
// file contains a null-valued context (e.g. `contexts:\n  prod:`), which
// unmarshals to a nil *Context. get-contexts and view must print a clean result
// instead of dereferencing the nil pointer.
func TestConfigNullContextEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("contexts:\n  prod:\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cli := &CLI{Config: path}

	if err := (&ConfigGetContextsCmd{}).Run(cli); err != nil {
		t.Fatalf("get-contexts with null entry: %v", err)
	}
	if err := (&ConfigViewCmd{}).Run(cli); err != nil {
		t.Fatalf("view with null entry: %v", err)
	}
	if err := (&ConfigViewCmd{ShowTokens: true}).Run(cli); err != nil {
		t.Fatalf("view --show-tokens with null entry: %v", err)
	}
}

// TestConfigSetContextConduitMode covers the --conduit-mode spec grammar: a bare
// word stores just the mode; the socks5://.shared[:port][?suffix=…] sentinel
// populates the stored shared-proxy listen/suffix; and a session-local URL is
// rejected because it is a per-run, not a stored, choice.
func TestConfigSetContextConduitMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	// A bare word stores only the mode; no Socks5 block is created.
	if err := (&ConfigSetContextCmd{Name: "bare", ConduitMode: "socks5"}).Run(cli); err != nil {
		t.Fatalf("set-context bare: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["bare"]; c.Conduit == nil || c.Conduit.Mode != "socks5" || c.Conduit.Socks5 != nil {
		t.Fatalf("bare conduit-mode: %+v", c.Conduit)
	}

	// The .shared sentinel with a port + suffix pins the shared proxy's address.
	if err := (&ConfigSetContextCmd{Name: "shared", ConduitMode: "socks5://.shared:1099?suffix=.demo.internal"}).Run(cli); err != nil {
		t.Fatalf("set-context shared: %v", err)
	}
	f, _ = clientconfig.Load(path)
	c := f.Contexts["shared"]
	if c.Conduit == nil || c.Conduit.Mode != "socks5" || c.Conduit.Socks5 == nil {
		t.Fatalf("shared conduit-mode: %+v", c.Conduit)
	}
	if c.Conduit.Socks5.Listen != "127.0.0.1:1099" || c.Conduit.Socks5.ServiceHostSuffix != ".demo.internal" {
		t.Fatalf("shared conduit-mode socks5 block: %+v", c.Conduit.Socks5)
	}

	// A session-local URL is a per-run choice and is rejected by set-context.
	if err := (&ConfigSetContextCmd{Name: "local", ConduitMode: "socks5://127.0.0.1:1099"}).Run(cli); err == nil {
		t.Fatal("set-context with a session-local URL = nil error, want error")
	}

	// A malformed URL is rejected up front rather than stored.
	if err := (&ConfigSetContextCmd{Name: "bad", ConduitMode: "http://x"}).Run(cli); err == nil {
		t.Fatal("set-context with a non-socks5 URL = nil error, want error")
	}
}

func TestConfigSetUseDeleteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	// set-context creates a profile.
	if err := (&ConfigSetContextCmd{Name: "prod", Server: "https://prod", Token: "t1"}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["prod"]; c == nil || c.Server != "https://prod" || c.Token != "t1" {
		t.Fatalf("after set-context: %+v", f.Contexts["prod"])
	}

	// set-context --merge edits only the given field; the server is preserved.
	if err := (&ConfigSetContextCmd{Name: "prod", Token: "t2", Merge: true}).Run(cli); err != nil {
		t.Fatalf("set-context edit: %v", err)
	}
	f, _ = clientconfig.Load(path)
	if c := f.Contexts["prod"]; c.Server != "https://prod" || c.Token != "t2" {
		t.Fatalf("edit clobbered server or missed token: %+v", c)
	}

	// Without --merge the same edit replaces the context: only the given field
	// survives, the server is dropped.
	if err := (&ConfigSetContextCmd{Name: "prod", Token: "t3"}).Run(cli); err != nil {
		t.Fatalf("set-context replace: %v", err)
	}
	f, _ = clientconfig.Load(path)
	if c := f.Contexts["prod"]; c.Server != "" || c.Token != "t3" {
		t.Fatalf("replace should have dropped server: %+v", c)
	}

	// use-context sets the current context; an unknown name errors.
	if err := (&ConfigUseContextCmd{Name: "prod"}).Run(cli); err != nil {
		t.Fatalf("use-context: %v", err)
	}
	f, _ = clientconfig.Load(path)
	if f.CurrentContext != "prod" {
		t.Fatalf("CurrentContext = %q, want prod", f.CurrentContext)
	}
	if err := (&ConfigUseContextCmd{Name: "nope"}).Run(cli); err == nil {
		t.Error("use-context(unknown) = nil error, want error")
	}

	// delete-context removes it and clears the current-context pointer.
	if err := (&ConfigDeleteContextCmd{Name: "prod"}).Run(cli); err != nil {
		t.Fatalf("delete-context: %v", err)
	}
	f, _ = clientconfig.Load(path)
	if _, ok := f.Contexts["prod"]; ok {
		t.Error("prod still present after delete")
	}
	if f.CurrentContext != "" {
		t.Errorf("CurrentContext = %q after deleting current, want empty", f.CurrentContext)
	}
	if err := (&ConfigDeleteContextCmd{Name: "prod"}).Run(cli); err == nil {
		t.Error("delete-context(missing) = nil error, want error")
	}
}

func TestConfigSetContextTLSAndPortForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	if err := (&ConfigSetContextCmd{
		Name: "cluster", CACert: "/ca.pem", Insecure: true,
		PFNamespace: "cornus", PFService: "cornus", PFRemotePort: 5000,
	}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["cluster"]
	if c.TLS == nil || c.TLS.CACert != "/ca.pem" || !c.TLS.InsecureSkipVerify {
		t.Fatalf("TLS not stored: %+v", c.TLS)
	}
	if c.PortForward == nil || c.PortForward.Service != "cornus" || c.PortForward.RemotePort != 5000 {
		t.Fatalf("port-forward not stored: %+v", c.PortForward)
	}
}

func TestConfigSetContextNamespaceNoDetect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	// --namespace with --no-detect stores the namespace without contacting a
	// cluster, so no Service name is resolved.
	if err := (&ConfigSetContextCmd{Name: "cluster", Namespace: "cornus-system", NoDetect: true}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["cluster"]
	if c.PortForward == nil || c.PortForward.Namespace != "cornus-system" {
		t.Fatalf("namespace not stored: %+v", c.PortForward)
	}
	if c.PortForward.Service != "" {
		t.Errorf("Service = %q, want empty (detection skipped)", c.PortForward.Service)
	}
}

func TestConfigSetContextFirstContextDefaultPrompt(t *testing.T) {
	// Non-interactive (the test's stdin is not a terminal): the first context is
	// created but NOT made default, so scripts stay deterministic.
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	if err := (&ConfigSetContextCmd{Name: "prod", Server: "https://prod"}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if f.CurrentContext != "" {
		t.Fatalf("CurrentContext = %q, want empty (no prompt when non-TTY)", f.CurrentContext)
	}

	// Simulate an interactive "yes": the first context becomes the default.
	restore := confirmSetDefaultContext
	confirmSetDefaultContext = func(*cliout.Driver, string) bool { return true }
	defer func() { confirmSetDefaultContext = restore }()

	path2 := filepath.Join(t.TempDir(), "config.yaml")
	cli2 := &CLI{Config: path2}
	if err := (&ConfigSetContextCmd{Name: "first", Server: "https://first"}).Run(cli2); err != nil {
		t.Fatalf("set-context first: %v", err)
	}
	f2, _ := clientconfig.Load(path2)
	if f2.CurrentContext != "first" {
		t.Fatalf("CurrentContext = %q, want first (interactive yes on first context)", f2.CurrentContext)
	}

	// A second context is never offered as default, even if the prompt would say
	// yes — the current context stays put.
	if err := (&ConfigSetContextCmd{Name: "second", Server: "https://second"}).Run(cli2); err != nil {
		t.Fatalf("set-context second: %v", err)
	}
	f2, _ = clientconfig.Load(path2)
	if f2.CurrentContext != "first" {
		t.Fatalf("CurrentContext = %q, want first (second context must not steal default)", f2.CurrentContext)
	}
}

func TestConfigSetContextKubeAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	if err := (&ConfigSetContextCmd{
		Name: "cluster", KubeAuthServiceAccount: "cornus-client",
		KubeAuthAudience: "cornus", KubeAuthExpiration: 1800,
	}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	ka := f.Contexts["cluster"].KubeAuth
	if ka == nil || ka.ServiceAccount != "cornus-client" || ka.Audience != "cornus" || ka.ExpirationSeconds != 1800 {
		t.Fatalf("kube-auth not stored: %+v", ka)
	}
}

func TestConfigSetContextSSHTunnel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	if err := (&ConfigSetContextCmd{
		Name: "devbox", SSHHost: "devbox", SSHUser: "ops",
		SSHRemoteAddr: "127.0.0.1:5000", SSHTLS: true, ServerName: "cornus.example.com",
	}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["devbox"]
	st := c.SSHTunnel
	if st == nil || st.Addr != "devbox" || st.User != "ops" || st.RemoteAddr != "127.0.0.1:5000" || !st.RemoteTLS {
		t.Fatalf("ssh-tunnel not stored: %+v", st)
	}
	if c.TLS == nil || c.TLS.ServerName != "cornus.example.com" {
		t.Fatalf("tls server-name not stored: %+v", c.TLS)
	}

	// get-contexts renders an ssh-tunnel-only profile in the SERVER column.
	var buf strings.Builder
	cli2 := &CLI{Config: path}
	cli2.drv = cliout.New(cliout.Options{Stdout: &buf, Stderr: &buf, Output: "plain"})
	if err := (&ConfigGetContextsCmd{}).Run(cli2); err != nil {
		t.Fatalf("get-contexts: %v", err)
	}
	if !strings.Contains(buf.String(), "ssh-tunnel ops@devbox -> 127.0.0.1:5000") {
		t.Fatalf("get-contexts output missing ssh-tunnel rendering:\n%s", buf.String())
	}
}

func TestConfigSetContextMergeVsReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	// Seed a context with several fields, including nested blocks.
	if err := (&ConfigSetContextCmd{
		Name: "prod", Server: "https://prod", Token: "t1",
		CACert: "/ca.pem", PFNamespace: "cornus", NoDetect: true,
	}).Run(cli); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// --merge keeps every unset field, including the nested tls / port-forward blocks.
	if err := (&ConfigSetContextCmd{Name: "prod", Token: "t2", Merge: true}).Run(cli); err != nil {
		t.Fatalf("merge: %v", err)
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["prod"]
	if c.Server != "https://prod" || c.Token != "t2" || c.TLS == nil || c.TLS.CACert != "/ca.pem" || c.PortForward == nil {
		t.Fatalf("merge did not preserve unset fields: %+v", c)
	}

	// The default (replace) drops everything not named on this invocation.
	if err := (&ConfigSetContextCmd{Name: "prod", Token: "t3"}).Run(cli); err != nil {
		t.Fatalf("replace: %v", err)
	}
	f, _ = clientconfig.Load(path)
	c = f.Contexts["prod"]
	if c.Token != "t3" || c.Server != "" || c.TLS != nil || c.PortForward != nil {
		t.Fatalf("replace should have cleared unset fields: %+v", c)
	}
}

// writeCtxFile writes name under a fresh temp dir and returns its path, for the
// --from-file / --from-file-override cases below.
func writeCtxFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestConfigSetContextFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	// A bare Context document populates every layer of the context, including nested
	// blocks, exactly as the equivalent flags would.
	src := writeCtxFile(t, "ctx.yaml", `server: https://from-file
token: ft
tls:
  ca-cert: /ca.pem
  insecure-skip-verify: true
port-forward:
  namespace: cornus
`)
	if err := (&ConfigSetContextCmd{Name: "prod", FromFile: []string{src}}).Run(cli); err != nil {
		t.Fatalf("set-context --from-file: %v", err)
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["prod"]
	if c == nil || c.Server != "https://from-file" || c.Token != "ft" {
		t.Fatalf("scalars not loaded: %+v", c)
	}
	if c.TLS == nil || c.TLS.CACert != "/ca.pem" || !c.TLS.InsecureSkipVerify {
		t.Fatalf("tls not loaded: %+v", c.TLS)
	}
	if c.PortForward == nil || c.PortForward.Namespace != "cornus" {
		t.Fatalf("port-forward not loaded: %+v", c.PortForward)
	}
}

// TestConfigSetContextFromFileTOML confirms --from-file accepts a TOML bare-context
// document (the loader now dispatches on extension), mapping keys onto the same
// json: field names as YAML/JSON.
func TestConfigSetContextFromFileTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	src := writeCtxFile(t, "ctx.toml", "server = \"https://from-toml\"\nregistry-host = \"reg.toml\"\n\n[tls]\ninsecure-skip-verify = true\n")
	if err := (&ConfigSetContextCmd{Name: "prod", FromFile: []string{src}}).Run(cli); err != nil {
		t.Fatalf("set-context --from-file (toml): %v", err)
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["prod"]
	if c == nil || c.Server != "https://from-toml" || c.RegistryHost != "reg.toml" {
		t.Fatalf("toml scalars not loaded: %+v", c)
	}
	if c.TLS == nil || !c.TLS.InsecureSkipVerify {
		t.Fatalf("toml tls not loaded: %+v", c.TLS)
	}
}

func TestConfigSetContextFromFilePrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	base := writeCtxFile(t, "base.yaml", "server: https://A\ntoken: T\n")

	// --from-file is a base layer: a CLI --server overrides it, while a field the CLI
	// leaves unset (token) is filled from the file.
	if err := (&ConfigSetContextCmd{Name: "prod", Server: "https://B", FromFile: []string{base}}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["prod"]; c.Server != "https://B" || c.Token != "T" {
		t.Fatalf("--from-file precedence wrong: %+v", c)
	}
}

func TestConfigSetContextFromFileOverridePrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	over := writeCtxFile(t, "over.yaml", "server: https://C\n")

	// --from-file-override wins over the CLI --server.
	if err := (&ConfigSetContextCmd{Name: "prod", Server: "https://B", FromFileOverride: []string{over}}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["prod"]; c.Server != "https://C" {
		t.Fatalf("--from-file-override should win: server=%q", c.Server)
	}
}

func TestConfigSetContextFromFileThreeLayers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	base := writeCtxFile(t, "base.yaml", "server: https://A\n")
	over := writeCtxFile(t, "over.yaml", "server: https://C\n")

	// stored < --from-file < CLI < --from-file-override, all on one server field, plus
	// a token that only the CLI sets.
	if err := (&ConfigSetContextCmd{
		Name: "prod", Server: "https://B", Token: "T2",
		FromFile: []string{base}, FromFileOverride: []string{over},
	}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["prod"]; c.Server != "https://C" || c.Token != "T2" {
		t.Fatalf("layering wrong: %+v", c)
	}
}

func TestConfigSetContextFromFileRepeatedMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}
	first := writeCtxFile(t, "first.yaml", "server: https://A\ntoken: T1\n")
	second := writeCtxFile(t, "second.yaml", "server: https://A2\n")

	// Repeated --from-file merges left-to-right: the second file overrides the first's
	// server, but the first's token survives (the second does not set it).
	if err := (&ConfigSetContextCmd{Name: "prod", FromFile: []string{first, second}}).Run(cli); err != nil {
		t.Fatalf("set-context: %v", err)
	}
	f, _ := clientconfig.Load(path)
	if c := f.Contexts["prod"]; c.Server != "https://A2" || c.Token != "T1" {
		t.Fatalf("repeated --from-file merge wrong: %+v", c)
	}
}

func TestConfigSetContextFromFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	// A full-config document (top-level contexts:) is rejected by the strict decode,
	// steering the user to the bare-Context shape.
	full := writeCtxFile(t, "full.yaml", "contexts:\n  prod:\n    server: https://x\n")
	if err := (&ConfigSetContextCmd{Name: "prod", FromFile: []string{full}}).Run(cli); err == nil {
		t.Error("--from-file with a full-config document = nil error, want error")
	}

	// A missing path errors for either flag.
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if err := (&ConfigSetContextCmd{Name: "prod", FromFile: []string{missing}}).Run(cli); err == nil {
		t.Error("--from-file missing path = nil error, want error")
	}
	if err := (&ConfigSetContextCmd{Name: "prod", FromFileOverride: []string{missing}}).Run(cli); err == nil {
		t.Error("--from-file-override missing path = nil error, want error")
	}

	// An invalid SOCKS5 resolve regexp in the file is rejected.
	badRe := writeCtxFile(t, "badre.yaml", "conduit:\n  socks5:\n    resolve:\n    - pattern: \"([bad\"\n      replace: svc:80\n")
	if err := (&ConfigSetContextCmd{Name: "prod", FromFile: []string{badRe}}).Run(cli); err == nil {
		t.Error("--from-file with an invalid resolve pattern = nil error, want error")
	}

	// None of the failing runs should have written a context.
	f, _ := clientconfig.Load(path)
	if _, ok := f.Contexts["prod"]; ok {
		t.Error("a failed set-context wrote a context to the config")
	}
}

func TestConfigViewExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cli := &CLI{Config: path}

	// Seed a context with a token and a nested block.
	if err := (&ConfigSetContextCmd{
		Name: "prod", Server: "https://prod", Token: "sekret", CACert: "/ca.pem",
	}).Run(cli); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// --export writes a bare Context object (no contexts: wrapper) with the real
	// token, selected by the global --context.
	out := filepath.Join(t.TempDir(), "prod.yaml")
	ecli := &CLI{Config: path, Context: "prod"}
	if err := (&ConfigViewCmd{Export: true, OutputFile: out}).Run(ecli); err != nil {
		t.Fatalf("view --export: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "contexts:") {
		t.Errorf("export should not carry the contexts: wrapper:\n%s", got)
	}
	if !strings.Contains(got, "token: sekret") {
		t.Errorf("export should include the real token by default:\n%s", got)
	}
	// The 0600 mode keeps the exported token private.
	if fi, err := os.Stat(out); err != nil {
		t.Fatalf("stat export: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("export file mode = %o, want 600", fi.Mode().Perm())
	}

	// It round-trips: feeding the export back into --from-file reproduces the context.
	if err := (&ConfigSetContextCmd{Name: "prod2", FromFile: []string{out}}).Run(cli); err != nil {
		t.Fatalf("round-trip --from-file: %v", err)
	}
	f, _ := clientconfig.Load(path)
	c := f.Contexts["prod2"]
	if c == nil || c.Server != "https://prod" || c.Token != "sekret" || c.TLS == nil || c.TLS.CACert != "/ca.pem" {
		t.Fatalf("round-trip context mismatch: %+v", c)
	}

	// --redact strips the token from the export.
	rout := filepath.Join(t.TempDir(), "redacted.yaml")
	if err := (&ConfigViewCmd{Export: true, Redact: true, OutputFile: rout}).Run(ecli); err != nil {
		t.Fatalf("view --export --redact: %v", err)
	}
	rdata, _ := os.ReadFile(rout)
	if strings.Contains(string(rdata), "sekret") || !strings.Contains(string(rdata), "REDACTED") {
		t.Errorf("--redact should hide the token:\n%s", rdata)
	}

	// --export with no --context and no current context is an error.
	if err := (&ConfigViewCmd{Export: true}).Run(&CLI{Config: path}); err == nil {
		t.Error("--export with no selectable context = nil error, want error")
	}
}
