package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// TestLoadKeepsAllServicesRegardlessOfProfile checks that Load/LoadWithOptions
// never filter by profile: every service, gated or not, stays in the returned
// Project. Profile gating is a ProjectProfileView concern (see
// TestProjectProfileView).
func TestLoadKeepsAllServicesRegardlessOfProfile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := `services:
  web:
    image: web
    depends_on: [cache]
  cache:
    image: cache
    profiles: [prod]
  tools:
    image: tools
    profiles: [dev]
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := LoadWithOptions(LoadOptions{}, file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := serviceNames(p); !equalNames(got, []string{"cache", "tools", "web"}) {
		t.Fatalf("services = %v, want [cache tools web]", got)
	}
}

// TestProjectProfileView checks Project.View's profile gating: a profiled
// service is excluded from the view unless its profile is active, a service
// with no profiles is always included, and a profile-gated dependency of an
// included service is pulled in transitively. The underlying Project is left
// untouched, so a second View with a different profile set still sees every
// service.
func TestProjectProfileView(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := `services:
  web:
    image: web
    depends_on: [cache]
  cache:
    image: cache
    profiles: [prod]
  tools:
    image: tools
    profiles: [dev]
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadWithOptions(LoadOptions{}, file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	viewNames := func(v *ProjectProfileView) []string {
		out := make([]string, 0, len(v.Services()))
		for n := range v.Services() {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}

	// No active profile: web (always) + cache (pulled in as web's dependency
	// despite being prod-gated); tools excluded.
	v := p.View(nil)
	if got := viewNames(v); !equalNames(got, []string{"cache", "web"}) {
		t.Fatalf("no-profile view = %v, want [cache web]", got)
	}
	if v.Project() != p {
		t.Fatalf("View.Project() = %p, want the source Project %p", v.Project(), p)
	}

	// --profile dev: adds tools.
	v = p.View([]string{"dev"})
	if got := viewNames(v); !equalNames(got, []string{"cache", "tools", "web"}) {
		t.Fatalf("dev-profile view = %v, want [cache tools web]", got)
	}

	// The underlying Project is never mutated by View: it still has every
	// service after both calls above.
	if got := serviceNames(p); !equalNames(got, []string{"cache", "tools", "web"}) {
		t.Fatalf("source Project services = %v, want [cache tools web] (View must not mutate it)", got)
	}

	// Order/Plan restrict the full project's dependency order / plans to the
	// view's selected services, while Project.Order/Project.Plan (the complete
	// model) still see everything — this is what lets `compose ps` report a
	// service regardless of the active profile set.
	noProfile := p.View(nil)
	order, err := noProfile.Order()
	if err != nil {
		t.Fatalf("View.Order: %v", err)
	}
	if !equalNames(order, []string{"cache", "web"}) {
		t.Fatalf("no-profile View.Order = %v, want [cache web]", order)
	}
	plans, err := noProfile.Plan("proj")
	if err != nil {
		t.Fatalf("View.Plan: %v", err)
	}
	if got := planNames(plans); !equalNames(got, []string{"cache", "web"}) {
		t.Fatalf("no-profile View.Plan = %v, want [cache web]", got)
	}

	fullOrder, err := p.Order()
	if err != nil {
		t.Fatalf("Project.Order: %v", err)
	}
	if !equalNames(fullOrder, []string{"cache", "tools", "web"}) {
		t.Fatalf("Project.Order = %v, want [cache tools web]", fullOrder)
	}
	fullPlans, err := p.Plan("proj")
	if err != nil {
		t.Fatalf("Project.Plan: %v", err)
	}
	if got := planNames(fullPlans); !equalNames(got, []string{"cache", "tools", "web"}) {
		t.Fatalf("Project.Plan = %v, want [cache tools web]", got)
	}
}

// TestViewExcludesMalformedInactiveService pins the fix for the profile-view
// regression: a service that is invalid at translate/order time but gated
// behind an INACTIVE profile must not fail the selected view's Order/Plan —
// only commands that actually activate its profile should ever touch it. Here
// `broken` has an unparseable mem_limit and forms a self-cycle; without the
// profile active, View(nil).Order()/Plan() must succeed over {web} alone.
func TestViewExcludesMalformedInactiveService(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := `services:
  web:
    image: web
  broken:
    image: broken
    profiles: [debug]
    mem_limit: "not-a-size"
    depends_on: [broken]
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadWithOptions(LoadOptions{}, file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// debug profile inactive: the view is {web}; Order/Plan must not touch broken.
	v := p.View(nil)
	order, err := v.Order()
	if err != nil {
		t.Fatalf("View.Order with malformed inactive service: %v", err)
	}
	if !equalNames(order, []string{"web"}) {
		t.Fatalf("View.Order = %v, want [web]", order)
	}
	plans, err := v.Plan("proj")
	if err != nil {
		t.Fatalf("View.Plan with malformed inactive service: %v", err)
	}
	if got := planNames(plans); !equalNames(got, []string{"web"}) {
		t.Fatalf("View.Plan = %v, want [web]", got)
	}

	// Activating the profile brings broken in, and its malformed config now
	// surfaces as an error (a command that opts into the profile should see it).
	if _, err := p.View([]string{"debug"}).Plan("proj"); err == nil {
		t.Fatal("View([debug]).Plan should fail on the malformed service, got nil")
	}
}

func planNames(plans map[string]ServicePlan) []string {
	out := make([]string, 0, len(plans))
	for n := range plans {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestViewCrossProfileDependencyPulledIn pins the compose-spec cross-profile
// depends_on rule: when an enabled service depends on a service gated by a
// DIFFERENT, inactive profile, the dependency is auto-enabled (started) rather
// than errored or dropped. Here `web` (profile frontend) depends on `db`
// (profile backend); activating only frontend must still deploy db (ordered
// before web), while an undepended backend service stays excluded.
func TestViewCrossProfileDependencyPulledIn(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := `services:
  web:
    image: web
    profiles: [frontend]
    depends_on: [db]
  db:
    image: db
    profiles: [backend]
  lonely:
    image: lonely
    profiles: [backend]
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadWithOptions(LoadOptions{}, file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	v := p.View([]string{"frontend"})
	got := make([]string, 0)
	for n := range v.Services() {
		got = append(got, n)
	}
	sort.Strings(got)
	// db pulled in via web's depends_on despite backend being inactive; lonely
	// (also backend, but undepended) excluded.
	if !equalNames(got, []string{"db", "web"}) {
		t.Fatalf("View([frontend]).Services = %v, want [db web] (cross-profile dep pulled in, lonely excluded)", got)
	}
	// The pulled-in dependency deploys, ordered before its dependent.
	order, err := v.Order()
	if err != nil {
		t.Fatalf("View.Order: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"db", "web"}) {
		t.Fatalf("View.Order = %v, want [db web] (dependency first)", order)
	}
	plans, err := v.Plan("proj")
	if err != nil {
		t.Fatalf("View.Plan: %v", err)
	}
	if _, ok := plans["db"]; !ok {
		t.Fatalf("db missing from plans %v (cross-profile dependency not deployed)", planNames(plans))
	}
}

// TestPlanForStatusTolerant pins that PlanForStatus (used by compose ps) lists
// every service even when one is malformed: the good service gets a full plan,
// the bad one a minimal fallback (resource name + image) plus a collected
// error, and the call never fails.
func TestPlanForStatusTolerant(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := `services:
  good:
    image: good:1
  bad:
    image: bad:1
    mem_limit: "not-a-size"
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadWithOptions(LoadOptions{}, file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	order, plans, errs := p.PlanForStatus("proj")
	if !equalNames(order, []string{"bad", "good"}) {
		t.Fatalf("order = %v, want [bad good] (all services listed)", order)
	}
	if _, ok := errs["bad"]; !ok || len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly {bad: ...}", errs)
	}
	// The bad service still has a row with resource name + image from the raw
	// service (no translation needed for those).
	if got := plans["bad"].Resource; got != "proj-bad" {
		t.Errorf("bad resource = %q, want proj-bad", got)
	}
	if got := plans["bad"].Spec.Image; got != "bad:1" {
		t.Errorf("bad image = %q, want bad:1", got)
	}
	// The good service translates fully.
	if got := plans["good"].Spec.Image; got != "good:1" {
		t.Errorf("good image = %q, want good:1", got)
	}
}

// TestPlanForStatusCycleDegradesOrder pins that a dependency cycle (which fails
// Order) degrades PlanForStatus to sorted-name order instead of erroring, so ps
// still lists every service.
func TestPlanForStatusCycleDegradesOrder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	body := `services:
  a:
    image: a
    depends_on: [b]
  b:
    image: b
    depends_on: [a]
`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadWithOptions(LoadOptions{}, file)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Order() itself errors on the cycle...
	if _, err := p.Order(); err == nil {
		t.Fatal("Order should error on a cycle")
	}
	// ...but PlanForStatus degrades to sorted order and lists both.
	order, plans, _ := p.PlanForStatus("proj")
	if !equalNames(order, []string{"a", "b"}) {
		t.Fatalf("order = %v, want [a b] (sorted fallback)", order)
	}
	if len(plans) != 2 {
		t.Fatalf("plans = %v, want both services", planNames(plans))
	}
}

func serviceNames(p *Project) []string {
	out := make([]string, 0, len(p.Services()))
	for n := range p.Services() {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func equalNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
