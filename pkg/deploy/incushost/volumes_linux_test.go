//go:build linux

package incushost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// volumeSpec is the shape both halves of the volume story are tested against: a
// deployment with one anonymous and one named volume.
func volumeSpec() api.DeploySpec {
	return api.DeploySpec{
		Name:  "web",
		Image: "localhost:5000/app:v1",
		Volumes: []api.VolumeSpec{
			{Target: "/var/cache"},
			{Name: "shared", Target: "/var/lib/shared", Size: "1Gi", ReadOnly: true},
		},
	}
}

// TestApplyProvisionsAndAttachesManagedVolumes is the regression test for the
// last of this backend's silent drops: `volumes:` was accepted, warned about
// once, and then nothing was provisioned — so the workload wrote into an
// ordinary directory in its own root disk and lost it on the next Apply.
//
// A managed volume is an incus custom storage volume plus the disk device that
// attaches it (pool + volume-name source, the same shape agentVolumeDeviceFor
// already uses; internal/server/device/disk.go:1090-1147). Both halves have to
// line up exactly — a device naming a volume that was never created leaves the
// instance unable to start — which is why this asserts them together.
func TestApplyProvisionsAndAttachesManagedVolumes(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	b.pool = "tank"
	if _, err := b.Apply(context.Background(), volumeSpec()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	anon := anonVolumeName("web", 0, "/var/cache")
	named := namedVolumeName("shared")
	for _, want := range []string{anon, named} {
		cfg, ok := f.volumes["tank/"+want]
		if !ok {
			t.Fatalf("volume %q was not created; have %v", want, f.volumeNames())
		}
		if cfg["security.shifted"] != "true" {
			t.Errorf("volume %q: security.shifted = %q, want true", want, cfg["security.shifted"])
		}
		if cfg[configKeyPrefix+deploy.LabelApp] != "web" {
			t.Errorf("volume %q carries no ownership metadata: %v", want, cfg)
		}
	}
	if got := f.volumes["tank/"+named]["size"]; got != "1073741824" {
		t.Errorf("named volume size = %q, want 1Gi in bytes", got)
	}
	if _, ok := f.volumes["tank/"+anon]["size"]; ok {
		t.Errorf("an unsized volume must carry no quota, got %v", f.volumes["tank/"+anon])
	}

	post := f.insts["cornus-web-0"]
	want := map[string]map[string]string{
		"cornus-vol-0": {"type": "disk", "pool": "tank", "source": anon, "path": "/var/cache"},
		"cornus-vol-1": {"type": "disk", "pool": "tank", "source": named, "path": "/var/lib/shared", "readonly": "true"},
	}
	if !reflect.DeepEqual(post.Devices, want) {
		t.Errorf("devices =\n%#v\nwant\n%#v", post.Devices, want)
	}
}

// TestDeleteReapsAnonymousVolumesButKeepsNamedOnes is the hard half: Delete gets
// a NAME, not a spec, so without the anonymous names recorded on the instance
// nothing at delete time could ever find them and every deploy/delete cycle
// would leak a storage volume.
//
// The asymmetry is deliberate and matches docker (and the host backends' volume
// store): an anonymous volume belongs to this deployment and dies with it; a
// named volume is shared and project-scoped, and outliving the deployment is the
// entire reason to name one.
func TestDeleteReapsAnonymousVolumesButKeepsNamedOnes(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	b.pool = "tank"
	spec := volumeSpec()
	spec.Replicas = 2
	if _, err := b.Apply(context.Background(), spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Both replicas get their OWN anonymous volume; the named one is shared.
	anon0, anon1 := anonVolumeName("web", 0, "/var/cache"), anonVolumeName("web", 1, "/var/cache")
	if anon0 == anon1 {
		t.Fatal("replicas must not share anonymous storage")
	}
	named := namedVolumeName("shared")

	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, gone := range []string{anon0, anon1} {
		if _, ok := f.volumes["tank/"+gone]; ok {
			t.Errorf("anonymous volume %q leaked past Delete", gone)
		}
	}
	if _, ok := f.volumes["tank/"+named]; !ok {
		t.Errorf("named volume %q was reaped; a named volume must survive its deployment", named)
	}
}

// TestDeleteReapsVolumesOnlyAfterTheInstancesAreGone pins the ordering: Incus
// refuses to delete a storage volume that is still attached, so reaping before
// the instance is deleted would fail every time.
func TestDeleteReapsVolumesOnlyAfterTheInstancesAreGone(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	b.pool = "tank"
	if _, err := b.Apply(context.Background(), volumeSpec()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.calls = nil
	if err := b.Delete(context.Background(), "web"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	lastDelete, firstVolume := -1, -1
	for i, c := range f.calls {
		if strings.HasPrefix(c, "DeleteInstance ") {
			lastDelete = i
		}
		if firstVolume < 0 && strings.HasPrefix(c, "DeleteVolume ") {
			firstVolume = i
		}
	}
	if lastDelete < 0 || firstVolume < 0 {
		t.Fatalf("expected both instance and volume deletions, got %v", f.calls)
	}
	if firstVolume < lastDelete {
		t.Errorf("a volume was reaped while an instance still held it: %v", f.calls)
	}
}

// TestRemoveVolumeIsDeriveOnlyAndDeleteIfExists pins deploy.VolumeRemover, the
// path `compose down --volumes` takes. It gets only the LOGICAL name, so the
// incus volume name has to be a pure function of that name — the same function
// Apply used — or the removal would silently target nothing.
func TestRemoveVolumeIsDeriveOnlyAndDeleteIfExists(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	b.pool = "tank"
	if _, err := b.Apply(context.Background(), volumeSpec()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := b.RemoveVolume(context.Background(), "shared"); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
	if _, ok := f.volumes["tank/"+namedVolumeName("shared")]; ok {
		t.Error("RemoveVolume did not remove the volume Apply created")
	}
	// Delete-if-exists, per the interface contract.
	if err := b.RemoveVolume(context.Background(), "shared"); err != nil {
		t.Errorf("removing an absent volume must succeed, got %v", err)
	}
	if err := b.RemoveVolume(context.Background(), "never-existed"); err != nil {
		t.Errorf("removing an unknown volume must succeed, got %v", err)
	}
	if err := b.RemoveVolume(context.Background(), ""); err == nil {
		t.Error("an empty volume name must be rejected rather than deriving a name from nothing")
	}
}

// TestVolumeFailuresSurfaceRatherThanStrandingTheDeployment pins the error
// paths. A volume that cannot be provisioned must fail the Apply — creating the
// instance anyway would leave it with a disk device naming storage that is not
// there, which fails at START, far from the cause. A volume that cannot be
// reaped must fail the Delete for the same reason: reporting success would hide
// a leak that nothing later can find.
func TestVolumeFailuresSurfaceRatherThanStrandingTheDeployment(t *testing.T) {
	t.Run("provisioning", func(t *testing.T) {
		f := newFakeConn()
		f.volumeErr = errors.New("pool is full")
		b := testBackend(f)
		b.pool = "tank"
		_, err := b.Apply(context.Background(), volumeSpec())
		if err == nil || !strings.Contains(err.Error(), "pool is full") {
			t.Fatalf("Apply error = %v, want the daemon's reason", err)
		}
		if len(f.insts) != 0 {
			t.Errorf("an instance was created against storage that does not exist: %v", f.insts)
		}
	})
	t.Run("reaping", func(t *testing.T) {
		f := newFakeConn()
		b := testBackend(f)
		b.pool = "tank"
		if _, err := b.Apply(context.Background(), volumeSpec()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		f.volumeDeleteErr = errors.New("volume busy")
		if err := b.Delete(context.Background(), "web"); err == nil || !strings.Contains(err.Error(), "volume busy") {
			t.Fatalf("Delete error = %v, want the daemon's reason", err)
		}
		if err := b.RemoveVolume(context.Background(), "shared"); err == nil || !strings.Contains(err.Error(), "volume busy") {
			t.Fatalf("RemoveVolume error = %v, want the daemon's reason", err)
		}
	})
}

// TestVolumePlanNamesAreStableAndDistinct pins the naming, which is load-bearing
// twice over: Apply and buildDevices must agree on it within one deploy, and
// RemoveVolume must reproduce it from the logical name alone, later, with no
// spec in hand.
func TestVolumePlanNamesAreStableAndDistinct(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Volumes: []api.VolumeSpec{
		{Target: "/a"}, {Target: "/b"}, {Name: "shared", Target: "/c"},
	}}
	plan, skipped := volumePlan(spec, 0)
	if len(skipped) != 0 {
		t.Fatalf("unexpected refusals: %v", skipped)
	}
	if len(plan) != 3 {
		t.Fatalf("plan = %v, want 3 entries", plan)
	}
	again, _ := volumePlan(spec, 0)
	if !reflect.DeepEqual(plan, again) {
		t.Error("volumePlan is not deterministic; Apply and buildDevices would disagree")
	}
	seen := map[string]bool{}
	for _, v := range plan {
		if seen[v.Volume] {
			t.Errorf("two volumes share the incus name %q", v.Volume)
		}
		seen[v.Volume] = true
		if !incusVolumeNameOK(v.Volume) {
			t.Errorf("volume name %q would be rejected by incus", v.Volume)
		}
	}
	if plan[0].Anonymous == plan[2].Anonymous {
		t.Error("a named volume must not be classified as anonymous")
	}
	// A different replica gets different anonymous storage but the SAME named one.
	other, _ := volumePlan(spec, 1)
	if other[0].Volume == plan[0].Volume {
		t.Error("two replicas share anonymous storage")
	}
	if other[2].Volume != plan[2].Volume {
		t.Error("replicas must share a named volume")
	}
}

// TestNamedVolumeNameSurvivesAnyLogicalName pins the derivation against the
// checks incusd runs on every volume create (validate.IsAPIName via
// cmd/incusd/storage_volumes.go:678-682). A compose project name is not
// constrained to what incus accepts, so the derived name is slugged and hashed
// rather than passed through — and the hash is of the ORIGINAL, so two names
// that slug alike never end up sharing storage.
func TestNamedVolumeNameSurvivesAnyLogicalName(t *testing.T) {
	logical := []string{
		"cache",
		"myproj_cache",
		"My Project/Cache",
		"UPPER",
		"a?b&c+d*e",
		"____",
		"x",
		strings.Repeat("verylongname", 20),
		"日本語",
	}
	seen := map[string]string{}
	for _, l := range logical {
		got := namedVolumeName(l)
		if !incusVolumeNameOK(got) {
			t.Errorf("namedVolumeName(%q) = %q, which incus would reject", l, got)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("namedVolumeName(%q) collides with %q at %q", l, prev, got)
		}
		seen[got] = l
		if got != namedVolumeName(l) {
			t.Errorf("namedVolumeName(%q) is not stable", l)
		}
	}
	// "app_cache" and "app-cache" slug identically; the hash is what separates them.
	if namedVolumeName("app_cache") == namedVolumeName("app-cache") {
		t.Error("names that slug alike must not share storage")
	}
}

// TestIncusVolumeNameOKMatchesIncusValidator pins the guard against
// validate.IsAPIName(value, false): at most 64 characters, no whitespace, none
// of the reserved URL characters, alphanumeric at both ends.
func TestIncusVolumeNameOKMatchesIncusValidator(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"ab", true},
		{"cornus-vol-cache-0a1b2c3d", true},
		{"a_b", true},
		{"a", false},
		{"", false},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"a b", false},
		{"a/b", false},
		{"a?b", false},
		{"a*b", false},
		{"-ab", false},
		{"ab-", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := incusVolumeNameOK(tc.name); got != tc.ok {
				t.Errorf("incusVolumeNameOK(%q) = %v, want %v", tc.name, got, tc.ok)
			}
		})
	}
}

// TestParseVolumeBytesAcceptsTheSpelledSizes pins the size parser. The field is
// documented in the kubernetes-quantity spelling ("1Gi"), which incus's own unit
// table does not contain — so the value is parsed here and handed over as a
// plain byte count rather than passed through to be rejected.
func TestParseVolumeBytesAcceptsTheSpelledSizes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1Gi", 1 << 30, true},
		{"1GiB", 1 << 30, true},
		{"512Mi", 512 << 20, true},
		{"2Ti", 2 << 40, true},
		{"1024", 1024, true},
		{"1024B", 1024, true},
		{"1GB", 1e9, true},
		{"1kB", 1000, true},
		{"1M", 1 << 20, true},
		{" 4Gi ", 4 << 30, true},
		{"", 0, false},
		{"0", 0, false},
		{"-1Gi", 0, false},
		{"lots", 0, false},
		{"Gi", 0, false},
		{"1.5Gi", 0, false},
		{"50%", 0, false},
		{"9223372036854775807Ti", 0, false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseVolumeBytes(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("parseVolumeBytes(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestVolumePlanRefusesWhatCannotBeProvisioned pins the refusal cases, each of
// which must produce a plan entry that is absent rather than one incus would
// reject — plus, for a bad size, a volume that is still provisioned (unsized) so
// the workload gets storage at the path it asked for.
func TestVolumePlanRefusesWhatCannotBeProvisioned(t *testing.T) {
	spec := api.DeploySpec{Name: "web", Volumes: []api.VolumeSpec{
		{Target: "data"},
		{Target: "/"},
		{Target: ""},
		{Target: "/ok", Size: "lots"},
	}}
	plan, skipped := volumePlan(spec, 0)
	if len(plan) != 1 || plan[0].Target != "/ok" || plan[0].Size != "" {
		t.Fatalf("plan = %#v, want only an unsized /ok", plan)
	}
	if len(skipped) != 4 {
		t.Fatalf("skipped = %#v, want one reason per bad entry", skipped)
	}
	// A deployment name long enough to blow incus's 64-character volume-name cap
	// is refused rather than sent to a create that would fail.
	long := api.DeploySpec{Name: strings.Repeat("n", 64), Volumes: []api.VolumeSpec{{Target: "/data"}}}
	if p, s := volumePlan(long, 0); len(p) != 0 || len(s) != 1 {
		t.Errorf("an over-long derived name must be refused, got plan=%#v skipped=%#v", p, s)
	}
}

// TestAnonVolumeStampRoundTrips pins the record Delete depends on: only the
// ANONYMOUS names go onto the instance (a named volume must survive Delete), and
// what is written must read back identically.
func TestAnonVolumeStampRoundTrips(t *testing.T) {
	spec := volumeSpec()
	stamp := anonVolumeStamp(spec, 0)
	got := anonVolumesOf(map[string]string{anonVolumesConfigKey: stamp})
	want := []string{anonVolumeName("web", 0, "/var/cache")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stamp round trip = %v, want %v", got, want)
	}
	if strings.Contains(stamp, namedVolumeName("shared")) {
		t.Error("a named volume must not be stamped for reaping")
	}
	// A deployment with no anonymous volumes writes no key at all, so an instance
	// created before this existed reaps nothing rather than something arbitrary.
	if s := anonVolumeStamp(api.DeploySpec{Name: "web"}, 0); s != "" {
		t.Errorf("stamp for a volume-less spec = %q, want empty", s)
	}
	if got := anonVolumesOf(map[string]string{}); got != nil {
		t.Errorf("anonVolumesOf(no key) = %v, want nil", got)
	}
	if got := anonVolumesOf(map[string]string{anonVolumesConfigKey: " a , ,b "}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("anonVolumesOf(padded) = %v", got)
	}
}
