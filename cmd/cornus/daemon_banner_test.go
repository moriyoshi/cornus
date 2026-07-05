package main

import (
	"bytes"
	"strings"
	"testing"

	"cornus/cmd/cornus/internal/clientagent"
	"cornus/cmd/cornus/internal/clientconn"
	"cornus/cmd/cornus/internal/cliout"
)

// TestDaemonDockerPrintsTheAgentsBanners drives `cornus daemon docker` end to end
// against a fake background agent and asserts what the USER sees.
//
// The banner it guards is load-bearing: the conduit banner names the SOCKS5 proxy
// address, and this is the only place a session-local proxy's address is ever
// printed — the agent binds it on a port the caller did not choose. Dropping the
// loop left a `daemon docker` session with a working proxy nobody could address,
// while `compose up` and `cornus web` both printed the same lines.
//
// The previous coverage proved only that the agent RETURNS `Response.Banners`,
// which stays true with the CLI print loop deleted. That is the gap the
// attestation audit named, and closing it needed a seam (agentEnsureRunning /
// agentSend) rather than a cleverer assertion — the print was unobservable, not
// merely untested.
//
// --daemon is set so Run registers and returns instead of blocking on a signal.
func TestDaemonDockerPrintsTheAgentsBanners(t *testing.T) {
	const (
		banner1 = "socks5 proxy listening on 127.0.0.1:41son"
		banner2 = "  service hosts resolve under .cornus.internal"
		sock    = "/run/user/1000/cornus-docker-test.sock"
	)

	t.Setenv("HOME", t.TempDir())
	var gotReq clientagent.Request
	agentEnsureRunning = func() (string, error) { return "/tmp/fake-agent.sock", nil }
	agentSend = func(socket string, req clientagent.Request) (*clientagent.Response, error) {
		gotReq = req
		return &clientagent.Response{OK: true, Banners: []string{banner1, banner2}}, nil
	}
	t.Cleanup(func() {
		agentEnsureRunning = clientagent.EnsureRunning
		agentSend = clientagent.Send
	})

	var out bytes.Buffer
	d := cliout.New(cliout.Options{Stdout: &out, Stderr: &out, Output: "plain"})
	c := &DockerProxyCmd{Host: "http://127.0.0.1:65534", Socket: sock, Daemon: true}

	if err := c.Run(&clientconn.Resolver{}, d); err != nil {
		t.Fatalf("Run: %v", err)
	}

	printed := out.String()
	// Positive control: the request actually reached the (fake) agent, so a Run
	// that returned early without registering cannot pass the assertions below.
	if gotReq.Action != "docker-serve" {
		t.Fatalf("agent received action %q, want docker-serve; Run did not get as far as registering, "+
			"so the output below describes nothing", gotReq.Action)
	}

	for _, b := range []string{banner1, banner2} {
		if !strings.Contains(printed, b) {
			t.Errorf("the agent's banner %q was not printed. It is the only place the session-local "+
				"SOCKS5 proxy address is shown, so dropping it leaves a working proxy nobody can "+
				"address.\ngot:\n%s", b, printed)
		}
	}
	// The socket guidance must survive too — it is what makes the frontend usable.
	if !strings.Contains(printed, "DOCKER_HOST=unix://"+sock) {
		t.Errorf("the DOCKER_HOST guidance naming %s was not printed.\ngot:\n%s", sock, printed)
	}
	// Order matters to a reader: the proxy address belongs above the export line,
	// not after it.
	if i, j := strings.Index(printed, banner1), strings.Index(printed, "DOCKER_HOST=unix://"); i >= 0 && j >= 0 && i > j {
		t.Errorf("the conduit banner is printed AFTER the DOCKER_HOST line; a reader who stops at the "+
			"export line never sees the proxy address.\ngot:\n%s", printed)
	}
}

// TestDaemonDockerSurfacesAnAgentRefusal pins the other half of the response
// contract: an agent that answers OK=false must fail the command with its reason,
// not print a success banner and return zero.
func TestDaemonDockerSurfacesAnAgentRefusal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	agentEnsureRunning = func() (string, error) { return "/tmp/fake-agent.sock", nil }
	agentSend = func(string, clientagent.Request) (*clientagent.Response, error) {
		return &clientagent.Response{OK: false, Error: "socket already in use"}, nil
	}
	t.Cleanup(func() {
		agentEnsureRunning = clientagent.EnsureRunning
		agentSend = clientagent.Send
	})

	var out bytes.Buffer
	d := cliout.New(cliout.Options{Stdout: &out, Stderr: &out, Output: "plain"})
	c := &DockerProxyCmd{Host: "http://127.0.0.1:65534", Socket: "/tmp/x.sock", Daemon: true}

	err := c.Run(&clientconn.Resolver{}, d)
	if err == nil {
		t.Fatal("Run returned nil for an agent refusal; the caller would exit 0 with no frontend hosted")
	}
	if !strings.Contains(err.Error(), "socket already in use") {
		t.Errorf("error = %v, want the agent's reason", err)
	}
	if strings.Contains(out.String(), "DOCKER_HOST=unix://") {
		t.Errorf("printed the usage guidance for a frontend that was refused:\n%s", out.String())
	}
}
