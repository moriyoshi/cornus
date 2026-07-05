package devcontainer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeDC writes files (relative paths -> content) into a fresh temp dir and
// returns the dir. Use ".devcontainer/devcontainer.json" for the definition.
func writeDC(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mustLoad(t *testing.T, dir string) *Result {
	t.Helper()
	res, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return res
}

func TestSingleContainerImage(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			// a JSONC comment
			"name": "My App",
			"image": "alpine:3",
			"forwardPorts": [3000, "8080:80"],
			"containerEnv": {"FOO": "bar", "N": 3},
			"postCreateCommand": "echo hi",
		}`,
	})
	res := mustLoad(t, dir)

	svc, ok := res.Project.Services()[singleServiceName]
	if !ok {
		t.Fatalf("no %q service; got %v", singleServiceName, keys(res.Project.Services()))
	}
	if svc.Image != "alpine:3" {
		t.Errorf("image = %q", svc.Image)
	}
	if !reflect.DeepEqual([]string(svc.Entrypoint), keepAlive) {
		t.Errorf("overrideCommand default should keep-alive as the entrypoint, got %v", svc.Entrypoint)
	}
	if len(svc.Command) != 0 {
		t.Errorf("keep-alive must override the entrypoint, not append args; command = %v", svc.Command)
	}
	if svc.Environment["FOO"] != "bar" || svc.Environment["N"] != "3" {
		t.Errorf("env = %v", svc.Environment)
	}
	wantPorts := map[int]int{3000: 3000, 8080: 80}
	got := map[int]int{}
	for _, p := range svc.Ports {
		got[p.Host] = p.Container
	}
	if !reflect.DeepEqual(got, wantPorts) {
		t.Errorf("ports = %v want %v", got, wantPorts)
	}
	// Workspace mount is first, bound at the default /workspaces/<basename>.
	ws := svc.Volumes[0]
	absDir, _ := filepath.Abs(dir)
	if ws.Source != absDir {
		t.Errorf("workspace source = %q want %q", ws.Source, absDir)
	}
	if want := "/workspaces/" + filepath.Base(absDir); ws.Target != want {
		t.Errorf("workspace target = %q want %q", ws.Target, want)
	}
	if res.Hooks[singleServiceName].PostCreate == nil {
		t.Error("postCreateCommand not captured")
	}
}

func TestSingleContainerBuild(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"name": "b",
			"build": {"dockerfile": "Dockerfile", "args": {"K": "v"}, "target": "dev", "cacheFrom": ["reg/app:cache"]}
		}`,
	})
	res := mustLoad(t, dir)
	svc := res.Project.Services()[singleServiceName]
	if svc.Build == nil {
		t.Fatal("build not set")
	}
	wantCtx := filepath.Join(dir, ".devcontainer")
	if svc.Build.Context != wantCtx {
		t.Errorf("context = %q want %q", svc.Build.Context, wantCtx)
	}
	if svc.Build.Dockerfile != "Dockerfile" {
		t.Errorf("dockerfile = %q", svc.Build.Dockerfile)
	}
	if svc.Build.Args["K"] != "v" {
		t.Errorf("args = %v", svc.Build.Args)
	}
	// build.target/cacheFrom are threaded through the build wire now (no warning).
	if svc.Build.Target != "dev" {
		t.Errorf("build.target = %q want %q", svc.Build.Target, "dev")
	}
	if len(svc.Build.CacheFrom) != 1 || svc.Build.CacheFrom[0] != "reg/app:cache" {
		t.Errorf("build.cacheFrom = %v want [reg/app:cache]", svc.Build.CacheFrom)
	}
	if hasWarning(res.Warnings, "build.target") || hasWarning(res.Warnings, "cacheFrom") {
		t.Errorf("did not expect a build.target/cacheFrom warning, got %v", res.Warnings)
	}
}

func TestMounts(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"image": "alpine",
			"workspaceFolder": "/src",
			"mounts": [
				"source=${localWorkspaceFolder}/data,target=/data,type=bind",
				{"source": "myvol", "target": "/cache", "type": "volume"}
			]
		}`,
	})
	res := mustLoad(t, dir)
	plans, err := res.Project.Plan("proj")
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[singleServiceName]
	plan.ResolveMounts(res.BaseDir)
	absDir, _ := filepath.Abs(dir)

	binds := map[string]string{} // target -> source
	for _, m := range plan.Spec.Mounts {
		binds[m.Target] = m.Source
	}
	if binds["/src"] != absDir {
		t.Errorf("workspace bind /src = %q want %q", binds["/src"], absDir)
	}
	if binds["/data"] != filepath.Join(absDir, "data") {
		t.Errorf("data bind = %q want %q", binds["/data"], filepath.Join(absDir, "data"))
	}
	// The volume mount is a managed volume, not a bind.
	if len(plan.Spec.Volumes) != 1 || plan.Spec.Volumes[0].Target != "/cache" {
		t.Fatalf("volumes = %+v", plan.Spec.Volumes)
	}
	if plan.Spec.Volumes[0].Name == "" {
		t.Errorf("named volume should carry a name, got anonymous")
	}
}

func TestRunArgs(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"image": "alpine",
			"runArgs": ["--privileged", "--cap-add", "SYS_PTRACE", "-u", "1000"]
		}`,
	})
	res := mustLoad(t, dir)
	if !res.Project.Services()[singleServiceName].Privileged {
		t.Error("--privileged should set Privileged")
	}
	if !hasWarning(res.Warnings, "--cap-add") || !hasWarning(res.Warnings, "--user") {
		t.Errorf("expected runArgs warnings, got %v", res.Warnings)
	}
}

func TestComposeBased(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/stack.yml": `
services:
  app:
    image: alpine
  db:
    image: postgres
  extra:
    image: redis
`,
		".devcontainer/devcontainer.json": `{
			"dockerComposeFile": "stack.yml",
			"service": "app",
			"runServices": ["db"],
			"workspaceFolder": "/work",
			"containerEnv": {"E": "1"},
			"postCreateCommand": ["echo", "hi"]
		}`,
	})
	res := mustLoad(t, dir)

	names := keys(res.Project.Services())
	if _, ok := res.Project.Services()["extra"]; ok {
		t.Errorf("extra should have been filtered out by runServices; got %v", names)
	}
	if _, ok := res.Project.Services()["app"]; !ok {
		t.Errorf("app missing; got %v", names)
	}
	if _, ok := res.Project.Services()["db"]; !ok {
		t.Errorf("db (runServices) missing; got %v", names)
	}
	app := res.Project.Services()["app"]
	// Workspace mount overlaid onto the compose service.
	found := false
	absDir, _ := filepath.Abs(dir)
	for _, v := range app.Volumes {
		if v.Target == "/work" && v.Source == absDir {
			found = true
		}
	}
	if !found {
		t.Errorf("workspace mount not overlaid; volumes = %+v", app.Volumes)
	}
	if app.Environment["E"] != "1" {
		t.Errorf("containerEnv not merged: %v", app.Environment)
	}
	// overrideCommand defaults to false for compose-based: no keep-alive.
	if reflect.DeepEqual([]string(app.Command), keepAlive) || reflect.DeepEqual([]string(app.Entrypoint), keepAlive) {
		t.Error("compose-based should not force keep-alive by default")
	}
	if res.Hooks["app"] == nil || res.Hooks["app"].PostCreate == nil {
		t.Error("hooks should attach to the compose service")
	}
}

func TestLifecycleForms(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"image": "alpine",
			"initializeCommand": "echo init",
			"onCreateCommand": ["sh", "-c", "echo oc"],
			"postCreateCommand": {"b": ["echo", "b"], "a": "echo a"}
		}`,
	})
	res := mustLoad(t, dir)
	if res.Initialize == nil || !reflect.DeepEqual(res.Initialize.Commands, [][]string{{"/bin/sh", "-c", "echo init"}}) {
		t.Errorf("initialize = %+v", res.Initialize)
	}
	h := res.Hooks[singleServiceName]
	if !reflect.DeepEqual(h.OnCreate.Commands, [][]string{{"sh", "-c", "echo oc"}}) {
		t.Errorf("onCreate = %+v", h.OnCreate)
	}
	// Object form: sorted by label (a before b).
	want := [][]string{{"/bin/sh", "-c", "echo a"}, {"echo", "b"}}
	if !reflect.DeepEqual(h.PostCreate.Commands, want) {
		t.Errorf("postCreate = %+v want %+v", h.PostCreate.Commands, want)
	}
}

func TestBareDevcontainerJSON(t *testing.T) {
	// A bare .devcontainer.json at the workspace root (no .devcontainer dir).
	dir := writeDC(t, map[string]string{
		".devcontainer.json": `{"image": "alpine"}`,
	})
	res := mustLoad(t, dir)
	absDir, _ := filepath.Abs(dir)
	if res.Project.Services()[singleServiceName].Volumes[0].Source != absDir {
		t.Errorf("workspace root should be the file's dir for a bare .devcontainer.json")
	}
}

func TestUnsupportedFeaturesWarn(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"image": "alpine",
			"features": {"ghcr.io/x/y:1": {}}
		}`,
	})
	res := mustLoad(t, dir)
	if !hasWarning(res.Warnings, "features") {
		t.Errorf("expected a features warning, got %v", res.Warnings)
	}
}

func TestDockerComposeFileNullFallsBackToSingleContainer(t *testing.T) {
	// "dockerComposeFile": null must not be mistaken for a compose-based
	// definition (which would resolve the empty path to the .devcontainer dir
	// and fail with a confusing "is a directory" error). It should decode to an
	// empty list so Load() falls back to the image-based single container.
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"dockerComposeFile": null,
			"service": "app",
			"image": "alpine"
		}`,
	})
	res := mustLoad(t, dir)
	if _, ok := res.Project.Services()[singleServiceName]; !ok {
		t.Fatalf("expected single-container fallback service %q, got %v", singleServiceName, keys(res.Project.Services()))
	}
	if got := res.Project.Services()[singleServiceName].Image; got != "alpine" {
		t.Errorf("image = %q, want alpine", got)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
