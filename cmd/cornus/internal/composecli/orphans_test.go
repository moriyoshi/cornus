package composecli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/api"

	"github.com/alecthomas/kong"
)

// fakeOrphanClient is a scripted orphanClient: List returns a canned deployment
// set (or an error), Delete records the resource names removed, and Status feeds
// reportTeardown — nil poller reports each deployment already gone so the wait
// returns at once.
type fakeOrphanClient struct {
	list    []api.DeployStatus
	listErr error
	deleted []string
	poller  *scriptedPoller
}

func (f *fakeOrphanClient) List(context.Context) ([]api.DeployStatus, error) {
	return f.list, f.listErr
}

func (f *fakeOrphanClient) Delete(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeOrphanClient) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	if f.poller != nil {
		return f.poller.Status(ctx, name)
	}
	return api.DeployStatus{Name: name}, nil // no instances: already gone
}

func owned(name, project string) api.DeployStatus {
	return api.DeployStatus{Name: name, Origin: &api.Origin{Project: project}}
}

// TestFindOrphans pins the orphan predicate: a deployment is an orphan only when
// it is attributed to THIS project (Origin.Project) AND its resource name is not
// a service currently in the Compose file. A deployment with no origin, or one
// owned by another project, or one that matches a known service, is never an
// orphan. The result is sorted.
func TestFindOrphans(t *testing.T) {
	list := []api.DeployStatus{
		owned("demo-web", "demo"),       // known service -> not an orphan
		owned("demo-oldcache", "demo"),  // orphan
		owned("demo-api", "demo"),       // orphan (renamed away)
		owned("other-worker", "other"),  // another project -> never touched
		{Name: "legacy", Origin: nil},   // no lineage -> cannot claim
		{Name: "demo-api", Origin: nil}, // same name but unattributed -> not an orphan
	}
	known := map[string]struct{}{"demo-web": {}, "demo-db": {}}

	got := findOrphans(list, "demo", known)
	want := []string{"demo-api", "demo-oldcache"} // sorted, project-owned, unknown
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findOrphans = %v; want %v", got, want)
	}

	// A project with no orphans yields an empty (nil) result.
	if got := findOrphans([]api.DeployStatus{owned("demo-web", "demo")}, "demo", known); len(got) != 0 {
		t.Fatalf("findOrphans (no orphans) = %v; want none", got)
	}
}

// TestRemoveOrphans covers the down/up --remove-orphans teardown: it deletes every
// project-owned orphan (sorted), waits for each to drain when wait is set, and a
// List failure is surfaced as an error rather than silently skipped.
func TestRemoveOrphans(t *testing.T) {
	known := map[string]struct{}{"demo-web": {}}

	t.Run("deletes orphans, waits for teardown", func(t *testing.T) {
		f := &fakeOrphanClient{list: []api.DeployStatus{
			owned("demo-web", "demo"),  // kept
			owned("demo-gone", "demo"), // orphan
			owned("other-x", "other"),  // other project
		}}
		var out bytes.Buffer
		if err := removeOrphans(context.Background(), f, "demo", known, testDriver(&out), true); err != nil {
			t.Fatalf("removeOrphans: %v", err)
		}
		if !reflect.DeepEqual(f.deleted, []string{"demo-gone"}) {
			t.Fatalf("deleted = %v; want [demo-gone]", f.deleted)
		}
		if got := out.String(); !strings.Contains(got, "demo-gone  removed") {
			t.Fatalf("teardown output missing the removed line; got:\n%s", got)
		}
	})

	t.Run("no-wait reports removal without polling", func(t *testing.T) {
		f := &fakeOrphanClient{list: []api.DeployStatus{owned("demo-gone", "demo")}}
		var out bytes.Buffer
		if err := removeOrphans(context.Background(), f, "demo", known, testDriver(&out), false); err != nil {
			t.Fatalf("removeOrphans: %v", err)
		}
		if !reflect.DeepEqual(f.deleted, []string{"demo-gone"}) {
			t.Fatalf("deleted = %v; want [demo-gone]", f.deleted)
		}
		if got := out.String(); !strings.Contains(got, "orphan removed") {
			t.Fatalf("output missing 'orphan removed'; got:\n%s", got)
		}
	})

	t.Run("nothing to remove is a silent no-op", func(t *testing.T) {
		f := &fakeOrphanClient{list: []api.DeployStatus{owned("demo-web", "demo")}}
		var out bytes.Buffer
		if err := removeOrphans(context.Background(), f, "demo", known, testDriver(&out), true); err != nil {
			t.Fatalf("removeOrphans: %v", err)
		}
		if len(f.deleted) != 0 {
			t.Fatalf("deleted = %v; want none", f.deleted)
		}
	})

	t.Run("list failure is an error", func(t *testing.T) {
		f := &fakeOrphanClient{listErr: errors.New("boom")}
		var out bytes.Buffer
		if err := removeOrphans(context.Background(), f, "demo", known, testDriver(&out), true); err == nil {
			t.Fatal("removeOrphans: want error on List failure, got nil")
		}
		if len(f.deleted) != 0 {
			t.Fatalf("deleted = %v; want none (List failed before any delete)", f.deleted)
		}
	})
}

// TestWarnOrphans covers the up advisory: it warns (naming the orphans and the
// flag) when orphans exist, stays silent when there are none, and swallows a List
// failure so it never blocks up.
func TestWarnOrphans(t *testing.T) {
	known := map[string]struct{}{"demo-web": {}}

	t.Run("warns and names the orphans", func(t *testing.T) {
		f := &fakeOrphanClient{list: []api.DeployStatus{
			owned("demo-web", "demo"),
			owned("demo-gone", "demo"),
		}}
		var out bytes.Buffer
		warnOrphans(context.Background(), f, "demo", known, testDriver(&out))
		got := out.String()
		for _, want := range []string{"demo-gone", "remove-orphans"} {
			if !strings.Contains(got, want) {
				t.Errorf("warning missing %q; got:\n%s", want, got)
			}
		}
		if len(f.deleted) != 0 {
			t.Fatalf("warnOrphans deleted %v; must never remove anything", f.deleted)
		}
	})

	t.Run("silent when no orphans", func(t *testing.T) {
		f := &fakeOrphanClient{list: []api.DeployStatus{owned("demo-web", "demo")}}
		var out bytes.Buffer
		warnOrphans(context.Background(), f, "demo", known, testDriver(&out))
		if got := out.String(); got != "" {
			t.Fatalf("warnOrphans printed %q; want nothing", got)
		}
	})

	t.Run("list failure is swallowed", func(t *testing.T) {
		f := &fakeOrphanClient{listErr: errors.New("boom")}
		var out bytes.Buffer
		warnOrphans(context.Background(), f, "demo", known, testDriver(&out)) // must not panic
		if got := out.String(); got != "" {
			t.Fatalf("warnOrphans printed %q on List failure; want nothing", got)
		}
	})
}

// TestRemoveOrphansFlag pins that both `up` and `down` parse --remove-orphans.
func TestRemoveOrphansFlag(t *testing.T) {
	t.Run("down", func(t *testing.T) {
		var cli struct {
			Down DownCmd `kong:"cmd"`
		}
		parser, err := kong.New(&cli, kong.Name("cornus"))
		if err != nil {
			t.Fatalf("kong.New: %v", err)
		}
		if _, err := parser.Parse([]string{"down", "--remove-orphans"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !cli.Down.RemoveOrphans {
			t.Fatal("down --remove-orphans did not set RemoveOrphans")
		}
	})

	t.Run("up", func(t *testing.T) {
		var cli struct {
			Up UpCmd `kong:"cmd"`
		}
		parser, err := kong.New(&cli, kong.Name("cornus"))
		if err != nil {
			t.Fatalf("kong.New: %v", err)
		}
		if _, err := parser.Parse([]string{"up", "--remove-orphans"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !cli.Up.RemoveOrphans {
			t.Fatal("up --remove-orphans did not set RemoveOrphans")
		}
	})
}
