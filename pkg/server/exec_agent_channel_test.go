package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/remotecompanion"
	"cornus/pkg/wire"
)

// fakeRemoteBackend adds RemoteCapable to fakeBackend so the exec-agent-channel
// endpoint's Remote() gating can be exercised directly. A dedicated wrapper,
// not a Remote() method on fakeBackend itself: fakeBackend is embedded by
// other fakes (e.g. fakeMountingBackend), and adding Remote() there would
// silently make THEM RemoteCapable too via method promotion, changing
// useSidecarMounts's dispatch for tests that never opted into remote mode —
// mirrors the existing fakeRemoteMountingBackend precedent (deploy_attach_test.go).
type fakeRemoteBackend struct {
	fakeBackend
	remote bool
}

func (f *fakeRemoteBackend) Remote() bool { return f.remote }

// TestRelayAgentMuxedFastFailWhenUnregistered proves a caretaker's
// AgentRelayRole stream (TagAgentRelay) is closed immediately when no
// `cornus exec --forward-agent` session has registered an agent channel for
// its instance — the same fast-fail-on-unregistered-session pattern already
// used for credential relay (TestCaretakerMountUnknownSession's sibling).
func TestRelayAgentMuxedFastFailWhenUnregistered(t *testing.T) {
	srv := newTestServer(t, &fakeRemoteBackend{remote: true})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach?instance="+url.QueryEscape("web/0"))
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()

	stream, err := wire.OpenTagged(mux, wire.TagAgentRelay)
	if err != nil {
		t.Fatalf("open agent-relay stream: %v", err)
	}
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("expected the agent-relay stream to be closed when no exec-agent-channel is registered")
	}
}

// TestRelayAgentMuxedRelaysToExecChannel proves a caretaker's agent-relay
// stream is bridged to whichever `cornus exec --forward-agent` channel is
// currently registered for its instance: the client opens the exec-agent-channel
// endpoint (mirroring cmd/cornus/exec.go), the caretaker (playing an
// AgentRelayRole connection) declares instance "web/0" and opens a
// TagAgentRelay stream, and bytes round-trip through the server relay to a
// fake "real local agent" on the client side.
func TestRelayAgentMuxedRelaysToExecChannel(t *testing.T) {
	srv := newTestServer(t, &fakeRemoteBackend{remote: true})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	// The exec CLI's side channel (cl.ExecAgentChannel), registered before any
	// caretaker connects.
	execSess, err := wire.DialControlHeaderTLS(ctx, wsBase+"/.cornus/v1/deploy/web/exec-agent-channel", nil, nil, nil)
	if err != nil {
		t.Fatalf("dial exec-agent-channel: %v", err)
	}
	defer execSess.Close()

	// Fake "real local agent": accept every relayed stream and echo.
	go func() {
		for {
			stream, err := execSess.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				defer stream.Close()
				buf := make([]byte, 4096)
				for {
					n, err := stream.Read(buf)
					if n > 0 {
						if _, werr := stream.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()

	// The caretaker connects, declaring instance "web/0", and relays one local
	// connection's worth of agent-protocol bytes.
	mux, err := wire.Dial(ctx, wsBase+"/.cornus/v1/caretaker/attach?instance="+url.QueryEscape("web/0"))
	if err != nil {
		t.Fatalf("dial caretaker attach: %v", err)
	}
	defer mux.Close()

	stream, err := wire.OpenTagged(mux, wire.TagAgentRelay)
	if err != nil {
		t.Fatalf("open agent-relay stream: %v", err)
	}
	defer stream.Close()

	if _, err := stream.Write([]byte("sign-request")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len("sign-request"))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "sign-request" {
		t.Fatalf("echoed %q, want %q", buf, "sign-request")
	}
}

// TestHandleDeployExecAgentChannelRejectsNonRemote proves the exec-agent-channel
// endpoint refuses to upgrade at all against a non-remote-capable backend.
func TestHandleDeployExecAgentChannelRejectsNonRemote(t *testing.T) {
	srv := newTestServer(t, &fakeRemoteBackend{remote: false})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	if _, err := wire.DialControlHeaderTLS(ctx, wsBase+"/.cornus/v1/deploy/web/exec-agent-channel", nil, nil, nil); err == nil {
		t.Fatal("expected the exec-agent-channel dial to fail against a non-remote backend")
	}
}

// fakeAgentForwardBackend adds deploy.AgentForwardCapable to fakeBackend (a
// dedicated wrapper, not a method on fakeBackend itself — see
// fakeRemoteBackend above for why) so the kubernetes-style per-deployment
// opt-in path (as opposed to fakeRemoteBackend's backend-wide Remote() path)
// can be exercised directly. It also overrides ExecCreate to record the
// ExecConfig it was called with, so a test can confirm the SSH_AUTH_SOCK env
// injection actually happened.
type fakeAgentForwardBackend struct {
	fakeBackend
	enabled     bool
	enabledErr  error
	lastExecCfg api.ExecConfig
}

func (f *fakeAgentForwardBackend) AgentForwardEnabled(_ context.Context, _ string) (bool, error) {
	return f.enabled, f.enabledErr
}

func (f *fakeAgentForwardBackend) ExecCreate(_ context.Context, name string, cfg api.ExecConfig) (string, error) {
	f.lastExecCfg = cfg
	return "exec-" + name, nil
}

// TestHandleDeployExecCreateAgentForwardCapableAllowed proves ForwardAgent is
// accepted (and SSH_AUTH_SOCK injected) against a backend that is not
// RemoteCapable but reports AgentForwardEnabled==true for the target
// deployment — the kubernetes opt-in path.
func TestHandleDeployExecCreateAgentForwardCapableAllowed(t *testing.T) {
	fb := &fakeAgentForwardBackend{enabled: true}
	srv := newTestServer(t, fb)
	defer srv.Close()

	body, _ := json.Marshal(api.ExecConfig{Cmd: []string{"sh"}, ForwardAgent: true})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/exec", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	wantEnv := "SSH_AUTH_SOCK=" + remotecompanion.AgentSocketPath
	found := false
	for _, e := range fb.lastExecCfg.Env {
		if e == wantEnv {
			found = true
		}
	}
	if !found {
		t.Errorf("ExecCreate env = %v, want %q", fb.lastExecCfg.Env, wantEnv)
	}
}

// TestHandleDeployExecCreateAgentForwardCapableDisabled proves ForwardAgent is
// rejected when AgentForwardEnabled reports false (the deployment was applied
// without AgentForward set).
func TestHandleDeployExecCreateAgentForwardCapableDisabled(t *testing.T) {
	fb := &fakeAgentForwardBackend{enabled: false}
	srv := newTestServer(t, fb)
	defer srv.Close()

	body, _ := json.Marshal(api.ExecConfig{Cmd: []string{"sh"}, ForwardAgent: true})
	resp, err := http.Post(srv.URL+"/.cornus/v1/deploy/web/exec", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandleDeployExecAgentChannelAllowsAgentForwardCapable proves the
// exec-agent-channel endpoint also accepts the per-deployment
// AgentForwardCapable opt-in path, not just RemoteCapable.
func TestHandleDeployExecAgentChannelAllowsAgentForwardCapable(t *testing.T) {
	srv := newTestServer(t, &fakeAgentForwardBackend{enabled: true})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	sess, err := wire.DialControlHeaderTLS(ctx, wsBase+"/.cornus/v1/deploy/web/exec-agent-channel", nil, nil, nil)
	if err != nil {
		t.Fatalf("dial exec-agent-channel: %v", err)
	}
	sess.Close()
}
