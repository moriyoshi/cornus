package clientconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Context.Shells names binaries that get EXECUTED inside a workload when a
// developer opens a terminal on it. Two properties carry the weight: the merge
// must REPLACE (it is a preference order, not an additive set), and the field must
// be classified sensitive so an auto-discovered per-project override file cannot
// supply it without explicit trust.

func TestMergeShellsReplacesRatherThanAppends(t *testing.T) {
	dst := &Context{Shells: []string{"/bin/zsh", "/bin/sh"}}
	Merge(dst, &Context{Shells: []string{"/bin/bash"}})
	// The whole slice: an appending implementation still CONTAINS /bin/bash and
	// still opens a working terminal — just the wrong one, ranked behind the base's
	// entries, which is the opposite of what an override layer means.
	want := []string{"/bin/bash"}
	if !reflect.DeepEqual(dst.Shells, want) {
		t.Errorf("Shells = %q, want %q", dst.Shells, want)
	}
}

func TestMergeShellsLeavesDstAloneWhenSrcHasNone(t *testing.T) {
	dst := &Context{Shells: []string{"/bin/zsh"}}
	Merge(dst, &Context{Server: "http://x"})
	want := []string{"/bin/zsh"}
	if !reflect.DeepEqual(dst.Shells, want) {
		t.Errorf("Shells = %q, want unchanged %q", dst.Shells, want)
	}
}

func TestMergeShellsCopiesTheBackingArray(t *testing.T) {
	src := &Context{Shells: []string{"/bin/bash", "/bin/sh"}}
	dst := &Context{}
	Merge(dst, src)
	// Merge is applied in layers (stored context, then each --from-file), so an
	// aliased slice would let a later layer's in-place edit rewrite an earlier
	// result — invisibly, since the length never changes.
	src.Shells[0] = "/bin/tampered"
	if dst.Shells[0] != "/bin/bash" {
		t.Errorf("dst.Shells[0] = %q after editing src; Merge aliased the backing array", dst.Shells[0])
	}
}

func TestShellsIsClassifiedSensitiveAndStripped(t *testing.T) {
	c := &Context{
		Shells:    []string{"/bin/bash"},
		ViaServer: boolPtr(true),
	}
	all, sensitive := FieldNames(c)
	if !reflect.DeepEqual(all, []string{"via-server", "shells"}) {
		t.Errorf("all fields = %v, want [via-server shells]", all)
	}
	// The load-bearing assertion: shells is SENSITIVE. Were it classified safe, an
	// auto-discovered cornus-context.yaml in any checked-out repository could name
	// the binary a developer's terminal executes inside their own workload.
	if !reflect.DeepEqual(sensitive, []string{"shells"}) {
		t.Errorf("sensitive fields = %v, want [shells]", sensitive)
	}

	stripped := StripSensitive(c)
	if !reflect.DeepEqual(stripped, []string{"shells"}) {
		t.Errorf("StripSensitive returned %v, want [shells]", stripped)
	}
	if c.Shells != nil {
		t.Errorf("StripSensitive left Shells = %q, want nil", c.Shells)
	}
	if c.ViaServer == nil || !*c.ViaServer {
		t.Error("StripSensitive must keep the safe via-server field")
	}
}

// shells must survive the strict loader in every accepted format, since an
// unknown key is a hard error: a format that failed to map the key would reject
// the whole file rather than quietly ignoring it.
func TestShellsLoadsFromEveryContextFileFormat(t *testing.T) {
	want := []string{"/bin/bash", "/bin/busybox sh"}
	files := map[string]string{
		"cornus-context.json": `{"shells": ["/bin/bash", "/bin/busybox sh"]}`,
		"cornus-context.yaml": "shells:\n  - /bin/bash\n  - /bin/busybox sh\n",
		"cornus-context.toml": "shells = [\"/bin/bash\", \"/bin/busybox sh\"]\n",
	}
	dir := t.TempDir()
	for name, body := range files {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadContextFile(p)
			if err != nil {
				t.Fatalf("LoadContextFile(%s): %v", name, err)
			}
			if !reflect.DeepEqual(got.Shells, want) {
				t.Errorf("Shells = %q, want %q", got.Shells, want)
			}
		})
	}
}
