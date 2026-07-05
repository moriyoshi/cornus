package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetNeeds(t *testing.T) {
	cases := []struct {
		target Target
		want   []Capability
		absent []Capability
	}{
		{&LocalTarget{}, nil, []Capability{CapDocker, CapKind, CapKubectl, Cap9P, CapContainerd, CapOCIRuntime}},
		{&DockerTarget{}, []Capability{CapDocker}, []Capability{CapKind, CapKubectl, Cap9P, CapContainerd, CapOCIRuntime}},
		{&ContainerdTarget{}, []Capability{CapContainerd}, []Capability{CapDocker, CapKind, CapKubectl, Cap9P, CapOCIRuntime}},
		{&BareTarget{}, []Capability{CapOCIRuntime}, []Capability{CapDocker, CapKind, CapKubectl, Cap9P, CapContainerd}},
		{&KubeTarget{}, []Capability{CapDocker, CapKind, CapKubectl, Cap9P}, []Capability{CapContainerd, CapOCIRuntime}},
	}
	for _, c := range cases {
		got := targetNeeds(c.target)
		for _, w := range c.want {
			if !hasCap(got, w) {
				t.Errorf("%T: missing required cap %s", c.target, w.Name())
			}
		}
		for _, a := range c.absent {
			if hasCap(got, a) {
				t.Errorf("%T: unexpected cap %s", c.target, a.Name())
			}
		}
	}
}

func TestScenarioNeeds(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	buildSc := write("b.star", "serve()\nbuild(name=\"x\", context=\".\")\n")
	caps, err := scenarioNeeds(buildSc)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCap(caps, CapBuildEngine) {
		t.Error("build() scenario should need the build engine")
	}
	if hasCap(caps, CapSSHTools) {
		t.Error("build() scenario without ssh_agent() should not need ssh tools")
	}

	sshSc := write("s.star", "serve()\nssh_agent()\nbuild(name=\"x\", context=\".\")\n")
	caps, err = scenarioNeeds(sshSc)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCap(caps, CapSSHTools) || !hasCap(caps, CapBuildEngine) {
		t.Errorf("ssh_agent()+build() scenario should need both ssh tools and build engine, got %v", caps)
	}

	plainSc := write("p.star", "serve()\nregistry_roundtrip(ref=\"a/b:v1\")\n")
	caps, err = scenarioNeeds(plainSc)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 0 {
		t.Errorf("registry-only scenario should need nothing, got %v", caps)
	}

	// build_upload() (raw POST /.cornus/v1/build) executes a real build but does NOT
	// contain the build( token, so it must be flagged explicitly.
	uploadSc := write("u.star", "serve()\nbuild_upload(target=\"x:v1\", context=\".\")\n")
	caps, err = scenarioNeeds(uploadSc)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCap(caps, CapBuildEngine) {
		t.Error("build_upload() scenario should need the build engine")
	}

	// A build(lazy_9p=True) scenario kernel-mounts a p9 server, so it needs 9p in
	// addition to the build engine.
	lazy9pSc := write("l9.star", "serve()\nbuild(name=\"x\", context=\".\", lazy_9p=True)\n")
	caps, err = scenarioNeeds(lazy9pSc)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCap(caps, Cap9P) || !hasCap(caps, CapBuildEngine) {
		t.Errorf("lazy_9p build scenario should need both 9p and the build engine, got %v", caps)
	}

	// A plain lazy build (default host-dir bind) does NOT need the 9p kernel module.
	lazySc := write("lz.star", "serve()\nbuild(name=\"x\", context=\".\", lazy=True)\n")
	caps, err = scenarioNeeds(lazySc)
	if err != nil {
		t.Fatal(err)
	}
	if hasCap(caps, Cap9P) {
		t.Errorf("plain lazy build scenario should not need 9p, got %v", caps)
	}

	// devcontainer_cli( shells out to the official @devcontainers/cli binary.
	// The devcontainer_up( builtin (cornus's own translation) must NOT flag it.
	dcSc := write("dc.star", "serve()\ndockerd_up()\ndevcontainer_cli(\"up\", \"--workspace-folder\", \".\")\n")
	caps, err = scenarioNeeds(dcSc)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCap(caps, CapDevcontainerCLI) {
		t.Errorf("devcontainer_cli() scenario should need the devcontainer CLI, got %v", caps)
	}
	nativeSc := write("dn.star", "serve()\ndevcontainer_up(dir=\"x\")\n")
	caps, err = scenarioNeeds(nativeSc)
	if err != nil {
		t.Fatal(err)
	}
	if hasCap(caps, CapDevcontainerCLI) {
		t.Errorf("cornus-native devcontainer_up() scenario should not need the devcontainer CLI, got %v", caps)
	}
}

func TestScenarioNeedsComposeBuild(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Compose file WITH a service-level build: section. The scenario drives it
	// purely via compose_up() (never build()), yet needs the build engine.
	write("with-build.yaml", "services:\n  web:\n    build:\n      context: .\n    ports:\n      - \"8080:80\"\n")
	buildSc := write("cbuild.star", "serve()\ncompose_up(file = \"with-build.yaml\", project = \"cbld\")\n")
	caps, err := scenarioNeeds(buildSc)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCap(caps, CapBuildEngine) {
		t.Errorf("compose_up() against a build: compose file should need the build engine, got %v", caps)
	}

	// Compose file WITHOUT a build: section: an image-only compose. No build need.
	write("no-build.yaml", "services:\n  web:\n    image: nginx:latest\n    ports:\n      - \"8080:80\"\n")
	noBuildSc := write("cnobuild.star", "serve()\ncompose_up(file = \"no-build.yaml\", project = \"cnb\")\n")
	caps, err = scenarioNeeds(noBuildSc)
	if err != nil {
		t.Fatal(err)
	}
	if hasCap(caps, CapBuildEngine) {
		t.Errorf("compose_up() against an image-only compose file should not need the build engine, got %v", caps)
	}

	// Compose file path referenced indirectly via a variable, and passed to
	// compose_build(). Still detected.
	write("via-var.yaml", "services:\n  api:\n    build: ./api\n")
	varSc := write("cvar.star", "serve()\ncompose_file = \"via-var.yaml\"\ncompose_build(file = compose_file, project = \"cv\")\n")
	caps, err = scenarioNeeds(varSc)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCap(caps, CapBuildEngine) {
		t.Errorf("compose_build() against a build: compose file (via variable) should need the build engine, got %v", caps)
	}
}

func TestPreflightAggregatesRequiredBy(t *testing.T) {
	dir := t.TempDir()
	buildSc := filepath.Join(dir, "build.star")
	if err := os.WriteFile(buildSc, []byte("serve()\nbuild(name=\"x\", context=\".\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// LocalTarget adds no capabilities of its own, so the only required cap here
	// is the build engine (from the scenario) — keeping this test off docker.
	results, err := Preflight(context.Background(), &LocalTarget{}, []string{buildSc})
	if err != nil {
		t.Fatal(err)
	}
	var found *CheckResult
	for i := range results {
		if results[i].Cap == CapBuildEngine {
			found = &results[i]
		}
		if results[i].Cap == CapDocker {
			t.Error("local target + build scenario should not require docker")
		}
	}
	if found == nil {
		t.Fatal("build engine capability not reported")
	}
	if len(found.RequiredBy) != 1 || found.RequiredBy[0] != buildSc {
		t.Errorf("RequiredBy = %v, want [%s]", found.RequiredBy, buildSc)
	}
}

func TestContainerdProbeAddress(t *testing.T) {
	// A --containerd-address flag lands in ContainerdTarget.Address (kong never
	// mirrors it into the environment), so the probe must honour it over the env.
	t.Setenv("CORNUS_CONTAINERD_ADDRESS", "/run/from-env.sock")
	if got := containerdProbeAddress(&ContainerdTarget{Address: "/run/from-flag.sock"}); got != "/run/from-flag.sock" {
		t.Errorf("flag address should win: got %q, want /run/from-flag.sock", got)
	}
	// With no explicit target address, the probe falls back to the environment.
	if got := containerdProbeAddress(&ContainerdTarget{}); got != "/run/from-env.sock" {
		t.Errorf("empty target address should fall back to env: got %q, want /run/from-env.sock", got)
	}
	// A non-containerd target has no socket of its own; use the env/default.
	if got := containerdProbeAddress(&LocalTarget{}); got != "/run/from-env.sock" {
		t.Errorf("non-containerd target should use env: got %q, want /run/from-env.sock", got)
	}
	// No env, no flag: the stock socket path.
	os.Unsetenv("CORNUS_CONTAINERD_ADDRESS")
	if got := containerdProbeAddress(&ContainerdTarget{}); got != "/run/containerd/containerd.sock" {
		t.Errorf("default socket: got %q, want /run/containerd/containerd.sock", got)
	}
}

func TestFirstFailureAndFormat(t *testing.T) {
	results := []CheckResult{
		{Cap: CapDocker, OK: true, Detail: "daemon 27", RequiredBy: []string{"<docker target>"}},
		{Cap: CapKind, OK: false, Detail: "kind not on PATH", RequiredBy: []string{"<kube target>"}},
	}
	fail := FirstFailure(results)
	if fail == nil || fail.Cap != CapKind {
		t.Fatalf("FirstFailure = %v, want CapKind", fail)
	}
	out := FormatPreflight(results)
	if !strings.Contains(out, "kind") || !strings.Contains(out, "✗") || !strings.Contains(out, "✓") {
		t.Errorf("FormatPreflight output missing marks/name: %q", out)
	}
	if FirstFailure(results[:1]) != nil {
		t.Error("FirstFailure should be nil when all pass")
	}
	if CapHint(CapBuildEngine) == "" {
		t.Error("CapHint(CapBuildEngine) should be non-empty")
	}
}

// TestModulesDepHas9P checks the modules.dep fallback that lets probe9P treat a
// loadable-but-unloaded 9p module as usable (the privileged sidecar mounts it on
// demand). It must match the 9p fs module (plain or compressed) and must not be
// fooled by the 9pnet transport dependency alone or an empty listing.
func TestModulesDepHas9P(t *testing.T) {
	cases := []struct {
		name string
		dep  string
		want bool
	}{
		{"plain", "kernel/fs/9p/9p.ko: kernel/net/9p/9pnet.ko\n", true},
		{"compressed_zst", "kernel/fs/9p/9p.ko.zst: kernel/net/9p/9pnet.ko.zst\n", true},
		{"compressed_xz", "kernel/fs/9p/9p.ko.xz:\n", true},
		{"among_others", "kernel/fs/ext4/ext4.ko:\nkernel/fs/9p/9p.ko: kernel/net/9p/9pnet.ko\n", true},
		{"only_9pnet", "kernel/net/9p/9pnet.ko: kernel/net/9p/9pnet_virtio.ko\n", false},
		{"empty", "", false},
		{"unrelated", "kernel/fs/ext4/ext4.ko:\nkernel/drivers/net/e1000.ko:\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modulesDepHas9P(tc.dep); got != tc.want {
				t.Errorf("modulesDepHas9P(%q) = %v, want %v", tc.dep, got, tc.want)
			}
		})
	}
}
