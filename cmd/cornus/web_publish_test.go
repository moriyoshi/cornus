package main

import (
	"path/filepath"
	"testing"

	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
)

// publishTestConn is a minimal resolved connection: no profile, so ConduitConfig
// reflects only the override, which is exactly the forcing behavior under test.
func publishTestConn(profile *clientconfig.Conduit) *clientconn.Conn {
	return &clientconn.Conn{Endpoint: "http://fake:5000", Config: clientconn.Config{Conduit: profile}}
}

func TestPublishRequestForcesSocks5(t *testing.T) {
	// A port-forward profile (and no --conduit) must still resolve to socks5:
	// --publish-in-conduit forces it.
	c := &WebCmd{Publish: true, PublishPort: 80}
	req, name, err := c.publishRequest(&clientconn.Resolver{}, publishTestConn(&clientconfig.Conduit{Mode: clientconduit.ModePortForward}))
	if err != nil {
		t.Fatalf("publishRequest: %v", err)
	}
	if req.Conduit.Mode != clientconduit.ModeSocks5 {
		t.Fatalf("conduit mode = %q, want socks5 (forced)", req.Conduit.Mode)
	}
	if name != "cornus.internal" || req.Web.Name != "cornus.internal" {
		t.Fatalf("name = %q / %q, want cornus.internal", name, req.Web.Name)
	}
	if req.Conduit.Socks5SessionLocal {
		t.Fatal("published UI must join the SHARED proxy, but SessionLocal is set")
	}
}

func TestPublishRequestRejectsPortForwardConduit(t *testing.T) {
	// An explicit contradiction errors client-side, before any agent contact.
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "port-forward"}
	if _, _, err := c.publishRequest(&clientconn.Resolver{}, publishTestConn(nil)); err == nil {
		t.Fatal("--publish-in-conduit --conduit port-forward should error")
	}
}

func TestPublishRequestRejectsExplicitAddr(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80, Addr: "127.0.0.1:9999"}
	if _, _, err := c.publishRequest(&clientconn.Resolver{}, publishTestConn(nil)); err == nil {
		t.Fatal("--addr with --publish-in-conduit should error")
	}
	// The default addr is not "explicit", so it must be accepted.
	c = &WebCmd{Publish: true, PublishPort: 80, Addr: defaultWebAddr}
	if _, _, err := c.publishRequest(&clientconn.Resolver{}, publishTestConn(nil)); err != nil {
		t.Fatalf("default addr should be accepted: %v", err)
	}
}

func TestPublishRequestNameFromSuffix(t *testing.T) {
	// A custom service-host suffix derives the apex name.
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "socks5://127.0.0.1:1080?suffix=.demo.internal"}
	_, name, err := c.publishRequest(&clientconn.Resolver{}, publishTestConn(nil))
	if err != nil {
		t.Fatalf("publishRequest: %v", err)
	}
	if name != "demo.internal" {
		t.Fatalf("name = %q, want demo.internal", name)
	}
}

func TestPublishRequestSendsAbsolutePaths(t *testing.T) {
	// Relative --file / --env-file must be sent absolute (the agent's cwd differs).
	c := &WebCmd{Publish: true, PublishPort: 80, Files: []string{"compose.yaml"}, EnvFile: []string{".env"}}
	req, _, err := c.publishRequest(&clientconn.Resolver{}, publishTestConn(nil))
	if err != nil {
		t.Fatalf("publishRequest: %v", err)
	}
	if len(req.Web.Files) != 1 || !filepath.IsAbs(req.Web.Files[0]) {
		t.Fatalf("files = %v, want one absolute path", req.Web.Files)
	}
	if len(req.Web.EnvFiles) != 1 || !filepath.IsAbs(req.Web.EnvFiles[0]) {
		t.Fatalf("envFiles = %v, want one absolute path", req.Web.EnvFiles)
	}
}

func TestPublishRequestRejectsBadPort(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 70000}
	if _, _, err := c.publishRequest(&clientconn.Resolver{}, publishTestConn(nil)); err == nil {
		t.Fatal("--publish-port 70000 should error")
	}
}
