package dockerhost

// The rootless refusal replaces a timeout with a diagnosis.
//
// Without it, ForwardPort against a rootless podman daemon dials an address that
// is real inside the workload's user namespace and meaningless outside it. The
// dial cannot succeed, so the caller waits out the full deadline and is told the
// workload is unreachable — which reads as "the application is down" and sends
// the operator to look in exactly the wrong place.
//
// These assert the CODE PATH (an error returned before any dial) rather than the
// wording, per this project's rule about testing behaviour instead of message
// text. The one thing asserted about the message is that it names the variable
// that fixes it, because a refusal without a remedy is its own dead end.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rootlessPodmanBackend builds a Backend whose fake daemon reports rootless.
func rootlessPodmanBackend(t *testing.T, rootless bool, opts ...Option) *Backend {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == libpodPingPath:
			w.Header().Set(libpodVersionHeader, "5.8.2")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/libpod/info"):
			w.WriteHeader(http.StatusOK)
			if rootless {
				io.WriteString(w, `{"host":{"security":{"rootless":true}}}`)
			} else {
				io.WriteString(w, `{"host":{"security":{"rootless":false}}}`)
			}
		default:
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)

	eng, err := newPodmanEngine(context.Background(), endpointFor(t, srv))
	if err != nil {
		t.Fatalf("newPodmanEngine: %v", err)
	}
	all := append([]Option{WithFlavor(FlavorPodman), WithEngine(eng)}, opts...)
	b, err := New(all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestForwardPortRefusesOnRootlessPodman(t *testing.T) {
	b := rootlessPodmanBackend(t, true)

	start := time.Now()
	err := b.ForwardPort(context.Background(), "web", 80, "tcp", nil)
	if err == nil {
		t.Fatal("ForwardPort succeeded against a rootless podman daemon; the workload's netns is not routable from here")
	}
	// The refusal is a PRECONDITION: it must come back immediately, not after a
	// dial deadline. Anything slow means the check did not run and the request
	// reached the network.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("ForwardPort took %s to refuse; the rootless check runs before dialing, so this "+
			"means it fell through to the dial", elapsed)
	}
	if !strings.Contains(err.Error(), "CORNUS_PODMAN_REMOTE") {
		t.Errorf("refusal does not name the variable that fixes it: %v", err)
	}
	// The remedy has prerequisites of its own; naming only the first sends the
	// operator to a second dead end.
	for _, want := range []string{"CORNUS_AGENT_IMAGE", "CORNUS_ADVERTISE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should also name %q, which remote mode needs: %v", want, err)
		}
	}
}

// TestForwardPortDoesNotRefuseOnRootfulPodman: the refusal must be scoped to the
// topology that cannot work. A rootful daemon's containers ARE routable.
func TestForwardPortDoesNotRefuseOnRootfulPodman(t *testing.T) {
	b := rootlessPodmanBackend(t, false)
	err := b.ForwardPort(context.Background(), "web", 80, "tcp", nil)
	if err != nil && strings.Contains(err.Error(), "CORNUS_PODMAN_REMOTE") {
		t.Errorf("ForwardPort refused on a ROOTFUL daemon with the rootless message: %v", err)
	}
}

// TestForwardPortDoesNotRefuseInRemoteMode: remote mode is the remedy, so it
// must not be blocked by the check that recommends it.
func TestForwardPortDoesNotRefuseInRemoteMode(t *testing.T) {
	b := rootlessPodmanBackend(t, true, WithRemote(true))
	err := b.ForwardPort(context.Background(), "web", 80, "tcp", nil)
	if err != nil && strings.Contains(err.Error(), "this podman daemon is rootless") {
		t.Errorf("ForwardPort refused in remote mode, which is the very path that works: %v", err)
	}
}

// TestRootlessIsFalseForTheDockerFlavor: the probe must not run at all for
// Docker, which has no /libpod/info to ask.
func TestRootlessIsFalseForTheDockerFlavor(t *testing.T) {
	b := &Backend{} // zero flavor == dockerhost
	if b.rootless(context.Background()) {
		t.Error("rootless() = true for the docker flavor; the question is podman-only")
	}
}

// TestForwardPortAllowsCoResidentCornusOnRootless is the case an earlier version
// of this logic got WRONG, and the reason the refusal is gated on
// selfNetworkScope rather than on rootlessness alone.
//
// A cornus that is itself a container on this same podman joins each workload's
// network at Apply time and can dial it directly — measured on Podman 5.8.2:
// from the rootless host netns the workload times out, from a container on its
// network it is reachable. Refusing here would turn away a forward that works,
// and push the operator toward a companion they do not need.
func TestForwardPortAllowsCoResidentCornusOnRootless(t *testing.T) {
	b := rootlessPodmanBackend(t, true)
	// The three conditions selfNetworkScope requires: a netns of our own, not
	// remote, and a CONFIRMED id on this daemon (pinned here, as the server does
	// after its own preflight).
	WithIsolatedNetwork(true)(b)
	WithSelfContainerID("cornus-self-abc")(b)

	err := b.ForwardPort(context.Background(), "web", 80, "tcp", nil)
	if err != nil && strings.Contains(err.Error(), "not routable from this host") {
		t.Errorf("ForwardPort refused for a cornus co-resident on the same rootless podman: %v\n"+
			"such a cornus joins the workload's network and CAN dial it; refusing sends the "+
			"operator to a companion they do not need", err)
	}
}

// TestForwardPortStillRefusesWithoutAConfirmedSelfID: "containerized" alone is
// not enough. Without a confirmed id on THIS daemon there is nothing to attach
// to the workload's network, so the route does not exist and the refusal stands.
//
// The confirmation matters in its own right: a GUESSED id would attach some
// unrelated container to the workload's network.
func TestForwardPortStillRefusesWithoutAConfirmedSelfID(t *testing.T) {
	b := rootlessPodmanBackend(t, true)
	WithIsolatedNetwork(true)(b) // containerized...
	WithSelfContainerID("")(b)   // ...but no confirmed identity on this daemon

	err := b.ForwardPort(context.Background(), "web", 80, "tcp", nil)
	if err == nil || !strings.Contains(err.Error(), "not routable from this host") {
		t.Errorf("ForwardPort = %v; want the rootless refusal — being containerized without a "+
			"confirmed id gives no route to the workload", err)
	}
}
