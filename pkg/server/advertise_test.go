package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/deploywire"
	"cornus/pkg/wire"
)

// TestAdvertiseURLAndAgentImageAreTrimmedOnTheAttachPath pins that a
// CORNUS_ADVERTISE_URL or CORNUS_AGENT_IMAGE carrying surrounding whitespace
// reaches the backend trimmed.
//
// The regression: these two variables used to be read with a bare os.Getenv at
// ten sites, two of which (the telemetry ones) trimmed and the rest of which did
// not. A value with a trailing newline — what a YAML folded scalar or a
// hand-edited env file produces — was therefore SET for every consumer but
// well-formed for only some: the telemetry endpoint came out clean while the
// mount and egress paths handed the caretaker a RelayURL with a newline in it,
// which fails when the companion dials, far from the cause. Trimming in one
// accessor is what makes "set" mean the same thing on every path.
func TestAdvertiseURLAndAgentImageAreTrimmedOnTheAttachPath(t *testing.T) {
	fb := &fakeMountingBackend{mounts: make(chan []deploy.AttachMount, 1)}
	srv := newTestServer(t, fb)
	defer srv.Close()

	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")
	t.Setenv("CORNUS_ADVERTISE_URL", " "+wsBase+"\n")
	t.Setenv("CORNUS_AGENT_IMAGE", "\tregistry.example/cornus:latest ")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	as := deploywire.DeployAttachSpec{
		Spec: api.DeploySpec{
			Name:   "web",
			Image:  "img",
			Mounts: []api.Mount{{Source: "/client/x", Target: "/data", ReadOnly: true}},
		},
		LocalMounts: []deploywire.LocalMount{{Index: 0, Name: "m0", ReadOnly: true}},
	}
	go func() {
		_ = deploywire.Serve(ctx, wsBase+"/.cornus/v1/deploy/attach", as, map[string]string{"m0": t.TempDir()}, func(deploywire.Event) {}, nil, wire.ClientTransport{})
	}()

	var mounts []deploy.AttachMount
	select {
	case mounts = <-fb.mounts:
	case <-ctx.Done():
		t.Fatal("backend never received ApplyWithMounts")
	}
	if len(mounts) != 1 {
		t.Fatalf("got %d attach mounts, want 1", len(mounts))
	}
	if got := mounts[0].RelayURL; got != wsBase {
		t.Errorf("RelayURL = %q, want %q: the caretaker cannot dial a URL with whitespace in it", got, wsBase)
	}
	if got, want := mounts[0].AgentImage, "registry.example/cornus:latest"; got != want {
		t.Errorf("AgentImage = %q, want %q: an image reference with whitespace is not pullable", got, want)
	}
}

// TestAdvertiseAccessorsTreatBlankAsUnset pins the other half: a variable
// holding only whitespace must read as UNSET, so the attach paths reject it with
// their "requires CORNUS_ADVERTISE_URL" guidance rather than accepting it and
// wiring a caretaker to an empty address.
func TestAdvertiseAccessorsTreatBlankAsUnset(t *testing.T) {
	t.Setenv("CORNUS_ADVERTISE_URL", "   ")
	t.Setenv("CORNUS_AGENT_IMAGE", "\n")
	if got := advertiseURL(); got != "" {
		t.Errorf("advertiseURL() = %q, want \"\" (whitespace is not an address)", got)
	}
	if got := agentImage(); got != "" {
		t.Errorf("agentImage() = %q, want \"\" (whitespace is not an image reference)", got)
	}
}
