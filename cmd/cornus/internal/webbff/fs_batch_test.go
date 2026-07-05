package webbff

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFsBatchReportsEveryItem: one failing item must not strand the ones after it, and
// the caller has to be able to tell which landed. A batch that aborted on the first
// error, or that answered with a single status code, could not express "three of five".
func TestFsBatchReportsEveryItem(t *testing.T) {
	s, projectDir, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(projectDir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var out fsBatchResponse
	body := `{"to":"proj-web/data","items":[{"from":"project/a.txt"},{"from":"project/missing.txt"},{"from":"project/b.txt"}]}`
	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("a batch answers 200 with per-item detail; got %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Result != "partial" {
		t.Errorf("result = %q, want partial", out.Result)
	}
	if len(out.Items) != 3 {
		t.Fatalf("want a record per item, got %d", len(out.Items))
	}
	if out.Items[0].Status != "ok" || out.Items[2].Status != "ok" {
		t.Errorf("items either side of the failure should have run: %+v", out.Items)
	}
	if out.Items[1].Status != "failed" || out.Items[1].Error == "" {
		t.Errorf("the missing item should be a named failure: %+v", out.Items[1])
	}
	// The two that succeeded really landed.
	for _, n := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(sharedDir, n)); err != nil {
			t.Errorf("%s did not land: %v", n, err)
		}
	}
}

// TestFsBatchKeepsSingleItemContract: a body with no "items" must behave exactly as
// before — the legacy shape, and the error as an HTTP status rather than a per-item
// record. The mock, the SPA and the older tests all depend on it.
func TestFsBatchKeepsSingleItemContract(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(projectDir, "one.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/one.txt",
		`{"to":"proj-web/data/one.txt"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("single copy: %d %s", rec.Code, rec.Body.String())
	}
	var legacy struct {
		Result  string   `json:"result"`
		Skipped []string `json:"skipped"`
		Items   []any    `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Result != "ok" || legacy.Items != nil {
		t.Errorf("single-item reply changed shape: %s", rec.Body.String())
	}
	// And a failure is still a status code, not a 200 with a buried error.
	rec = doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=project/nope.txt",
		`{"to":"proj-web/data/nope.txt"}`)
	if rec.Code == http.StatusOK {
		t.Errorf("a failing single copy must not answer 200: %s", rec.Body.String())
	}
}

// TestFsPreflightReportsRouteAndAction is the core of the report: where the work will
// run, and what it will do to the destination.
func TestFsPreflightReportsRouteAndAction(t *testing.T) {
	s, projectDir, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(projectDir, "new.txt"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "clash.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "clash.txt"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out fsPreflightResponse
	body := `{"to":"proj-web/data","items":[{"from":"project/new.txt"},{"from":"project/clash.txt"}]}`
	rec := doReq(t, s, "POST", "/.cornus/web/fs/preflight?source=virtual&op=copy", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Op != "copy" || len(out.Items) != 2 {
		t.Fatalf("report: %+v", out)
	}
	if out.Items[0].Action != "create" {
		t.Errorf("a fresh destination is a create, got %q", out.Items[0].Action)
	}
	if out.Items[1].Action != "overwrite" {
		t.Errorf("an existing file is an overwrite, got %q", out.Items[1].Action)
	}
	// Both sides resolve onto the developer's disk, so neither costs a round trip.
	for _, it := range out.Items {
		if it.Route != "here" {
			t.Errorf("%s: route = %q, want here (why: %s)", it.From, it.Route, it.Why)
		}
	}
	if out.Bytes != 6 { // 5 + 1
		t.Errorf("total bytes = %d, want 6", out.Bytes)
	}
	// Preflight must not have DONE anything.
	if _, err := os.Stat(filepath.Join(sharedDir, "new.txt")); !os.IsNotExist(err) {
		t.Error("preflight created the destination")
	}
	if b, _ := os.ReadFile(filepath.Join(sharedDir, "clash.txt")); string(b) != "existing" {
		t.Error("preflight overwrote the destination")
	}
}

// TestFsPreflightRefusesBeforeTheOperationDoes: every refusal the real call would raise
// has to show up here, or preflight is worse than useless — it would bless a transfer
// that then fails.
func TestFsPreflightRefusesBeforeTheOperationDoes(t *testing.T) {
	roDir, rwDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(roDir, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := explorerServerWithVolumes(t, fakeCornusServer(t, nil, nil),
		fmt.Sprintf("      - %s:/ro:ro\n      - %s:/rw\n", roDir, rwDir))
	roID := rootIDForPath(s, mustEval(t, roDir))
	rwID := rootIDForPath(s, mustEval(t, rwDir))

	// A MOVE off a read-only mount removes the source, so it must be refused; the same
	// COPY is fine, because only the destination is written.
	for _, tc := range []struct{ op, want string }{{"move", "refused"}, {"copy", "create"}} {
		var out fsPreflightResponse
		body := fmt.Sprintf(`{"to":%q,"items":[{"from":%q}]}`, rwID, roID+"/keep.txt")
		rec := doReq(t, s, "POST", "/.cornus/web/fs/preflight?source=virtual&op="+tc.op, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s preflight: %d %s", tc.op, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Items[0].Action != tc.want {
			t.Errorf("%s off a read-only mount: action = %q, want %q (error: %s)",
				tc.op, out.Items[0].Action, tc.want, out.Items[0].Error)
		}
		if tc.op == "move" && out.Refusals != 1 {
			t.Errorf("a refusal should be counted: %+v", out)
		}
	}
}

// TestFsPreflightWarnsAboutRelayCost: the per-file CAP is gone, so preflight must no
// longer threaten one. What is still true is that a relayed transfer pulls every byte
// through this process, where a client-side one never leaves the disk — worth saying for
// a big transfer, and worth NOT saying for a small one.
func TestFsPreflightWarnsAboutRelayCost(t *testing.T) {
	srv, proj, _ := explorerServer(t, runningUpstream(t))
	big := filepath.Join(proj, "big.bin")
	if err := os.WriteFile(big, make([]byte, relayCostWarnBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(proj, "small.bin")
	if err := os.WriteFile(small, make([]byte, maxEditableFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.cfs = &fakeContainerFS{
		statErr: fmt.Errorf("not found"),
		execFn:  func(_ string, _ []string) (ExecResult, error) { return ExecResult{ExitKnown: true}, nil },
	}

	var out fsPreflightResponse
	// project -> a container path with no bind behind it, so both must relay.
	rec := doReq(t, srv, "POST", "/.cornus/web/fs/preflight?source=virtual&op=copy",
		`{"to":"proj-web/opt","items":[{"from":"project/big.bin"},{"from":"project/small.bin"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Items[0].Route != "relay" {
		t.Fatalf("route = %q, want relay (why: %s)", out.Items[0].Route, out.Items[0].Why)
	}
	joined := strings.Join(out.Items[0].Warnings, " | ")
	if !strings.Contains(joined, "streamed through this process") {
		t.Errorf("a large relayed file should say it is going the long way; warnings: %q", joined)
	}
	// Nothing anywhere may still threaten a size cap — that was the old behaviour.
	for _, it := range out.Items {
		for _, w := range it.Warnings {
			if strings.Contains(w, "capped") || strings.Contains(w, "refused") {
				t.Errorf("%s: stale cap warning %q", it.From, w)
			}
		}
	}
	// A 10 MB file used to be over the cap. It is now unremarkable, and must be silent.
	if len(out.Items[1].Warnings) != 0 {
		t.Errorf("a small relayed file should warn about nothing, got %q", out.Items[1].Warnings)
	}
}

// TestFsPreflightSizesADirectory: a folder's report has to carry the file count and byte
// total, or a UI cannot tell a trivial drop from an expensive one.
func TestFsPreflightSizesADirectory(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	tree := filepath.Join(projectDir, "tree", "sub")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "tree", "a"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "b"), []byte("123456"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out fsPreflightResponse
	rec := doReq(t, s, "POST", "/.cornus/web/fs/preflight?source=virtual&op=copy",
		`{"to":"proj-web/data","items":[{"from":"project/tree"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	it := out.Items[0]
	if it.Kind != "dir" || it.Files != 2 || it.Bytes != 10 {
		t.Errorf("directory estimate: kind=%q files=%d bytes=%d, want dir/2/10", it.Kind, it.Files, it.Bytes)
	}
}

// TestSamePathIsRefusedNotSilentlyOk covers the drop a user makes by accident more often
// than any other: onto the folder the file is already in. It used to answer 200 "ok"
// having rewritten the file with its own contents — a success report for work that never
// happened, and the closest thing to a data-loss shape in the copy path.
func TestSamePathIsRefusedNotSilentlyOk(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(projectDir, "self.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, op := range []string{"copy", "move"} {
		rec := doReq(t, s, "POST", "/.cornus/web/fs/"+op+"?source=virtual&path=project/self.txt",
			`{"to":"project/self.txt"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s onto itself: got %d, want 400 (%s)", op, rec.Code, rec.Body.String())
		}
	}
	if b, _ := os.ReadFile(filepath.Join(projectDir, "self.txt")); string(b) != "original" {
		t.Fatalf("the file was disturbed: %q", b)
	}
}

// TestPreflightNamesTheAlreadyHereCase: the report has to say which situation this is.
// "the source and destination are the same file" is true but unhelpful for the gesture
// that actually produced it — dropping something back into its own folder.
func TestPreflightNamesTheAlreadyHereCase(t *testing.T) {
	s, projectDir, _ := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(projectDir, "here.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out fsPreflightResponse
	rec := doReq(t, s, "POST", "/.cornus/web/fs/preflight?source=virtual&op=copy",
		`{"to":"project","items":[{"from":"project/here.txt"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	it := out.Items[0]
	if it.Action != "refused" {
		t.Fatalf("a drop into its own folder should be refused, got %q", it.Action)
	}
	if !strings.Contains(it.Error, "already in this folder") {
		t.Errorf("error should name the gesture, got %q", it.Error)
	}
	if out.Refusals != 1 {
		t.Errorf("refusals = %d, want 1", out.Refusals)
	}
}

// TestPreflightDetectsTheAliasedSamePath is the case a spelling comparison cannot see:
// one file reachable both as a local root path and through the bind mount that exports it
// to a container. Copying one onto the other is copying a file onto itself.
func TestPreflightDetectsTheAliasedSamePath(t *testing.T) {
	s, _, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	if err := os.WriteFile(filepath.Join(sharedDir, "aliased.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// mount0 is <sharedDir>; proj-web/data is the SAME directory seen through the bind.
	var out fsPreflightResponse
	rec := doReq(t, s, "POST", "/.cornus/web/fs/preflight?source=virtual&op=copy",
		`{"to":"proj-web/data/aliased.txt","items":[{"from":"mount0/aliased.txt","to":"proj-web/data/aliased.txt"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Items[0].Action != "refused" {
		t.Fatalf("two names for one file should be refused, got %q (why: %s)",
			out.Items[0].Action, out.Items[0].Why)
	}
}

// TestFolderIntoItsOwnSubtreeIsRefusedAtAnyDepth: the gate has to hold for the source
// being ANY ancestor of the destination, not just its immediate parent — the walk would
// recurse into what it is writing either way — and it has to hold when the two are named
// through different mounts, which is what a spelling comparison cannot see.
func TestFolderIntoItsOwnSubtreeIsRefusedAtAnyDepth(t *testing.T) {
	s, _, sharedDir := redirectServer(t)
	s.cfs = fatalContainerFS{t}
	deep := filepath.Join(sharedDir, "tree", "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, from, to string }{
		{"immediate child", "mount0/tree", "mount0/tree/a"},
		{"grandchild", "mount0/tree", "mount0/tree/a/b"},
		{"great-grandchild", "mount0/tree", "mount0/tree/a/b/c"},
		// The same directory under two names: mount0 IS proj-web's /data bind. A guard
		// that compares fsQuery fields calls this an ordinary cross-mount copy.
		{"aliased through the bind", "mount0/tree", "proj-web/data/tree/a/b"},
		{"aliased the other way", "proj-web/data/tree", "mount0/tree/a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []string{"copy", "move"} {
				rec := doReq(t, s, "POST", "/.cornus/web/fs/"+op+"?source=virtual&path="+tc.from,
					fmt.Sprintf(`{"to":%q}`, tc.to))
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s %s -> %s: got %d, want 400 (%s)",
						op, tc.from, tc.to, rec.Code, strings.TrimSpace(rec.Body.String()))
				}
			}
			// And the preflight says so before anything is attempted.
			var out fsPreflightResponse
			rec := doReq(t, s, "POST", "/.cornus/web/fs/preflight?source=virtual&op=copy",
				fmt.Sprintf(`{"to":%q,"items":[{"from":%q,"to":%q}]}`, tc.to, tc.from, tc.to))
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Items[0].Action != "refused" {
				t.Errorf("preflight %s -> %s: action = %q, want refused",
					tc.from, tc.to, out.Items[0].Action)
			}
		})
	}
	// The paired negative: a SIBLING is not a descendant and must still copy.
	if err := os.MkdirAll(filepath.Join(sharedDir, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, s, "POST", "/.cornus/web/fs/copy?source=virtual&path=mount0/tree",
		`{"to":"mount0/elsewhere/tree"}`); rec.Code != http.StatusOK {
		t.Errorf("a sibling destination must be allowed: %d %s", rec.Code, rec.Body.String())
	}
}
