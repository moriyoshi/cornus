package devcontainer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cornus/pkg/compose"
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
	svc := res.Project.Services()[singleServiceName]
	if !svc.Privileged {
		t.Error("--privileged should set Privileged")
	}
	if len(svc.CapAdd) != 1 || svc.CapAdd[0] != "SYS_PTRACE" {
		t.Errorf("--cap-add = %v want [SYS_PTRACE]", svc.CapAdd)
	}
	if svc.User != "1000" {
		t.Errorf("-u = %q want 1000", svc.User)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("these runArgs are all supported now; got warnings %v", res.Warnings)
	}
}

// TestRunArgsMapped exercises the breadth of the docker-run argv cornus maps
// onto the compose service, in both the "--flag value" and "--flag=value"
// spellings.
func TestRunArgsMapped(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"image": "alpine",
			"runArgs": [
				"--init", "--read-only", "-t", "-i",
				"--cap-drop=NET_RAW", "--security-opt", "seccomp=unconfined",
				"--group-add", "audio", "--device", "/dev/fuse",
				"--tmpfs", "/run", "--dns=1.1.1.1", "--dns-search", "corp.example",
				"--dns-option", "ndots:2", "--add-host", "db:10.0.0.5",
				"--sysctl", "net.ipv4.ip_forward=1", "--label", "team=dev",
				"-e", "MODE=fast", "--ulimit", "nofile=1024:2048",
				"-p", "127.0.0.1:9000:9000", "-v", "/opt/data:/data:ro",
				"--mount", "type=tmpfs,target=/scratch",
				"-w", "/srv", "-h", "devbox", "--name", "mybox",
				"--restart", "unless-stopped", "--stop-signal", "SIGINT",
				"--pid", "host", "--ipc=shareable",
				"--shm-size", "256m", "-m", "2g", "--cpus", "1.5",
				"--expose", "7000", "-d"
			]
		}`,
	})
	res := mustLoad(t, dir)
	if len(res.Warnings) != 0 {
		t.Fatalf("every runArg here is supported; got warnings %v", res.Warnings)
	}
	svc := res.Project.Services()[singleServiceName]
	if svc.Init == nil || !*svc.Init || !svc.ReadOnly || !svc.TTY || !svc.StdinOpen {
		t.Errorf("boolean flags: init=%v readOnly=%v tty=%v stdinOpen=%v", svc.Init, svc.ReadOnly, svc.TTY, svc.StdinOpen)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"cap_drop", []string(svc.CapDrop), []string{"NET_RAW"}},
		{"security_opt", []string(svc.SecurityOpt), []string{"seccomp=unconfined"}},
		{"group_add", []string(svc.GroupAdd), []string{"audio"}},
		{"devices", []string(svc.Devices), []string{"/dev/fuse"}},
		{"dns", []string(svc.DNS), []string{"1.1.1.1"}},
		{"dns_search", []string(svc.DNSSearch), []string{"corp.example"}},
		{"dns_opt", []string(svc.DNSOpt), []string{"ndots:2"}},
		{"extra_hosts", []string(svc.ExtraHosts), []string{"db:10.0.0.5"}},
		{"sysctls", map[string]string(svc.Sysctls), map[string]string{"net.ipv4.ip_forward": "1"}},
		{"labels", map[string]string(svc.Labels), map[string]string{"team": "dev"}},
		{"expose", []int(svc.Expose), []int{7000}},
		{"working_dir", svc.WorkingDir, "/srv"},
		{"hostname", svc.Hostname, "devbox"},
		{"container_name", svc.ContainerName, "mybox"},
		{"restart", string(svc.Restart), "unless-stopped"},
		{"stop_signal", svc.StopSignal, "SIGINT"},
		{"pid", svc.PID, "host"},
		{"ipc", svc.IPC, "shareable"},
		{"ulimits", []compose.Ulimit(svc.Ulimits), []compose.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v want %#v", c.name, c.got, c.want)
		}
	}
	if svc.Environment["MODE"] != "fast" {
		t.Errorf("-e not applied: %v", svc.Environment)
	}
	// --tmpfs and a type=tmpfs --mount both land in the service tmpfs list.
	if want := []string{"/run", "/scratch"}; !reflect.DeepEqual([]string(svc.Tmpfs), want) {
		t.Errorf("tmpfs = %v want %v", svc.Tmpfs, want)
	}
	var published *compose.Port
	for i, p := range svc.Ports {
		if p.Container == 9000 {
			published = &svc.Ports[i]
		}
	}
	if published == nil || published.Host != 9000 || published.HostIP != "127.0.0.1" {
		t.Errorf("-p not applied: %+v", svc.Ports)
	}
	var bind *compose.Volume
	for i, v := range svc.Volumes {
		if v.Target == "/data" {
			bind = &svc.Volumes[i]
		}
	}
	if bind == nil || bind.Source != "/opt/data" || !bind.ReadOnly {
		t.Errorf("-v not applied: %+v", svc.Volumes)
	}
	// The size/CPU scalars route through the compose limit fields.
	plans, err := res.Project.Plan("p")
	if err != nil {
		t.Fatal(err)
	}
	spec := plans[singleServiceName].Spec
	if spec.ShmSize != 256*1024*1024 {
		t.Errorf("shm_size = %d", spec.ShmSize)
	}
	if spec.Resources == nil || spec.Resources.MemoryLimit != 2*1024*1024*1024 {
		t.Errorf("mem_limit = %+v", spec.Resources)
	}
	if spec.Resources == nil || spec.Resources.CPULimit != 1.5 {
		t.Errorf("cpus = %+v", spec.Resources)
	}
}

// TestRunArgsUserOverridesContainerUser pins the precedence between the two
// ways a devcontainer can name the container's process user.
func TestRunArgsUserOverridesContainerUser(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"image": "alpine",
			"containerUser": "vscode",
			"remoteUser": "node",
			"runArgs": ["--user", "4242"]
		}`,
	})
	res := mustLoad(t, dir)
	if got := res.Project.Services()[singleServiceName].User; got != "4242" {
		t.Errorf("runArgs --user should win over containerUser; user = %q", got)
	}
	// remoteUser still selects the lifecycle-command user.
	if got := res.Hooks[singleServiceName].User; got != "node" {
		t.Errorf("hooks user = %q want node", got)
	}
}

// TestContainerUserApplied pins containerUser onto the container's own process
// user (it used to be recognised but only ever warned about).
func TestContainerUserApplied(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{"image": "alpine", "containerUser": "vscode"}`,
	})
	res := mustLoad(t, dir)
	if got := res.Project.Services()[singleServiceName].User; got != "vscode" {
		t.Errorf("containerUser should set the service user; got %q", got)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("containerUser is implemented now; got warnings %v", res.Warnings)
	}
}

func TestBuildOptions(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"build": {
				"dockerfile": "Dockerfile",
				"options": [
					"--no-cache", "--pull", "--platform", "linux/amd64,linux/arm64",
					"--build-arg", "K=v", "--label", "l=1", "--target=prod",
					"--cache-from", "reg/app:cache", "--cache-to=type=registry,ref=reg/app:cache",
					"--add-host", "db:10.0.0.5", "--network=host", "--ssh", "default",
					"-t", "reg/app:dev", "--shm-size", "128m"
				]
			}
		}`,
	})
	res := mustLoad(t, dir)
	if len(res.Warnings) != 0 {
		t.Fatalf("every build option here is supported; got warnings %v", res.Warnings)
	}
	b := res.Project.Services()[singleServiceName].Build
	if !b.NoCache || !b.Pull {
		t.Errorf("no_cache=%v pull=%v", b.NoCache, b.Pull)
	}
	if want := []string{"linux/amd64", "linux/arm64"}; !reflect.DeepEqual(b.Platforms, want) {
		t.Errorf("platforms = %v want %v", b.Platforms, want)
	}
	if b.Args["K"] != "v" || b.Labels["l"] != "1" || b.Target != "prod" || b.Network != "host" {
		t.Errorf("args=%v labels=%v target=%q network=%q", b.Args, b.Labels, b.Target, b.Network)
	}
	if !reflect.DeepEqual(b.CacheFrom, []string{"reg/app:cache"}) ||
		!reflect.DeepEqual(b.CacheTo, []string{"type=registry,ref=reg/app:cache"}) {
		t.Errorf("cache_from=%v cache_to=%v", b.CacheFrom, b.CacheTo)
	}
	if !reflect.DeepEqual([]string(b.ExtraHosts), []string{"db:10.0.0.5"}) ||
		!reflect.DeepEqual(b.SSH, []string{"default"}) ||
		!reflect.DeepEqual(b.Tags, []string{"reg/app:dev"}) {
		t.Errorf("extra_hosts=%v ssh=%v tags=%v", b.ExtraHosts, b.SSH, b.Tags)
	}
}

// TestLifecycleVariableSubstitution pins that devcontainer variables in
// lifecycle commands are expanded before the command reaches a shell (where
// ${containerWorkspaceFolder} would otherwise expand to nothing), while a real
// shell variable is left alone and NOT reported as unresolved.
func TestLifecycleVariableSubstitution(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"image": "alpine",
			"workspaceFolder": "/src",
			"postCreateCommand": "cd ${containerWorkspaceFolder} && echo ${HOME}"
		}`,
	})
	res := mustLoad(t, dir)
	got := res.Hooks[singleServiceName].PostCreate.Commands[0][2]
	if want := "cd /src && echo ${HOME}"; got != want {
		t.Errorf("postCreate = %q want %q", got, want)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("a shell variable in a lifecycle command is not an unresolved devcontainer variable; got %v", res.Warnings)
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

// composeFixture is the compose file the compose-based boundary cases load.
const composeFixture = `
services:
  app:
    image: alpine
`

// TestCompatibilityBoundary is the regression guard for the supported-subset
// boundary: EVERY field the parser recognises but does not (fully) implement
// must produce a warning that names the field and says what happens instead.
// A field that starts being silently dropped again fails here.
//
// The `want` strings are asserted as substrings of one warning, so they double
// as the documented wording a user sees.
func TestCompatibilityBoundary(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "features",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "features": {"ghcr.io/devcontainers/features/go:1": {}}}`},
			want: []string{"`features` is not supported and was ignored: the container is created from `image`/`build` alone, with no feature packages installed"},
		},
		{
			name: "customizations",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "customizations": {"vscode": {"extensions": ["golang.go"]}}}`},
			want: []string{"`customizations` is editor-specific and was ignored"},
		},
		{
			name: "hostRequirements",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "hostRequirements": {"cpus": 4}}`},
			want: []string{"`hostRequirements` is not supported and was ignored"},
		},
		{
			name: "runArgs with no equivalent",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "runArgs": ["--network", "host", "--gpus=all", "--runtime", "sysbox-runc"]}`},
			want: []string{
				"`runArgs` --network=host is not supported and was ignored: the container joins the project's own cornus network instead",
				"`runArgs` entries have no cornus equivalent and were ignored: --gpus --runtime",
			},
		},
		{
			name: "build.options with no equivalent",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"build": {"dockerfile": "Dockerfile", "options": ["--secret", "id=x", "--output=type=local"]}}`},
			want: []string{"`build.options` entries have no cornus equivalent and were ignored: --secret --output"},
		},
		{
			name: "compose runArgs",
			files: map[string]string{
				".devcontainer/stack.yml":         composeFixture,
				".devcontainer/devcontainer.json": `{"dockerComposeFile": "stack.yml", "service": "app", "runArgs": ["--privileged"]}`,
			},
			want: []string{"`runArgs` is ignored for compose-based devcontainers (--privileged): set the equivalent keys on service \"app\" in the compose file instead"},
		},
		{
			name: "compose image",
			files: map[string]string{
				".devcontainer/stack.yml":         composeFixture,
				".devcontainer/devcontainer.json": `{"dockerComposeFile": "stack.yml", "service": "app", "image": "ubuntu"}`,
			},
			want: []string{"`image` is ignored for compose-based devcontainers: service \"app\" runs the image its compose file declares"},
		},
		{
			name: "compose build",
			files: map[string]string{
				".devcontainer/stack.yml":         composeFixture,
				".devcontainer/devcontainer.json": `{"dockerComposeFile": "stack.yml", "service": "app", "build": {"dockerfile": "Dockerfile"}}`,
			},
			want: []string{"`build` is ignored for compose-based devcontainers"},
		},
		{
			name: "unknown runServices entry",
			files: map[string]string{
				".devcontainer/stack.yml":         composeFixture,
				".devcontainer/devcontainer.json": `{"dockerComposeFile": "stack.yml", "service": "app", "runServices": ["nope"]}`,
			},
			want: []string{"`runServices` names \"nope\", which is not a service in the compose file; it was ignored"},
		},
		{
			name: "unsupported mount type",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "mounts": ["source=x,target=/x,type=npipe"]}`},
			want: []string{"type \"npipe\" is not supported; it was applied as a bind mount at /x instead"},
		},
		{
			name: "unsupported mount option",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "mounts": ["source=/x,target=/x,type=bind,bind-propagation=rslave"]}`},
			want: []string{"option(s) bind-propagation have no cornus equivalent and were ignored"},
		},
		{
			name: "unsupported workspaceMount option",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "workspaceMount": "source=/w,target=/w,type=bind,volume-driver=local"}`},
			want: []string{"`workspaceMount` option(s) volume-driver have no cornus equivalent and were ignored"},
		},
		{
			name: "unresolved variable",
			files: map[string]string{".devcontainer/devcontainer.json": `{
				"image": "alpine", "containerEnv": {"X": "${devcontainerId}"}}`},
			want: []string{"unresolved variable ${devcontainerId} left as-is"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustLoad(t, writeDC(t, tc.files))
			for _, want := range tc.want {
				if !hasWarning(res.Warnings, want) {
					t.Errorf("missing warning %q; got %v", want, res.Warnings)
				}
			}
		})
	}
}

// TestSupportedSubsetIsSilent is the other half of the boundary guard: a
// definition that uses only implemented fields must produce NO warnings, so the
// warning channel stays meaningful.
func TestSupportedSubsetIsSilent(t *testing.T) {
	dir := writeDC(t, map[string]string{
		".devcontainer/devcontainer.json": `{
			"name": "full",
			"build": {"dockerfile": "Dockerfile", "context": ".", "args": {"A": "1"},
			          "target": "dev", "cacheFrom": ["reg/app:cache"], "options": ["--no-cache"]},
			"workspaceFolder": "/w",
			"workspaceMount": "source=${localWorkspaceFolder},target=/w,type=bind,consistency=cached",
			"mounts": [{"source": "vol", "target": "/cache", "type": "volume"}],
			"forwardPorts": [3000],
			"appPort": ["8080:80"],
			"containerEnv": {"A": "1"},
			"remoteEnv": {"B": "${localWorkspaceFolderBasename}"},
			"runArgs": ["--privileged"],
			"overrideCommand": true,
			"containerUser": "vscode",
			"remoteUser": "vscode",
			"initializeCommand": "echo hi",
			"onCreateCommand": "echo oc",
			"updateContentCommand": "echo uc",
			"postCreateCommand": "echo pc",
			"postStartCommand": "echo ps",
			"postAttachCommand": "echo pa"
		}`,
	})
	res := mustLoad(t, dir)
	if len(res.Warnings) != 0 {
		t.Errorf("supported fields must not warn; got %v", res.Warnings)
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
