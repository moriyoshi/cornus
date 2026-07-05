package herdr

import (
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml"
)

// What a bundled manifest must look like. Asserted structurally rather than by
// pinning exact contents: the files are third-party and REFRESHED from upstream
// as agent UIs change, so a test that pinned their text would fail on every
// routine update and teach whoever ran it to delete the test.
//
// What it does catch is a refresh that landed broken — truncated, half-copied, or
// no longer TOML — which is the failure a vendored data bundle actually has.
func TestBundledManifestsAreIntact(t *testing.T) {
	got, err := Manifests()
	if err != nil {
		t.Fatal(err)
	}
	// Upstream shipped 19 at the vendored commit. A floor rather than an equality:
	// upstream adding an agent is a good thing and should not fail the build, but
	// a bundle that suddenly holds two files is a copy that went wrong.
	if len(got) < 15 {
		t.Fatalf("only %d manifests bundled, want at least 15 — did the copy truncate?", len(got))
	}

	seen := map[string]bool{}
	for _, a := range got {
		if seen[a.ID] {
			t.Errorf("duplicate manifest id %q", a.ID)
		}
		seen[a.ID] = true

		if len(a.TOML) == 0 {
			t.Errorf("%s: empty manifest", a.ID)
			continue
		}
		var doc struct {
			ID      string `toml:"id"`
			Version string `toml:"version"`
			Rules   []struct {
				ID    string `toml:"id"`
				State string `toml:"state"`
			} `toml:"rules"`
		}
		if err := toml.Unmarshal(a.TOML, &doc); err != nil {
			t.Errorf("%s: not parseable as TOML: %v", a.ID, err)
			continue
		}
		if doc.ID == "" {
			t.Errorf("%s: manifest has no id", a.ID)
		}
		if doc.Version == "" {
			t.Errorf("%s: manifest has no version", a.ID)
		}
		if len(doc.Rules) == 0 {
			t.Errorf("%s: manifest carries no rules", a.ID)
		}
		for i, r := range doc.Rules {
			switch r.State {
			case "working", "idle", "blocked", "unknown", "done":
			default:
				t.Errorf("%s: rule %d (%q) has unknown state %q", a.ID, i, r.ID, r.State)
			}
		}
	}

	// The agent the rest of this work keeps referring to had better be in here.
	if !seen["claude"] {
		t.Errorf("no claude manifest bundled; got %v", ids(got))
	}
}

// The vendored tree must keep the attribution Apache-2.0 section 4 requires. This
// is cheap and guards the one thing about a vendored bundle that is a legal
// question rather than a technical one — a future tidy-up that deletes LICENSE
// because "it is not code" fails here.
func TestVendoredLicenseIsRetained(t *testing.T) {
	// Read from disk rather than the embed: LICENSE is deliberately NOT embedded
	// into the binary (it is a repository artifact, and THIRD_PARTY_NOTICES.md is
	// what ships), so the embed could not prove it is present.
	b, err := readRepoFile("LICENSE")
	if err != nil {
		t.Fatalf("vendored LICENSE missing: %v", err)
	}
	if !strings.Contains(string(b), "Apache License") || !strings.Contains(string(b), "Version 2.0") {
		t.Error("vendored LICENSE is not the Apache 2.0 text")
	}
	r, err := readRepoFile("README.md")
	if err != nil {
		t.Fatalf("vendored README missing: %v", err)
	}
	// The upstream commit is the attribution that makes the bundle auditable; a
	// refresh that forgets to record which commit it took is the failure here.
	for _, want := range []string{"github.com/ogulcancelik/herdr", "Apache License 2.0", "Commit"} {
		if !strings.Contains(string(r), want) {
			t.Errorf("vendored README does not record %q", want)
		}
	}
}

func ids(as []Agent) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}
