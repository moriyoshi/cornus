package dockerhost

import (
	"context"
	"reflect"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// idempotenceBase is the minimal deployment every idempotence test starts from:
// name + image and nothing else. Every other field is left at its zero value so
// a single-field mutation is the ONLY difference between it and the spec applied
// next.
func idempotenceBase() api.DeploySpec {
	return api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"}
}

// changedSpec is idempotenceBase with EVERY api.DeploySpec field set to a
// non-zero value, assembled from the two lists this package already maintains:
// supportedSpec (the fields dockerhost realizes) and unsupportedFieldCases (the
// fields it warns about and drops). Reusing those rather than writing a third
// list is the point — a field added to api.DeploySpec must be added to one of
// them (TestEveryDeploySpecFieldIsMappedOrWarned makes sure of it), and it then
// appears here automatically.
//
// Name is held at the base value: it is the deployment's IDENTITY, not part of
// its configuration, so changing it does not describe the same deployment
// differently — it describes a different deployment. Image is overridden because
// supportedSpec happens to carry the same ref the base does.
func changedSpec() api.DeploySpec {
	s := supportedSpec()
	dst := reflect.ValueOf(&s).Elem()
	for _, tc := range unsupportedFieldCases {
		src := reflect.ValueOf(tc.spec)
		for i := 0; i < src.NumField(); i++ {
			if src.Type().Field(i).PkgPath == "" && !src.Field(i).IsZero() {
				dst.Field(i).Set(src.Field(i))
			}
		}
	}
	s.Name = idempotenceBase().Name
	s.Image = "localhost:5000/app:v2"
	return s
}

// TestChangedSpecCoversEveryDeploySpecField is what stops the table below from
// quietly shrinking. A field nobody sets in changedSpec produces a "mutated"
// spec identical to the base, and its row would then assert nothing while
// reporting a pass — the precise failure this package's field-coverage guards
// exist to prevent, now for the recreate decision instead of the warning
// surface.
func TestChangedSpecCoversEveryDeploySpecField(t *testing.T) {
	base, changed := idempotenceBase(), changedSpec()
	bv, cv := reflect.ValueOf(base), reflect.ValueOf(changed)
	var same []string
	for i := 0; i < cv.NumField(); i++ {
		f := cv.Type().Field(i)
		if f.PkgPath != "" || f.Name == "Name" {
			continue
		}
		if reflect.DeepEqual(bv.Field(i).Interface(), cv.Field(i).Interface()) {
			same = append(same, f.Name)
		}
	}
	if len(same) > 0 {
		t.Errorf("changedSpec does not differ from idempotenceBase in: %v\n"+
			"Each such field gets a row in TestApplyRecreatesForEveryChangedSpecField that mutates NOTHING, "+
			"so the row passes while observing nothing. Give the field a non-zero value in supportedSpec "+
			"(if this backend maps it) or in unsupportedFieldCases (if it warns about it).", same)
	}
}

// TestApplyRecreatesForEveryChangedSpecField is the guard on the apply
// fingerprint, one api.DeploySpec field at a time.
//
// Each row asserts BOTH directions against the same deployment, and that pairing
// is the whole design:
//
//	(1) re-applying the UNCHANGED spec must not create, remove or start anything;
//	(2) applying the spec with exactly ONE field changed must recreate.
//
// Either assertion alone is worthless here. (2) alone passes for every field on
// a backend that recreates unconditionally — which is what this backend did
// before the fix, so a table of (2)-only rows would have been green against the
// bug it was written for. (1) alone passes for a backend that never recreates
// anything, including one whose fingerprint ignores the field under test. Only
// together do they pin "recreates exactly when the configuration changed".
//
// Note what decides (2): the third Apply runs against a live set created from
// the BASE spec, so there is no companion in it and no attachment shape to
// disqualify the fast path. The reuse decision therefore rests on the
// fingerprint alone — with one honest exception, Replicas, which is also caught
// by reusableInstances' container-count check.
func TestApplyRecreatesForEveryChangedSpecField(t *testing.T) {
	base, changed := idempotenceBase(), changedSpec()
	specType := reflect.TypeOf(api.DeploySpec{})
	for i := 0; i < specType.NumField(); i++ {
		field := specType.Field(i)
		if field.PkgPath != "" || field.Name == "Name" {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			mutated := base
			reflect.ValueOf(&mutated).Elem().Field(i).Set(reflect.ValueOf(changed).Field(i))

			f := &fakeDocker{}
			b := newTestBackend(t, f)
			// Telemetry's row deploys a collector companion, which this backend
			// refuses without an agent image. Set for every row so no row differs
			// from the others in backend configuration.
			b.agentImage = "cornus:latest"
			ctx := context.Background()

			if _, err := b.Apply(ctx, base); err != nil {
				t.Fatalf("first apply of the base spec: %v", err)
			}
			created1, removed1 := f.appCreates(), f.removals()
			if created1 != 1 {
				t.Fatalf("first apply created %d app containers, want 1 — the counters this test reads are not live", created1)
			}

			if _, err := b.Apply(ctx, base); err != nil {
				t.Fatalf("repeat apply of the base spec: %v", err)
			}
			created2, removed2 := f.appCreates(), f.removals()
			if created2 != created1 || removed2 != removed1 {
				t.Fatalf("re-applying the UNCHANGED spec created %d and removed %d container(s), want 0 of each:"+
					" nothing about the deployment changed, so nothing may be replaced",
					created2-created1, removed2-removed1)
			}

			if _, err := b.Apply(ctx, mutated); err != nil {
				t.Fatalf("apply with %s changed: %v", field.Name, err)
			}
			created3, removed3 := f.appCreates(), f.removals()
			wantCreated, wantRemoved := deploy.Replicas(mutated), deploy.Replicas(base)
			if created3-created2 != wantCreated || removed3-removed2 != wantRemoved {
				t.Fatalf("changing %s created %d and removed %d app container(s), want %d created and %d removed:"+
					" the deployment's configuration changed, so the containers must be replaced"+
					" — a spec fingerprint blind to this field would leave the old ones running",
					field.Name, created3-created2, removed3-removed2, wantCreated, wantRemoved)
			}
		})
	}
}

// TestApplyRecreatesWhenTheImageContentChanged covers the half of the decision
// no api.DeploySpec field can reach: the ref is unchanged and the BYTES behind
// it are not. That is not an exotic case, it is the ordinary one — `compose up
// --build` rebuilds the same tag on every save — and a fingerprint that compared
// image REFS would answer "unchanged" and leave the workload running the
// previous build. That failure would be worse than the recreate-always
// behaviour this whole change replaces, because it is silent.
func TestApplyRecreatesWhenTheImageContentChanged(t *testing.T) {
	const ref = "localhost:5000/app:v1"
	f := &fakeDocker{imageIDs: map[string]string{ref: "sha256:build-1"}}
	b := newTestBackend(t, f)
	ctx := context.Background()
	spec := api.DeploySpec{Name: "web", Image: ref}

	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if n := f.removals(); n != 0 {
		t.Fatalf("re-applying with the same image content removed %d container(s), want 0", n)
	}

	// Same tag, rebuilt: the daemon now resolves the ref to different content.
	f.mu.Lock()
	f.imageIDs[ref] = "sha256:build-2"
	f.mu.Unlock()

	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("apply after the rebuild: %v", err)
	}
	if n := f.removals(); n != 1 {
		t.Fatalf("the image content changed under an unchanged tag and %d container(s) were removed, want 1:"+
			" the workload is still running the previous build", n)
	}
	if n := f.appCreates(); n != 2 {
		t.Fatalf("app containers created = %d, want 2 (one per genuine change)", n)
	}
}

// TestApplyStartsAStoppedInstanceWithoutRecreating pins what "reuse" must mean
// for a project that is down: `up` after `stop` brings the SAME containers back,
// it does not replace them. Without this, the fast path would return a
// successful-looking status for a deployment that is not running at all.
func TestApplyStartsAStoppedInstanceWithoutRecreating(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"}

	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	const id = "id-cornus-web-0"
	f.mu.Lock()
	if _, ok := f.containers[id]; !ok {
		f.mu.Unlock()
		t.Fatalf("expected the instance to be named %s; containers = %v", id, f.containers)
	}
	f.containers[id]["State"] = "exited" // as if `compose stop` had run
	f.started = nil
	f.mu.Unlock()

	st, err := b.Apply(ctx, spec)
	if err != nil {
		t.Fatalf("apply over a stopped instance: %v", err)
	}
	f.mu.Lock()
	started, removed, created := append([]string(nil), f.started...), len(f.removed), len(f.created)
	f.mu.Unlock()
	if removed != 0 || created != 1 {
		t.Fatalf("apply over a stopped instance removed %d and created %d container(s), want 0 removed and no second create", removed, created-1)
	}
	if len(started) != 1 || started[0] != id {
		t.Fatalf("started = %v, want [%s]: an up-to-date instance that is down must be started, not replaced", started, id)
	}
	if len(st.Instances) != 1 {
		t.Fatalf("status = %+v, want the one reused instance", st)
	}
}

// TestApplyDoesNotReuseAcrossACompanion proves the companion rule through the
// real Apply path rather than only through the pure predicate. A companion
// (egress, client mounts, remote mode, a telemetry collector) carries
// configuration that is NOT in the spec, so the spec fingerprint cannot vouch
// for the deployment as a whole; its presence must send Apply down the full
// recreate path even when the app container's own fingerprint still matches.
func TestApplyDoesNotReuseAcrossACompanion(t *testing.T) {
	f := &fakeDocker{}
	b := newTestBackend(t, f)
	ctx := context.Background()
	spec := api.DeploySpec{Name: "web", Image: "localhost:5000/app:v1"}

	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// A companion of this deployment, as ApplyWithEgress/ApplyWithMounts would
	// have left behind: same app label, plus the role label that marks it.
	f.mu.Lock()
	f.containers["id-companion"] = map[string]any{
		"Id": "id-companion", "Image": "cornus:latest", "State": "running",
		"Labels": map[string]string{deploy.LabelApp: "web", labelRole: roleEgressCaretaker},
	}
	f.mu.Unlock()

	if _, err := b.Apply(ctx, spec); err != nil {
		t.Fatalf("apply beside a companion: %v", err)
	}
	if n := f.removals(); n < 2 {
		t.Fatalf("removed %d container(s) beside a companion, want the app instance AND the companion:"+
			" reuse must not keep a deployment whose companion the spec fingerprint cannot describe", n)
	}
}

// TestReusableInstances is the unit table for the decision itself. It matters
// separately from the Apply-level tests because it is the one place each branch
// can be exercised in isolation — and because reusableInstances is a pure
// function, breaking a branch of it to check that a row observes it cannot reach
// a single container removal.
func TestReusableInstances(t *testing.T) {
	const hash = "abc123"
	app := func(id, h string) containerSummary {
		return containerSummary{ID: id, Labels: map[string]string{specHashLabel: h}}
	}
	companion := containerSummary{ID: "id-companion", Labels: map[string]string{labelRole: roleEgressCaretaker}}
	// A 64-hex id whose 12-hex abbreviation is what an operator would pin.
	const fullID = "1f0a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"

	for _, tc := range []struct {
		name     string
		live     []containerSummary
		replicas int
		hash     string
		selfID   string
		want     bool
	}{
		{"unchanged single replica", []containerSummary{app("a", hash)}, 1, hash, "", true},
		{"unchanged two replicas", []containerSummary{app("a", hash), app("b", hash)}, 2, hash, "", true},
		{"nothing deployed yet", nil, 1, hash, "", false},
		{"fingerprint differs", []containerSummary{app("a", "other")}, 1, hash, "", false},
		{"one replica of two differs", []containerSummary{app("a", hash), app("b", "other")}, 2, hash, "", false},
		{"no fingerprint on the container", []containerSummary{{ID: "a"}}, 1, hash, "", false},
		// An unknown DESIRED hash, against a container that carries no hash of its
		// own — the shape a "" == "" comparison would wave through. Pairing it with
		// a HASHED container instead would be decided by the hash comparison, and
		// the empty-hash guard could then be deleted with this row still green.
		{"desired fingerprint unknown", []containerSummary{{ID: "a"}}, 1, "", "", false},
		{"scaling up", []containerSummary{app("a", hash)}, 2, hash, "", false},
		{"scaling down", []containerSummary{app("a", hash), app("b", hash)}, 1, hash, "", false},
		// replicas <= 0 against an EMPTY live set: with the guard gone, zero app
		// containers would equal zero desired replicas and the function would call a
		// deployment with nothing in it up to date.
		{"replicas not yet resolved", nil, 0, hash, "", false},
		// One app instance (exactly the desired count) PLUS a companion: the app
		// count and every hash agree, so nothing but the companion rule itself can
		// refuse this. Counting the companion toward `replicas` instead would let
		// the arithmetic refuse it and leave the rule untested.
		{"a companion is present", []containerSummary{app("a", hash), companion}, 1, hash, "", false},
		{"the instance is this server", []containerSummary{app("a", hash)}, 1, hash, "a", false},
		{"this server, pinned by short id", []containerSummary{app(fullID, hash)}, 1, hash, fullID[:12], false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keep, ok := reusableInstances(tc.live, tc.replicas, tc.hash, tc.selfID)
			if ok != tc.want {
				t.Fatalf("reusableInstances = %v, want %v", ok, tc.want)
			}
			if ok && len(keep) != len(tc.live) {
				t.Fatalf("kept %d of %d live containers; every reused instance must be returned so Apply can start it", len(keep), len(tc.live))
			}
			if !ok && keep != nil {
				t.Fatalf("kept %v while refusing reuse", keep)
			}
		})
	}
}

// TestFingerprintSpecIsStableAndTotal states the two properties the Apply-level
// tests depend on but cannot see directly: the same input hashes the same way
// twice (otherwise nothing would ever be reused), and the image content id is
// part of the input (otherwise a rebuilt tag would be invisible).
func TestFingerprintSpecIsStableAndTotal(t *testing.T) {
	spec := supportedSpec()
	a, err := fingerprintSpec(spec, "sha256:one")
	if err != nil {
		t.Fatalf("fingerprintSpec: %v", err)
	}
	b, err := fingerprintSpec(supportedSpec(), "sha256:one")
	if err != nil {
		t.Fatalf("fingerprintSpec: %v", err)
	}
	if a != b {
		t.Fatalf("the same spec hashed to %s and %s; an unstable fingerprint makes every apply a recreate", a, b)
	}
	c, err := fingerprintSpec(spec, "sha256:two")
	if err != nil {
		t.Fatalf("fingerprintSpec: %v", err)
	}
	if c == a {
		t.Fatal("the image content id does not reach the fingerprint: a rebuilt tag would be treated as unchanged")
	}
}
