package compose

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// planBuild loads a compose document, plans it, and returns the named service's
// BuildPlan (failing the test if absent).
func planBuild(t *testing.T, content, service string) *BuildPlan {
	t.Helper()
	file := writeCompose(t, content)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	plans, err := project.Plan(project.ResolveName(filepath.Dir(file)))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	bp := plans[service].Build
	if bp == nil {
		t.Fatalf("service %q has no build plan", service)
	}
	return bp
}

// TestBuildKeysMapForms parses the map/scalar forms of the extended build keys
// and asserts translateService carries each into the BuildPlan.
func TestBuildKeysMapForms(t *testing.T) {
	bp := planBuild(t, `
services:
  app:
    build:
      context: ./app
      no_cache: true
      pull: true
      network: host
      shm_size: 128M
      labels:
        com.example.k: v
        com.example.j: w
      extra_hosts:
        somehost: 162.242.195.82
`, "app")

	if !bp.NoCache {
		t.Error("NoCache not carried")
	}
	if !bp.Pull {
		t.Error("Pull not carried")
	}
	if bp.Network != "host" {
		t.Errorf("Network = %q", bp.Network)
	}
	// 128M = 128 * 1024 * 1024 bytes (power-of-1024, matching parseSize).
	if bp.ShmSize != 128*1024*1024 {
		t.Errorf("ShmSize = %d want %d", bp.ShmSize, 128*1024*1024)
	}
	if bp.Labels["com.example.k"] != "v" || bp.Labels["com.example.j"] != "w" {
		t.Errorf("Labels = %v", bp.Labels)
	}
	// extra_hosts map form normalises to "host:ip".
	if len(bp.ExtraHosts) != 1 || bp.ExtraHosts[0] != "somehost:162.242.195.82" {
		t.Errorf("ExtraHosts = %v", bp.ExtraHosts)
	}
}

// TestBuildKeysListForms parses the list forms of the extended build keys.
func TestBuildKeysListForms(t *testing.T) {
	bp := planBuild(t, `
services:
  app:
    build:
      context: ./app
      platforms:
        - linux/amd64
        - linux/arm64
      tags:
        - reg/app:1.0
        - reg/app:latest
      cache_to:
        - type=registry,ref=reg/app:cache
        - reg/app:bare
      labels:
        - com.example.k=v
      extra_hosts:
        - "myhost:10.0.0.1"
`, "app")

	if !reflect.DeepEqual(bp.Platforms, []string{"linux/amd64", "linux/arm64"}) {
		t.Errorf("Platforms = %v", bp.Platforms)
	}
	if !reflect.DeepEqual(bp.Tags, []string{"reg/app:1.0", "reg/app:latest"}) {
		t.Errorf("Tags = %v", bp.Tags)
	}
	if !reflect.DeepEqual(bp.CacheTo, []string{"type=registry,ref=reg/app:cache", "reg/app:bare"}) {
		t.Errorf("CacheTo = %v", bp.CacheTo)
	}
	// labels list (KEY=VALUE) form decodes like a map.
	if bp.Labels["com.example.k"] != "v" {
		t.Errorf("Labels = %v", bp.Labels)
	}
	if len(bp.ExtraHosts) != 1 || bp.ExtraHosts[0] != "myhost:10.0.0.1" {
		t.Errorf("ExtraHosts = %v", bp.ExtraHosts)
	}
}

// TestBuildDockerfileInlinePrecedence asserts build.dockerfile_inline is carried
// into the BuildPlan alongside dockerfile (the inline body supersedes dockerfile
// downstream in client.Build, which stages it as a synthetic Dockerfile).
func TestBuildDockerfileInlinePrecedence(t *testing.T) {
	bp := planBuild(t, `
services:
  app:
    build:
      context: ./app
      dockerfile: Dockerfile.prod
      dockerfile_inline: |
        FROM alpine
        RUN echo hi
`, "app")

	if bp.DockerfileInline != "FROM alpine\nRUN echo hi\n" {
		t.Errorf("DockerfileInline = %q", bp.DockerfileInline)
	}
	// dockerfile is still carried; precedence (inline wins) is applied by client.Build.
	if bp.Dockerfile != "Dockerfile.prod" {
		t.Errorf("Dockerfile = %q", bp.Dockerfile)
	}
}

// TestBuildSecretsLongForm asserts the long form of build.secrets captures
// target/uid/gid/mode on the parsed model while still resolving the id to a file.
func TestBuildSecretsLongForm(t *testing.T) {
	file := writeCompose(t, `
services:
  app:
    build:
      context: .
      secrets:
        - source: mysecret
          target: /run/secrets/db
          uid: "103"
          gid: "103"
          mode: "0440"
secrets:
  mysecret:
    file: ./secret.txt
`)
	project, err := Load(file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The parsed model captures the long-form attributes.
	sec := project.Services()["app"].Build.Secrets
	if len(sec) != 1 {
		t.Fatalf("build.secrets = %+v want 1", sec)
	}
	got := sec[0]
	want := BuildSecret{Source: "mysecret", Target: "/run/secrets/db", UID: "103", GID: "103", Mode: "0440"}
	if got != want {
		t.Fatalf("BuildSecret = %+v want %+v", got, want)
	}
	// The id still resolves to its top-level file in the BuildPlan (target/uid/gid/
	// mode are not yet plumbed to the build engine — a warning is emitted).
	plans, err := project.Plan("p")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plans["app"].Build.Secrets["mysecret"] != "./secret.txt" {
		t.Fatalf("BuildPlan.Secrets = %v", plans["app"].Build.Secrets)
	}
}

// TestMergeBuildKeys checks the multi-file deep merge of the extended build keys:
// labels merge key-by-key, list keys append-dedup, and scalars/bools override
// when the later file sets them.
func TestMergeBuildKeys(t *testing.T) {
	base := writeCompose(t, `
services:
  app:
    build:
      context: ./app
      network: default
      labels:
        a: "1"
      platforms:
        - linux/amd64
      tags:
        - reg/app:1.0
      cache_to:
        - reg/app:cache
      extra_hosts:
        - "h1:10.0.0.1"
`)
	override := writeCompose(t, `
services:
  app:
    build:
      no_cache: true
      pull: true
      network: host
      shm_size: 64M
      labels:
        b: "2"
      platforms:
        - linux/arm64
      tags:
        - reg/app:2.0
      cache_to:
        - reg/app:cache2
      extra_hosts:
        - "h2:10.0.0.2"
`)
	project, err := Load(base, override)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := project.Services()["app"].Build
	if !b.NoCache || !b.Pull {
		t.Errorf("no_cache=%v pull=%v want both true", b.NoCache, b.Pull)
	}
	if b.Network != "host" {
		t.Errorf("Network = %q want host (override wins)", b.Network)
	}
	if b.ShmSize != "64M" {
		t.Errorf("ShmSize = %q", b.ShmSize)
	}
	if b.Labels["a"] != "1" || b.Labels["b"] != "2" {
		t.Errorf("Labels = %v want merged a+b", b.Labels)
	}
	assertSetEqual(t, "Platforms", b.Platforms, []string{"linux/amd64", "linux/arm64"})
	assertSetEqual(t, "Tags", b.Tags, []string{"reg/app:1.0", "reg/app:2.0"})
	assertSetEqual(t, "CacheTo", b.CacheTo, []string{"reg/app:cache", "reg/app:cache2"})
	assertSetEqual(t, "ExtraHosts", []string(b.ExtraHosts), []string{"h1:10.0.0.1", "h2:10.0.0.2"})
}

func assertSetEqual(t *testing.T, name string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %v want (as set) %v", name, got, want)
	}
}
