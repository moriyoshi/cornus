package caretaker

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// waitDial polls addr until it accepts a connection or the deadline passes, so the
// test does not race the goroutine that binds the listener.
func waitDial(t *testing.T, network, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout(network, addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("endpoint %s://%s never came up", network, addr)
}

// freePort returns a currently-free loopback TCP address.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// getBody issues an HTTP GET against the client and returns the trimmed body. The
// caller supplies the transport (TCP uses the default; unix uses a socket dialer).
func getBody(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestRunDockerTCP(t *testing.T) {
	addr := freePort(t)
	d := DockerRole{Server: "http://127.0.0.1:1", TCPAddr: addr} // server is never dialed by /_ping

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- runDocker(ctx, d, nil) }()
	waitDial(t, "tcp", addr)

	// /_ping is served statically by the dockerproxy — proves the endpoint is wired
	// end to end without a live cornus server behind it.
	if err := dockerReady(d); err != nil {
		t.Fatalf("dockerReady: %v", err)
	}
	code, body := getBody(t, http.DefaultClient, "http://"+addr+"/_ping")
	if code != http.StatusOK || body != "OK" {
		t.Fatalf("_ping = %d %q, want 200 OK", code, body)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("runDocker returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runDocker did not shut down after ctx cancel")
	}
}

func TestRunDockerUnix(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	d := DockerRole{Server: "http://127.0.0.1:1", UnixPath: sock}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- runDocker(ctx, d, nil) }()
	waitDial(t, "unix", sock)

	if err := dockerReady(d); err != nil {
		t.Fatalf("dockerReady: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	code, body := getBody(t, client, "http://docker/_ping")
	if code != http.StatusOK || body != "OK" {
		t.Fatalf("_ping = %d %q, want 200 OK", code, body)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("runDocker returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runDocker did not shut down after ctx cancel")
	}
}

func TestRunDockerNoEndpoint(t *testing.T) {
	if err := runDocker(context.Background(), DockerRole{Server: "http://x"}, nil); err == nil {
		t.Fatal("expected an error when neither tcpAddr nor unixPath is set")
	}
}

func TestDockerReadyNotLive(t *testing.T) {
	if err := dockerReady(DockerRole{TCPAddr: freePort(t)}); err == nil {
		t.Fatal("expected dockerReady to fail for an unbound address")
	}
}

func TestDockerClientTokenEnvOverride(t *testing.T) {
	t.Setenv("CORNUS_DOCKER_CLIENT_TOKEN", "from-env")
	if got := (DockerRole{Token: "from-config"}).dockerClientToken(); got != "from-env" {
		t.Fatalf("dockerClientToken = %q, want from-env (the Secret env wins)", got)
	}
	t.Setenv("CORNUS_DOCKER_CLIENT_TOKEN", "")
	if got := (DockerRole{Token: "from-config"}).dockerClientToken(); got != "from-config" {
		t.Fatalf("dockerClientToken = %q, want from-config (fallback)", got)
	}
}

// TestDockerRoleReadyThroughConfig exercises the Ready() dispatch for the docker
// role via the public Config surface (what caretaker-check runs).
func TestDockerRoleReadyThroughConfig(t *testing.T) {
	addr := freePort(t)
	cfg := Config{Docker: &DockerRole{Server: "http://x", TCPAddr: addr}}
	if err := Ready(cfg); err == nil {
		t.Fatal("Ready should fail before the endpoint is bound")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := Ready(cfg); err != nil {
		t.Fatalf("Ready should pass once bound: %v", err)
	}
}
