package hostpolicy

import (
	"os"
	"path/filepath"
	"testing"

	"cornus/pkg/api"
)

func TestPolicyValidatePrivileged(t *testing.T) {
	spec := api.DeploySpec{Name: "x", Image: "img", Privileged: true}

	if err := (Policy{}).Validate("test", spec); err == nil {
		t.Fatal("default-deny policy should reject a privileged spec")
	}
	if err := (Policy{AllowPrivileged: true}).Validate("test", spec); err != nil {
		t.Fatalf("AllowPrivileged should permit a privileged spec: %v", err)
	}
	// A non-privileged spec is fine either way.
	if err := (Policy{}).Validate("test", api.DeploySpec{Name: "x", Image: "img"}); err != nil {
		t.Fatalf("non-privileged spec rejected: %v", err)
	}
}

func TestPolicyValidateBinds(t *testing.T) {
	specWith := func(src string) api.DeploySpec {
		return api.DeploySpec{Name: "x", Image: "img", Mounts: []api.Mount{{Source: src, Target: "/t"}}}
	}

	// Default-deny: any host bind is rejected.
	if err := (Policy{}).Validate("test", specWith("/data")); err == nil {
		t.Fatal("default-deny policy should reject a host bind")
	}
	// The dangerous cases the policy exists to stop.
	if err := (Policy{}).Validate("test", specWith("/var/run/docker.sock")); err == nil {
		t.Fatal("default-deny should reject the docker socket bind")
	}
	if err := (Policy{}).Validate("test", specWith("/")); err == nil {
		t.Fatal("default-deny should reject a root bind")
	}

	// Prefix allowlist: sources under an allowed prefix pass; siblings don't.
	pol := Policy{AllowBindPrefixes: []string{"/srv/data"}}
	if err := pol.Validate("test", specWith("/srv/data")); err != nil {
		t.Fatalf("exact prefix should be allowed: %v", err)
	}
	if err := pol.Validate("test", specWith("/srv/data/sub/dir")); err != nil {
		t.Fatalf("nested path should be allowed: %v", err)
	}
	if err := pol.Validate("test", specWith("/srv/database")); err == nil {
		t.Fatal("a sibling sharing a string prefix must NOT be allowed (boundary check)")
	}
	if err := pol.Validate("test", specWith("/srv/data/../../etc")); err == nil {
		t.Fatal("a traversal escaping the prefix must be rejected")
	}

	// "/" permits any absolute source (Permissive / test server).
	root := Policy{AllowBindPrefixes: []string{"/"}}
	for _, src := range []string{"/etc", "/var/run/docker.sock", "/"} {
		if err := root.Validate("test", specWith(src)); err != nil {
			t.Fatalf("root prefix should allow %q: %v", src, err)
		}
	}

	// An empty source is a named/anonymous volume, not a host bind — always fine.
	if err := (Policy{}).Validate("test", api.DeploySpec{Name: "x", Image: "img", Mounts: []api.Mount{{Target: "/t"}}}); err != nil {
		t.Fatalf("empty-source mount should be allowed: %v", err)
	}
}

// TestPolicyValidateBindSymlinkEscape proves a source that is lexically inside an
// allowed prefix but whose real path escapes it -- via a symlinked component --
// is rejected. This is the symlink-bypass the daemon would otherwise follow when
// setting up the bind. It runs without root: it only creates a symlink.
func TestPolicyValidateBindSymlinkEscape(t *testing.T) {
	specWith := func(src string) api.DeploySpec {
		return api.DeploySpec{Name: "x", Image: "img", Mounts: []api.Mount{{Source: src, Target: "/t"}}}
	}

	allowed := t.TempDir() // the prefix operators opt in to
	outside := t.TempDir() // an out-of-policy location

	// A symlink INSIDE the allowed prefix pointing OUT of it.
	escape := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// A real subdirectory that legitimately lives under the prefix.
	inside := filepath.Join(allowed, "real")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A symlink inside the prefix that points to another location inside it.
	innerLink := filepath.Join(allowed, "innerlink")
	if err := os.Symlink(inside, innerLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	pol := Policy{AllowBindPrefixes: []string{allowed}}

	// The escape symlink itself, and any path traversing it, must be rejected
	// because they resolve outside the allowed prefix.
	if err := pol.Validate("test", specWith(escape)); err == nil {
		t.Fatal("a symlink escaping the allowed prefix must be rejected")
	}
	if err := pol.Validate("test", specWith(filepath.Join(escape, "secret"))); err == nil {
		t.Fatal("a path under a symlink escaping the allowed prefix must be rejected")
	}
	// Attacker also can't point the source directly at the out-of-policy dir.
	if err := pol.Validate("test", specWith(filepath.Join(outside, "secret"))); err == nil {
		t.Fatal("a source outside every prefix must be rejected")
	}

	// A real subdirectory, and a symlink that stays inside the prefix, are fine.
	if err := pol.Validate("test", specWith(inside)); err != nil {
		t.Fatalf("a real subdir under the prefix should be allowed: %v", err)
	}
	if err := pol.Validate("test", specWith(innerLink)); err != nil {
		t.Fatalf("a symlink resolving inside the prefix should be allowed: %v", err)
	}
	// A not-yet-existent path under the real prefix stays allowed (the daemon may
	// create it); resolution falls back to the deepest existing ancestor.
	if err := pol.Validate("test", specWith(filepath.Join(inside, "does", "not", "exist"))); err != nil {
		t.Fatalf("a not-yet-existent path under the prefix should be allowed: %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("CORNUS_ALLOW_PRIVILEGED", "1")
	t.Setenv("CORNUS_ALLOW_BIND_SOURCES", "/srv/a, /srv/b")
	p := FromEnv()
	if !p.AllowPrivileged {
		t.Fatal("CORNUS_ALLOW_PRIVILEGED=1 should set AllowPrivileged")
	}
	if len(p.AllowBindPrefixes) != 2 || p.AllowBindPrefixes[0] != "/srv/a" || p.AllowBindPrefixes[1] != "/srv/b" {
		t.Fatalf("bind prefixes = %v", p.AllowBindPrefixes)
	}

	t.Setenv("CORNUS_ALLOW_PRIVILEGED", "no")
	if FromEnv().AllowPrivileged {
		t.Fatal("CORNUS_ALLOW_PRIVILEGED=no should not set AllowPrivileged")
	}
}

// TestErrorNamesBackend proves the error text names the calling backend so a
// denial is attributable when more than one host backend exists.
func TestErrorNamesBackend(t *testing.T) {
	err := (Policy{}).Validate("containerdhost", api.DeploySpec{Name: "x", Image: "img", Privileged: true})
	if err == nil || err.Error()[:len("containerdhost:")] != "containerdhost:" {
		t.Fatalf("error should be prefixed with the backend name: %v", err)
	}
}
