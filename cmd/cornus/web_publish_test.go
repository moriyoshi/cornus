package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cornus/cmd/cornus/internal/clientagent"
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/pkg/clientconduit"
	"cornus/pkg/clientconfig"
)

// publishTestConn is a minimal resolved connection: no profile, so ConduitConfig
// reflects only the override, which is exactly the forcing behavior under test.
func publishTestConn(profile *clientconfig.Conduit) *clientconn.Conn {
	return &clientconn.Conn{Endpoint: "http://fake:5000", Config: clientconn.Config{Conduit: profile}}
}

// publishReq is the common call: publishRequest needs a ctx only for the ingress
// resolution, which reaches the server solely in native mode (never in these
// profile-less tests).
func publishReq(t *testing.T, c *WebCmd, cn *clientconn.Conn) clientagent.Request {
	t.Helper()
	req, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, cn)
	if err != nil {
		t.Fatalf("publishRequest: %v", err)
	}
	return req
}

func TestPublishRequestForcesSocks5(t *testing.T) {
	// A port-forward profile (and no --conduit) must still resolve to socks5:
	// --publish-in-conduit forces it.
	c := &WebCmd{Publish: true, PublishPort: 80}
	req := publishReq(t, c, publishTestConn(&clientconfig.Conduit{Mode: clientconduit.ModePortForward}))
	if req.Conduit.Mode != clientconduit.ModeSocks5 {
		t.Fatalf("conduit mode = %q, want socks5 (forced)", req.Conduit.Mode)
	}
	if req.Conduit.Socks5SessionLocal {
		t.Fatal("published UI must join the SHARED proxy, but SessionLocal is set")
	}
}

func TestPublishRequestRejectsPortForwardConduit(t *testing.T) {
	// An explicit contradiction errors client-side, before any agent contact.
	c := &WebCmd{Publish: true, PublishPort: 80, Conduit: "port-forward"}
	if _, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, publishTestConn(nil)); err == nil {
		t.Fatal("--publish-in-conduit --conduit port-forward should error")
	}
}

func TestPublishRequestRejectsExplicitAddr(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80, Addr: "127.0.0.1:9999"}
	if _, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, publishTestConn(nil)); err == nil {
		t.Fatal("--addr with --publish-in-conduit should error")
	}
	// The default addr is not "explicit", so it must be accepted.
	c = &WebCmd{Publish: true, PublishPort: 80, Addr: defaultWebAddr}
	if _, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, publishTestConn(nil)); err != nil {
		t.Fatalf("default addr should be accepted: %v", err)
	}
}

// The name is no longer decided here. Deriving it client-side is only correct when
// the client also chose the conduit, which JoinConduit is precisely the end of: the
// suffix that names the apex belongs to the conduit the AGENT adopts. Sending a
// guess would produce a name the proxy resolves (router locals beat every rule) and
// the BFF's Host check then refuses with 421.
//
// The suffix -> apex rule itself is still pinned, in the one place that now applies
// it: TestDefaultPublishedNameFromAdoptedConduit in package clientagent.
func TestPublishRequestLeavesTheNameToTheAgent(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80}
	if got := publishReq(t, c, publishTestConn(nil)).Web.Name; got != "" {
		t.Fatalf("Web.Name = %q, want empty (the agent derives the default)", got)
	}
	// Even a pinned conduit whose suffix the client DOES know is left to the agent,
	// so there is one derivation site rather than two that can disagree.
	c = &WebCmd{Publish: true, PublishPort: 80, Conduit: "socks5://127.0.0.1:1080?suffix=.demo.internal"}
	if got := publishReq(t, c, publishTestConn(nil)).Web.Name; got != "" {
		t.Fatalf("Web.Name = %q with a pinned suffix, want empty", got)
	}
	// --publish-name still wins outright.
	c = &WebCmd{Publish: true, PublishPort: 80, PublishName: "ui.example"}
	req := publishReq(t, c, publishTestConn(nil))
	if req.Web.Name != "ui.example" {
		t.Fatalf("Web.Name = %q, want ui.example", req.Web.Name)
	}
	if !req.Web.JoinConduit {
		t.Fatal("--publish-name should not turn off joining: it names the UI, not the conduit")
	}
}

// The join/pin rule. These assert a WIRE FIELD, so on their own they would pass
// against an agent that ignored it entirely — TestWebServeJoinsExistingConduit and
// TestWebServeWithoutJoinStartsItsOwn (package clientagent) are the other half of
// the pair, and neither half is sufficient alone.
func TestPublishRequestJoinPinRule(t *testing.T) {
	for _, tc := range []struct {
		conduit  string
		wantJoin bool
		why      string
	}{
		{"", true, "no flag at all: nothing was named"},
		{"socks5", true, "a bare mode word names no settings (the mode is forced anyway)"},
		{"socks5://", true, "no authority and no query: nothing was named"},
		{"socks5h://", true, "socks5h is a scheme synonym, and names nothing either"},
		{"socks5://127.0.0.1:1085", false, "an address was named"},
		{"socks5://.shared:1085", false, "a shared-proxy address was named"},
		{"socks5://?suffix=.x.internal", false, "a suffix was named"},
		{"socks5://127.0.0.1:1085?suffix=.x.internal", false, "both were named"},
	} {
		c := &WebCmd{Publish: true, PublishPort: 80, Conduit: tc.conduit}
		got := publishReq(t, c, publishTestConn(nil)).Web.JoinConduit
		if got != tc.wantJoin {
			t.Errorf("--conduit %q: JoinConduit = %v, want %v (%s)", tc.conduit, got, tc.wantJoin, tc.why)
		}
	}
}

// The profile's ingress settings must ride along. They are what `compose up -d`
// puts in ITS conduit config, and a conduit this command creates without them is
// exactly the one a later `compose up -d` cannot share — the fork this whole change
// is about, in the one order joining cannot fix.
func TestPublishRequestCarriesProfileIngress(t *testing.T) {
	profile := &clientconfig.Conduit{
		Mode:    clientconduit.ModeSocks5,
		Ingress: &clientconfig.Ingress{Mode: "emulate"},
	}
	req := publishReq(t, &WebCmd{Publish: true, PublishPort: 80}, publishTestConn(profile))
	if req.Conduit.Ingress == nil {
		t.Fatal("the profile's ingress settings were dropped; the conduit this creates cannot be shared with `compose up -d`")
	}
	if got := string(req.Conduit.Ingress.Mode); got != "emulate" {
		t.Fatalf("ingress mode = %q, want emulate", got)
	}
}

func TestPublishRequestSendsAbsolutePaths(t *testing.T) {
	// Relative --file / --env-file must be sent absolute (the agent's cwd differs).
	c := &WebCmd{Publish: true, PublishPort: 80, Files: []string{"compose.yaml"}, EnvFile: []string{".env"}}
	req, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, publishTestConn(nil))
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
	if _, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, publishTestConn(nil)); err == nil {
		t.Fatal("--publish-port 70000 should error")
	}
}

// TestPublishRequestSendsLocalRootsAbsolute is the companion of the --file case
// above, for the same reason and with the same failure mode: the agent that hosts
// a published UI is env-frozen at spawn, so its working directory is not the
// caller's. A relative --local-root that survived to the wire would name a real,
// different directory over there — no error, no clue, just the wrong files.
//
// The label and :ro have to travel too. A root arriving unlabelled shows up in the
// switcher under a name the operator did not choose, and one arriving writable is
// a declared refusal quietly dropped.
func TestPublishRequestSendsLocalRootsAbsolute(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "scratch")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	c := &WebCmd{Publish: true, PublishPort: 80, LocalRoot: []string{"notes=scratch:ro"}}
	req, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, publishTestConn(nil))
	if err != nil {
		t.Fatalf("publishRequest: %v", err)
	}
	if len(req.Web.LocalRoots) != 1 {
		t.Fatalf("localRoots = %v, want one", req.Web.LocalRoots)
	}
	got := req.Web.LocalRoots[0]
	if !filepath.IsAbs(got.Path) {
		t.Errorf("Path = %q, want absolute", got.Path)
	}
	wantReal, _ := filepath.EvalSymlinks(sub)
	gotReal, _ := filepath.EvalSymlinks(got.Path)
	if gotReal != wantReal {
		t.Errorf("Path = %q (resolves to %q), want %q", got.Path, gotReal, wantReal)
	}
	if got.Label != "notes" || !got.ReadOnly {
		t.Errorf("root = %+v, want label %q and readOnly", got, "notes")
	}
}

// TestPublishRequestRejectsUnusableLocalRoot: the validation that makes --local-root
// fail at the command rather than on the first listing has to hold on THIS path
// too. The published UI is hosted by another process, so a bad root there would
// surface as a broken switcher entry in a browser, one process removed from the
// person who typed it.
func TestPublishRequestRejectsUnusableLocalRoot(t *testing.T) {
	c := &WebCmd{Publish: true, PublishPort: 80, LocalRoot: []string{filepath.Join(t.TempDir(), "nope")}}
	if _, err := c.publishRequest(context.Background(), &clientconn.Resolver{}, publishTestConn(nil)); err == nil {
		t.Fatal("a --local-root that does not exist should error before the agent is contacted")
	}
}
