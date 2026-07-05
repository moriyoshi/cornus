//go:build linux

package barehost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestNeedsRebootRecovery(t *testing.T) {
	dead := func(string) bool { return false }
	alive := func(string) bool { return true }

	cases := []struct {
		name  string
		rec   *instanceRecord
		alive func(string) bool
		want  bool
	}{
		{"dead pin -> recover", &instanceRecord{ID: "a", NetNS: "/run/cornus/netns/a"}, dead, true},
		{"live pin -> no recover", &instanceRecord{ID: "a", NetNS: "/run/cornus/netns/a"}, alive, false},
		{"no netns -> no recover", &instanceRecord{ID: "a"}, dead, false},
		{"companion never recovered here", &instanceRecord{ID: "c", Role: roleEgressCaretaker, NetNS: "/run/cornus/netns/a"}, dead, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsRebootRecovery(tc.rec, tc.alive); got != tc.want {
				t.Errorf("needsRebootRecovery = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRewriteNetnsPath(t *testing.T) {
	bundle := t.TempDir()
	orig := &specs.Spec{
		Version: "1.0.2",
		Linux: &specs.Linux{
			Namespaces: []specs.LinuxNamespace{
				{Type: specs.PIDNamespace},
				{Type: specs.NetworkNamespace, Path: "/run/cornus/netns/dead-pin"},
				{Type: specs.MountNamespace},
			},
		},
	}
	if err := writeBundleConfig(bundle, orig); err != nil {
		t.Fatalf("writeBundleConfig: %v", err)
	}

	if err := rewriteNetnsPath(bundle, "/run/cornus/netns/fresh-pin"); err != nil {
		t.Fatalf("rewriteNetnsPath: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var got specs.Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}
	// The network namespace path is repointed; the other namespaces are untouched.
	var netPath string
	types := make([]specs.LinuxNamespaceType, 0, len(got.Linux.Namespaces))
	for _, ns := range got.Linux.Namespaces {
		types = append(types, ns.Type)
		if ns.Type == specs.NetworkNamespace {
			netPath = ns.Path
		}
	}
	if netPath != "/run/cornus/netns/fresh-pin" {
		t.Errorf("network namespace path = %q, want the fresh pin", netPath)
	}
	if len(got.Linux.Namespaces) != 3 {
		t.Errorf("namespace count changed: %v", types)
	}
}

func TestRewriteNetnsPathNoNetworkNamespace(t *testing.T) {
	bundle := t.TempDir()
	if err := writeBundleConfig(bundle, &specs.Spec{Linux: &specs.Linux{}}); err != nil {
		t.Fatalf("writeBundleConfig: %v", err)
	}
	if err := rewriteNetnsPath(bundle, "/run/cornus/netns/x"); err == nil {
		t.Error("rewriteNetnsPath should error when there is no network namespace to repoint")
	}
}
