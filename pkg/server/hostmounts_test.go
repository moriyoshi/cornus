package server

import (
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/config"
	"cornus/pkg/hostenv"
)

// tableMapper answers from a fixed prefix table; an absent prefix is not
// host-visible.
type tableMapper map[string]string

func (m tableMapper) ToHost(p string) (string, bool) {
	for container, host := range m {
		if p == container {
			return host, true
		}
		if strings.HasPrefix(p, strings.TrimSuffix(container, "/")+"/") {
			return host + strings.TrimPrefix(p, strings.TrimSuffix(container, "/")), true
		}
	}
	return "", false
}

func (m tableMapper) Propagation(string) string { return hostenv.PropagationShared }

func testServer(mapper hostenv.Mapper) *Server {
	return &Server{
		cfg:  config.Config{DataDir: "/var/lib/cornus"},
		host: hostSetup{mapper: mapper},
	}
}

// A mountpoint the server minted is translated; a user's own bind source is
// already a host path (the daemon opens it) and must be left alone.
func TestHostVisibleMountSourcesTranslatesOnlyServerMounts(t *testing.T) {
	s := testServer(tableMapper{"/var/lib/cornus": "/srv/cornus"})
	got, err := s.hostVisibleMountSources(api.DeploySpec{Mounts: []api.Mount{
		{Source: "/var/lib/cornus/mounts/sess1/0", Target: "/work"},
		{Source: "/home/dev/data", Target: "/data"},
		{Source: "/var/lib/cornus-elsewhere/x", Target: "/other"},
	}})
	if err != nil {
		t.Fatalf("hostVisibleMountSources: %v", err)
	}
	want := []string{"/srv/cornus/mounts/sess1/0", "/home/dev/data", "/var/lib/cornus-elsewhere/x"}
	for i, w := range want {
		if got.Mounts[i].Source != w {
			t.Errorf("mount %d source = %q, want %q", i, got.Mounts[i].Source, w)
		}
	}
}

// The failure this whole path exists to prevent: the runtime would accept an
// invisible path, create it empty, and start the workload against nothing.
func TestHostVisibleMountSourcesRejectsInvisibleMountsDir(t *testing.T) {
	s := testServer(tableMapper{"/opt/elsewhere": "/srv/elsewhere"})
	_, err := s.hostVisibleMountSources(api.DeploySpec{Mounts: []api.Mount{
		{Source: "/var/lib/cornus/mounts/sess1/0", Target: "/work"},
	}})
	if err == nil {
		t.Fatal("accepted a mountpoint the runtime cannot see; the workload would start against an empty directory")
	}
	// The message must name the target and both remedies: nothing else in the
	// system will report this.
	for _, want := range []string{"/work", "rshared", hostenv.HostPathMapEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// Every non-containerized server maps to itself, so this must be a no-op that
// cannot fail — the overwhelmingly common case.
func TestHostVisibleMountSourcesIdentityIsNoOp(t *testing.T) {
	s := testServer(hostenv.Identity())
	in := api.DeploySpec{Mounts: []api.Mount{{Source: "/var/lib/cornus/mounts/s/0", Target: "/work"}}}
	got, err := s.hostVisibleMountSources(in)
	if err != nil {
		t.Fatalf("hostVisibleMountSources: %v", err)
	}
	if got.Mounts[0].Source != in.Mounts[0].Source {
		t.Errorf("source = %q, want it unchanged", got.Mounts[0].Source)
	}
}

// The spec the caller handed in must not be mutated: it is re-read elsewhere on
// the deploy-attach path.
func TestHostVisibleMountSourcesDoesNotMutateInput(t *testing.T) {
	s := testServer(tableMapper{"/var/lib/cornus": "/srv/cornus"})
	in := api.DeploySpec{Mounts: []api.Mount{{Source: "/var/lib/cornus/mounts/s/0", Target: "/work"}}}
	if _, err := s.hostVisibleMountSources(in); err != nil {
		t.Fatal(err)
	}
	if in.Mounts[0].Source != "/var/lib/cornus/mounts/s/0" {
		t.Errorf("input spec was mutated: %q", in.Mounts[0].Source)
	}
}

// The policy inside the backend sees the translated (host) source, so the
// carve-out must permit it — otherwise the server's own client-local mounts are
// rejected by its own default-deny policy the moment translation kicks in.
func TestMountBindPrefixesIncludeTheHostSpelling(t *testing.T) {
	cfg := config.Config{DataDir: "/var/lib/cornus"}
	got := mountBindPrefixes(cfg, tableMapper{"/var/lib/cornus": "/srv/cornus"})
	want := map[string]bool{"/var/lib/cornus/mounts": true, "/srv/cornus/mounts": true}
	if len(got) != 2 {
		t.Fatalf("prefixes = %v, want both spellings", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected prefix %q", p)
		}
	}
}

// With no translation in effect there is only one spelling, and duplicating it
// would just add noise to the policy.
func TestMountBindPrefixesIdentityYieldsOne(t *testing.T) {
	cfg := config.Config{DataDir: "/var/lib/cornus"}
	got := mountBindPrefixes(cfg, hostenv.Identity())
	if len(got) != 1 || got[0] != cfg.MountsDir() {
		t.Errorf("prefixes = %v, want just %q", got, cfg.MountsDir())
	}
}

func TestUnderDir(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{"/var/lib/cornus/mounts", "/var/lib/cornus/mounts", true},
		{"/var/lib/cornus/mounts/s/0", "/var/lib/cornus/mounts", true},
		{"/var/lib/cornus/mounts-old/x", "/var/lib/cornus/mounts", false},
		{"/var/lib", "/var/lib/cornus/mounts", false},
		{"", "/var/lib", false},
		{"/var/lib", "", false},
	} {
		if got := underDir(tc.path, tc.dir); got != tc.want {
			t.Errorf("underDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}
