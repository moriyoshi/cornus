//go:build linux

package incushost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/hostpolicy"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// remoteBackend returns a backend in remote mode with the configuration remote
// mode requires already in place.
func remoteBackend(t *testing.T, f *fakeConn) *Backend {
	t.Helper()
	t.Setenv("CORNUS_ADVERTISE_URL", "http://cornus.example:5000")
	return &Backend{
		conn:       f,
		policy:     hostpolicy.Permissive(),
		project:    "default",
		pool:       "default",
		execs:      newExecRegistry(),
		remote:     true,
		agentImage: "localhost:5000/cornus:latest",
		companions: remotecompanion.NewRegistry(),
	}
}

// addressed gives every instance the fake creates a global IPv4, so the
// companion's address wait resolves the way a working bridge would.
func addressed(f *fakeConn, id, ip string) {
	f.states[id] = &incusapi.InstanceState{
		Status:     "Running",
		StatusCode: incusapi.Running,
		Network: map[string]incusapi.InstanceStateNetwork{
			"lo":   {Addresses: []incusapi.InstanceStateNetworkAddress{{Family: "inet", Scope: "local", Address: "127.0.0.1"}}},
			"eth0": {Addresses: []incusapi.InstanceStateNetworkAddress{{Family: "inet", Scope: "global", Address: ip}}},
		},
	}
}

// applyRemote runs a one-replica remote-mode Apply with the replica addressed,
// returning the backend and fake for assertions.
func applyRemote(t *testing.T, replicas int) (*Backend, *fakeConn) {
	t.Helper()
	f := newFakeConn()
	b := remoteBackend(t, f)
	// The address wait polls the seam, so seeding the state up front lets the
	// first poll succeed; the fake creates the instance during Apply.
	for i := 0; i < replicas; i++ {
		addressed(f, instanceName("web", i), "10.42.0."+strconv.Itoa(10+i))
	}
	if _, err := b.Apply(context.Background(), api.DeploySpec{
		Name: "web", Image: "localhost:5000/app:v1", Replicas: replicas,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return b, f
}

// TestRemoteApplyCreatesACompanionPerReplica proves remote mode realizes
// deploy.RemoteCapable rather than merely recording the operator's intent: every
// replica gets its own companion instance and its own shared agent volume.
func TestRemoteApplyCreatesACompanionPerReplica(t *testing.T) {
	_, f := applyRemote(t, 2)

	for i := 0; i < 2; i++ {
		comp, ok := f.insts[companionName("web", i)]
		if !ok {
			t.Fatalf("no companion instance for replica %d; have %v", i, instanceNames(f))
		}
		if !isCompanion(*comp) {
			t.Errorf("companion %s is not stamped with a role, so instance listing cannot tell it from a replica", comp.Name)
		}
		if got := comp.Config[configKeyPrefix+deploy.LabelApp]; got != "web" {
			t.Errorf("companion app label = %q, want web (delete must reap it with the app)", got)
		}
		vol := "default/" + agentVolumeName("web", i)
		if cfg, ok := f.volumes[vol]; !ok {
			t.Errorf("no agent volume %s; have %v", vol, volumeNames(f))
		} else if cfg["security.shifted"] != "true" {
			t.Errorf("agent volume %s missing security.shifted, so two unprivileged instances cannot both mount it", vol)
		}
	}

	// A replica's own volume, never a shared one: a forwarded agent must not be
	// reachable from a different replica.
	if a, b := agentVolumeName("web", 0), agentVolumeName("web", 1); a == b {
		t.Fatalf("replicas share the agent volume name %q", a)
	}
}

// TestRemoteApplyMountsTheAgentVolumeInBothInstances pins the mechanism that
// makes ssh-agent forwarding work at all: the socket the companion binds is only
// reachable from the workload because both instances mount the same volume at
// the same path (remotecompanion.AgentSocketPath's directory).
func TestRemoteApplyMountsTheAgentVolumeInBothInstances(t *testing.T) {
	_, f := applyRemote(t, 1)

	for _, name := range []string{instanceName("web", 0), companionName("web", 0)} {
		in, ok := f.insts[name]
		if !ok {
			t.Fatalf("instance %s was not created", name)
		}
		dev, ok := in.Devices[agentVolumeDevice]
		if !ok {
			t.Fatalf("%s has no %s device; devices = %v", name, agentVolumeDevice, in.Devices)
		}
		if dev["type"] != "disk" || dev["pool"] != "default" || dev["source"] != agentVolumeName("web", 0) {
			t.Errorf("%s agent device = %v, want a disk on the replica's agent volume", name, dev)
		}
		if dev["path"] != remotecompanion.AgentScratchDir {
			t.Errorf("%s mounts the agent volume at %q, want %q — the path SSH_AUTH_SOCK points into",
				name, dev["path"], remotecompanion.AgentScratchDir)
		}
	}
}

// TestNonRemoteApplyCreatesNoCompanionOrVolume pins that the companion is
// strictly opt-in: an ordinary deploy is byte-for-byte what it was before, with
// no extra instance to pay for and no volume to reap.
func TestNonRemoteApplyCreatesNoCompanionOrVolume(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	if _, ok := f.insts[companionName("web", 0)]; ok {
		t.Error("a non-remote deploy created a companion")
	}
	if len(f.volumes) != 0 {
		t.Errorf("a non-remote deploy created volumes: %v", volumeNames(f))
	}
	if in := f.insts[instanceName("web", 0)]; in.Devices[agentVolumeDevice] != nil {
		t.Error("a non-remote replica mounts an agent volume that does not exist")
	}
}

// TestCompanionCaretakerConfigCarriesBothRoles decodes the config the companion
// actually runs on. It is the wire contract between this backend and
// pkg/caretaker, so it is asserted as caretaker.Config rather than as a string.
func TestCompanionCaretakerConfigCarriesBothRoles(t *testing.T) {
	_, f := applyRemote(t, 1)
	comp := f.insts[companionName("web", 0)]

	raw, ok := comp.Config["environment.CORNUS_CARETAKER_CONFIG"]
	if !ok {
		t.Fatalf("companion carries no caretaker config; config = %v", comp.Config)
	}
	var cfg caretaker.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("caretaker config is not valid JSON: %v", err)
	}

	if cfg.Instance != remotecompanion.InstanceKey("web", 0) {
		t.Errorf("Instance = %q, want %q — the server looks the companion up by this key",
			cfg.Instance, remotecompanion.InstanceKey("web", 0))
	}
	if cfg.PortForward == nil {
		t.Fatal("no PortForward role, so ForwardPort has nothing to reroute through")
	}
	if cfg.PortForward.Server != "http://cornus.example:5000" {
		t.Errorf("PortForward.Server = %q, want the advertise URL", cfg.PortForward.Server)
	}
	// The decisive one: an Incus companion is a SIBLING instance, so a loopback
	// dial would reach the companion itself rather than the workload.
	if cfg.PortForward.Host != "10.42.0.10" {
		t.Errorf("PortForward.Host = %q, want the replica's address 10.42.0.10", cfg.PortForward.Host)
	}
	if cfg.AgentRelay == nil {
		t.Fatal("no AgentRelay role, but AgentForwardEnabled promises one in remote mode")
	}
	if cfg.AgentRelay.SocketPath != remotecompanion.AgentSocketPath {
		t.Errorf("AgentRelay.SocketPath = %q, want %q — the SSH_AUTH_SOCK the server injects",
			cfg.AgentRelay.SocketPath, remotecompanion.AgentSocketPath)
	}
	// Mount/egress roles are deliberately absent: a sibling instance's 9P mount
	// cannot propagate into the workload's mount namespace.
	if len(cfg.Mounts) != 0 || cfg.Egress != nil {
		t.Errorf("companion carries mount/egress roles it cannot realize: %+v", cfg)
	}
}

// TestCompanionConfigCarriesTheCaretakerToken pins that an authenticated server
// hands the companion the credential it needs for its dial back; without it the
// companion never registers and remote mode silently does nothing.
func TestCompanionConfigCarriesTheCaretakerToken(t *testing.T) {
	t.Setenv("CORNUS_CARETAKER_TOKEN", "s3cret")
	cfg := companionConfig("web", 0, "http://cornus:5000", "10.0.0.2")
	if cfg.Token != "s3cret" {
		t.Errorf("Token = %q, want the CORNUS_CARETAKER_TOKEN value", cfg.Token)
	}
}

// TestCompanionRunsTheCaretakerNotAServer pins the entrypoint override. The
// cornus image's own entrypoint is `cornus`, which would start a SERVER inside
// the companion; oci.entrypoint is the Incus key that replaces it.
func TestCompanionRunsTheCaretakerNotAServer(t *testing.T) {
	_, f := applyRemote(t, 1)
	comp := f.insts[companionName("web", 0)]
	if got, want := comp.Config["oci.entrypoint"], ociEntrypoint([]string{"cornus", "caretaker"}); got != want {
		t.Errorf("oci.entrypoint = %q, want %q", got, want)
	}
	if !strings.Contains(comp.Config["oci.entrypoint"], "caretaker") {
		t.Errorf("companion entrypoint %q does not run the caretaker", comp.Config["oci.entrypoint"])
	}
	if comp.Config["boot.autorestart"] != "true" {
		t.Error("companion is not set to restart, so it would not come back with the app")
	}
}

// TestOciEntrypointQuotesEveryWord pins the encoding Incus's shell-word splitter
// consumes. Quoting matters because an unquoted word with a shell metacharacter
// would be re-split by incusd into a different argv than cornus intended.
func TestOciEntrypointQuotesEveryWord(t *testing.T) {
	if got, want := ociEntrypoint([]string{"cornus", "caretaker"}), `'cornus' 'caretaker'`; got != want {
		t.Errorf("ociEntrypoint = %q, want %q", got, want)
	}
	if got, want := ociEntrypoint([]string{"a b", "c;d"}), `'a b' 'c;d'`; got != want {
		t.Errorf("ociEntrypoint = %q, want %q", got, want)
	}
	if got, want := ociEntrypoint([]string{"it's"}), `'it'\''s'`; got != want {
		t.Errorf("embedded quote: ociEntrypoint = %q, want %q", got, want)
	}
}

// TestRemoteApplyRefusesMissingConfigBeforeTearingAnythingDown pins the ordering
// that protects a running deployment: remote mode's requirements are checked
// before the recreate-on-Apply teardown, so a misconfigured server leaves the
// existing deployment alone instead of deleting it and failing to replace it.
func TestRemoteApplyRefusesMissingConfigBeforeTearingAnythingDown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agent     string
		advertise string
		want      string
	}{
		{"no agent image", "", "http://cornus:5000", "CORNUS_AGENT_IMAGE"},
		{"no advertise url", "localhost:5000/cornus:latest", "", "CORNUS_ADVERTISE_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeConn()
			b := remoteBackend(t, f)
			b.agentImage, b.remote = tc.agent, false
			// Seed a live deployment through a normal (non-remote) Apply, then flip
			// remote mode on for the failing re-apply.
			addressed(f, instanceName("web", 0), "10.42.0.10")
			applyOne(t, b, f, "web")
			b.remote = true
			t.Setenv("CORNUS_ADVERTISE_URL", tc.advertise)

			_, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply error = %v, want one naming %s", err, tc.want)
			}
			if _, ok := f.insts[instanceName("web", 0)]; !ok {
				t.Error("the existing deployment was torn down by an Apply that could never succeed")
			}
		})
	}
}

// TestRemoteApplyFailsWhenTheReplicaNeverGetsAnAddress pins that a companion is
// never created pointing at nothing. Without the address its PortForwardRole has
// no target, so the failure has to surface at Apply rather than as a
// port-forward that silently relays into the void.
func TestRemoteApplyFailsWhenTheReplicaNeverGetsAnAddress(t *testing.T) {
	old := companionAddressTimeout
	companionAddressTimeout = 50 * time.Millisecond
	defer func() { companionAddressTimeout = old }()

	f := newFakeConn()
	b := remoteBackend(t, f)
	// No addressed() call: the instance comes up but never acquires an IPv4.
	_, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"})
	if err == nil || !strings.Contains(err.Error(), "addressed") {
		t.Fatalf("Apply error = %v, want a wait-for-address failure", err)
	}
	if _, ok := f.insts[companionName("web", 0)]; ok {
		t.Error("a companion was created for a replica with no address to relay to")
	}
}

// TestWaitInstanceIPv4FailsFastOnAMissingInstance pins that a vanished instance
// short-circuits the wait: burning the whole timeout on a name that will never
// exist turns a clear ErrNotFound into an opaque deadline.
func TestWaitInstanceIPv4FailsFastOnAMissingInstance(t *testing.T) {
	f := newFakeConn()
	b := remoteBackend(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	_, err := b.waitInstanceIPv4(ctx, "cornus-ghost-0")
	if !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v for an instance that does not exist", elapsed)
	}
}

// TestDeleteReapsCompanionsAndAgentVolumes pins the full teardown: an app's
// companions go with it (a companion left behind would keep answering
// port-forwards for a workload that no longer exists) and the agent volumes go
// after them, since Incus refuses to delete an attached volume.
func TestDeleteReapsCompanionsAndAgentVolumes(t *testing.T) {
	b, f := applyRemote(t, 2)
	if len(f.volumes) != 2 {
		t.Fatalf("setup: %d agent volumes, want 2", len(f.volumes))
	}

	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := len(f.insts); n != 0 {
		t.Errorf("instances left after Delete: %v", instanceNames(f))
	}
	if n := len(f.volumes); n != 0 {
		t.Errorf("agent volumes leaked after Delete: %v", volumeNames(f))
	}
}

// TestDeleteReapsVolumesEvenAfterRemoteModeIsTurnedOff pins that cleanup follows
// what was CREATED, not what the current configuration would create: an operator
// who deploys with remote mode on and then turns it off must not be left with
// orphaned volumes no later delete would ever remove.
func TestDeleteReapsVolumesEvenAfterRemoteModeIsTurnedOff(t *testing.T) {
	b, f := applyRemote(t, 1)
	b.remote = false

	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := len(f.volumes); n != 0 {
		t.Errorf("agent volumes leaked once remote mode was turned off: %v", volumeNames(f))
	}
}

// TestLifecycleActionsIncludeCompanions pins that stop/start/restart move the
// companion with its app. A companion that kept running past a `stop` would
// still be registered and would still accept port-forward streams.
func TestLifecycleActionsIncludeCompanions(t *testing.T) {
	f := newFakeConn()
	b := remoteBackend(t, f)
	addressed(f, instanceName("web", 0), "10.42.0.10")
	rec := &recordingConn{fakeConn: f}
	b.conn = rec
	if _, err := b.Apply(context.Background(), api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	rec.calls = nil
	if err := b.Stop(context.Background(), "web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := map[string]bool{
		"stop " + instanceName("web", 0):  false,
		"stop " + companionName("web", 0): false,
	}
	for _, c := range rec.calls {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for call, seen := range want {
		if !seen {
			t.Errorf("Stop did not issue %q; calls = %v", call, rec.calls)
		}
	}
}

// TestCompanionsAreNotAddressableAsReplicas pins the invariant that keeps every
// replica-indexed operation honest once a second instance per replica exists:
// Status, Logs, Exec and Stats must see the app's replicas only, or `--instance
// 1` on a one-replica deployment would land on its companion.
func TestCompanionsAreNotAddressableAsReplicas(t *testing.T) {
	b, f := applyRemote(t, 1)

	st, err := b.Status(context.Background(), "web")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Instances) != 1 || st.Instances[0].ID != instanceName("web", 0) {
		t.Fatalf("Status reported %d instances (%+v), want just the replica", len(st.Instances), st.Instances)
	}

	list, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || len(list[0].Instances) != 1 {
		t.Fatalf("List reported %+v, want one app with one instance", list)
	}

	// The ordinal seam every log/exec/stat path goes through.
	if _, err := b.instanceAt("web", 1); !errors.Is(err, deploy.ErrNotFound) {
		t.Errorf("instance 1 of a one-replica deployment resolved to %v, want ErrNotFound", err)
	}
	id, err := b.instanceAt("web", 0)
	if err != nil || id != instanceName("web", 0) {
		t.Errorf("instance 0 = (%q, %v), want the replica", id, err)
	}
	_ = f
}

// TestRemoteForwardPortRoutesThroughTheCompanion proves ForwardPort stops
// dialing the instance directly in remote mode and opens a stream on the
// companion's registered connection instead — the whole point of the companion
// for a cornus that has no route to the incus bridge. It mirrors the equivalent
// dockerhost/containerdhost tests, standing in for pkg/caretaker's accept loop.
func TestRemoteForwardPortRoutesThroughTheCompanion(t *testing.T) {
	f := newFakeConn()
	b := remoteBackend(t, f)

	serverSess, clientSess := yamuxPipe(t)
	b.companions.Put(remotecompanion.InstanceKey("web", 0), serverSess)

	appLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer appLn.Close()
	_, portStr, _ := net.SplitHostPort(appLn.Addr().String())
	port, _ := strconv.Atoi(portStr)
	go func() {
		conn, err := appLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()

	// The caretaker side: accept the server-opened stream and dial the port.
	go func() {
		tag, stream, err := wire.AcceptTagged(clientSess)
		if err != nil || tag != wire.TagPortForward {
			return
		}
		defer stream.Close()
		p, err := wire.ReadLine(stream)
		if err != nil {
			return
		}
		if _, err := wire.ReadLine(stream); err != nil { // proto
			return
		}
		upstream, err := net.Dial("tcp", "127.0.0.1:"+p)
		if err != nil {
			return
		}
		defer upstream.Close()
		wire.Pipe(stream, upstream)
	}()

	callerConn, appSideConn := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", port, "tcp", appSideConn) }()

	if _, err := callerConn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	callerConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(callerConn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echoed %q, want %q", buf, "ping")
	}
	callerConn.Close()
	<-done
}

// TestRemoteForwardPortReportsAnUnconnectedCompanion pins a clear error rather
// than a hang or a fallback to a dial that cannot work, for the window between
// creating a companion and its caretaker registering.
func TestRemoteForwardPortReportsAnUnconnectedCompanion(t *testing.T) {
	f := newFakeConn()
	b := remoteBackend(t, f)
	_, appSideConn := net.Pipe()
	defer appSideConn.Close()
	err := b.ForwardPort(context.Background(), "web", 80, "tcp", appSideConn)
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("want a not-connected error, got %v", err)
	}

	b.companions = nil
	if err := b.ForwardPort(context.Background(), "web", 80, "tcp", appSideConn); err == nil {
		t.Error("a remote backend with no companion registry should error")
	}
}

// yamuxPipe returns a connected (server, client) yamux session pair over a real
// loopback connection, standing in for the WebSocket transport a real
// caretaker/server pair uses.
func yamuxPipe(t *testing.T) (serverSess, clientSess *yamux.Session) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	serverConnCh := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			serverConnCh <- c
		}
	}()
	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	serverSess, err = yamux.Server(<-serverConnCh, nil)
	if err != nil {
		t.Fatalf("yamux.Server: %v", err)
	}
	clientSess, err = yamux.Client(clientConn, nil)
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	t.Cleanup(func() { serverSess.Close(); clientSess.Close() })
	return serverSess, clientSess
}

func instanceNames(f *fakeConn) []string {
	var out []string
	for n := range f.insts {
		out = append(out, n)
	}
	return out
}

func volumeNames(f *fakeConn) []string {
	var out []string
	for n := range f.volumes {
		out = append(out, n)
	}
	return out
}

// TestRemoteApplySurfacesEveryCompanionFailure pins that each daemon call the
// companion path adds is reported rather than swallowed. A companion that failed
// to appear must fail the Apply: silently succeeding would leave a deployment
// whose port-forward and agent-forwarding are permanently broken, with nothing
// in the deploy output to say so.
func TestRemoteApplySurfacesEveryCompanionFailure(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"}

	t.Run("volume creation", func(t *testing.T) {
		f := newFakeConn()
		b := remoteBackend(t, f)
		addressed(f, instanceName("web", 0), "10.42.0.10")
		f.volumeErr = errors.New("pool is out of space")
		_, err := b.Apply(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), "agent volume") {
			t.Fatalf("Apply error = %v, want a volume failure", err)
		}
	})

	t.Run("companion creation", func(t *testing.T) {
		f := newFakeConn()
		b := remoteBackend(t, f)
		addressed(f, instanceName("web", 0), "10.42.0.10")
		f.createErrs = map[string]error{companionName("web", 0): errors.New("no such image")}
		_, err := b.Apply(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), "creating companion") {
			t.Fatalf("Apply error = %v, want a companion-create failure", err)
		}
	})

	t.Run("credential resolution", func(t *testing.T) {
		f := newFakeConn()
		b := remoteBackend(t, f)
		addressed(f, instanceName("web", 0), "10.42.0.10")
		// Only the AGENT image's credential fails, so the app instance is created
		// and the failure is provably the companion's own pull credential.
		b.creds = func(_ context.Context, ref string) (deploy.RegistryCredential, bool, error) {
			if ref == b.agentImage {
				return deploy.RegistryCredential{}, false, errors.New("issuer unavailable")
			}
			return deploy.RegistryCredential{}, false, nil
		}
		_, err := b.Apply(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), "resolve registry credential") {
			t.Fatalf("Apply error = %v, want a credential failure", err)
		}
	})

	t.Run("unparseable agent image", func(t *testing.T) {
		f := newFakeConn()
		b := remoteBackend(t, f)
		addressed(f, instanceName("web", 0), "10.42.0.10")
		b.agentImage = "NOT A REF"
		_, err := b.Apply(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), "parsing image reference") {
			t.Fatalf("Apply error = %v, want an image-reference failure", err)
		}
	})
}

// TestDeleteSurfacesAVolumeReapFailure pins that a volume cornus cannot remove
// is an error, not a silent leak: an orphaned volume keeps consuming pool space
// and blocks the next deploy from creating its replacement.
func TestDeleteSurfacesAVolumeReapFailure(t *testing.T) {
	b, f := applyRemote(t, 1)
	f.volumeDeleteErr = errors.New("volume is still attached")
	err := b.Delete(context.Background(), "web")
	if err == nil || !strings.Contains(err.Error(), "deleting agent volume") {
		t.Fatalf("Delete error = %v, want a volume-reap failure", err)
	}
}

// TestRemoteForwardPortBridgesUDP pins that the companion route carries UDP too,
// not just TCP: the datagram framing is a different code path from the byte
// stream, and a port-forward that silently only worked for TCP would look like a
// broken workload.
func TestRemoteForwardPortBridgesUDP(t *testing.T) {
	f := newFakeConn()
	b := remoteBackend(t, f)
	serverSess, clientSess := yamuxPipe(t)
	b.companions.Put(remotecompanion.InstanceKey("web", 0), serverSess)

	// The caretaker side: echo one datagram back.
	go func() {
		tag, stream, err := wire.AcceptTagged(clientSess)
		if err != nil || tag != wire.TagPortForward {
			return
		}
		defer stream.Close()
		if _, err := wire.ReadLine(stream); err != nil { // port
			return
		}
		if _, err := wire.ReadLine(stream); err != nil { // proto
			return
		}
		datagram, err := wire.ReadDatagram(stream)
		if err != nil {
			return
		}
		_ = wire.WriteDatagram(stream, datagram)
	}()

	callerConn, appSideConn := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- b.ForwardPort(context.Background(), "web", 53, "udp", appSideConn) }()

	if err := wire.WriteDatagram(callerConn, []byte("pingu")); err != nil {
		t.Fatalf("write datagram: %v", err)
	}
	callerConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := wire.ReadDatagram(callerConn)
	if err != nil {
		t.Fatalf("read datagram: %v", err)
	}
	if string(got) != "pingu" {
		t.Fatalf("echoed %q, want %q", got, "pingu")
	}
	callerConn.Close()
	<-done
}

// TestConsoleSinkNeverWritesIntoTheWorkload pins the safety property that makes
// a log follow read-only. The Incus console is bidirectional — bytes read from
// the sink are written into PID 1's stdin — so the sink must produce none, and
// must report EOF on cancellation so the mirror tears down instead of hanging.
func TestConsoleSinkNeverWritesIntoTheWorkload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out strings.Builder
	s := &consoleSink{ctx: ctx, w: &out}

	if _, err := s.Write([]byte("from the console")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if out.String() != "from the console" {
		t.Errorf("console output = %q", out.String())
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	read := make(chan error, 1)
	go func() {
		buf := make([]byte, 8)
		n, err := s.Read(buf)
		if n != 0 {
			t.Errorf("the sink produced %d bytes, which would be typed into the workload's console", n)
		}
		read <- err
	}()
	select {
	case err := <-read:
		t.Fatalf("Read returned %v before cancellation; it must block", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-read:
		if !errors.Is(err, io.EOF) {
			t.Errorf("Read after cancel = %v, want io.EOF (what ends the mirror)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not unblock on cancellation, so a follow would never end")
	}
}
