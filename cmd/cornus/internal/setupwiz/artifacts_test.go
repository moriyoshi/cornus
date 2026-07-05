package setupwiz

import (
	"os"
	"strings"
	"testing"
)

func TestRenderSystemdDocker(t *testing.T) {
	got, err := renderSystemd(systemdData{Addr: "127.0.0.1:5000"})
	if err != nil {
		t.Fatal(err)
	}
	want := `[Unit]
Description=cornus server
After=network-online.target
Wants=network-online.target

[Service]
Environment=CORNUS_DATA=/var/lib/cornus
ExecStart=/usr/local/bin/cornus serve --addr 127.0.0.1:5000
Restart=on-failure

[Install]
WantedBy=multi-user.target
`
	if got != want {
		t.Errorf("docker unit:\n got %q\nwant %q", got, want)
	}
}

// Each non-default backend must select itself and state its own prerequisite.
// The prerequisites are the point: none of them fails at unit start, so a unit
// that omits them looks healthy right up until a deploy fails for reasons three
// layers away.
func TestRenderSystemdPerBackend(t *testing.T) {
	for _, tc := range []struct {
		backend string
		want    []string
		absent  []string
	}{
		{
			backend: backendContainerd,
			want: []string{
				"Environment=CORNUS_DEPLOY_BACKEND=containerd",
				"ExecStart=/usr/local/bin/cornus serve --addr 10.0.0.5:5000",
				"User=root",
				"/opt/cni/bin",
			},
		},
		{
			backend: backendBare,
			want: []string{
				"Environment=CORNUS_DEPLOY_BACKEND=bare",
				"User=root",
				"CORNUS_BARE_RUNTIME",
				"/opt/cni/bin",
			},
		},
		{
			backend: backendIncus,
			want: []string{
				"Environment=CORNUS_DEPLOY_BACKEND=incus",
				"After=incus.service",
				"CORNUS_INCUS_SOCKET",
				"skopeo",
				"umoci",
				"6.3+",
			},
			// Incus talks to a daemon that owns the namespaces and cgroups, so
			// unlike containerd/bare it does not need cornus itself to be root.
			absent: []string{"User=root"},
		},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			got, err := renderSystemd(systemdData{Addr: "10.0.0.5:5000", Backend: tc.backend})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("%s unit missing %q:\n%s", tc.backend, want, got)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("%s unit unexpectedly contains %q:\n%s", tc.backend, absent, got)
				}
			}
		})
	}
}

func TestRenderSystemdDefaultsAddr(t *testing.T) {
	got, _ := renderSystemd(systemdData{})
	if !strings.Contains(got, "--addr 127.0.0.1:5000") {
		t.Errorf("empty addr should default: %s", got)
	}
}

func TestRenderHelm(t *testing.T) {
	got, err := renderHelm(helmData{Exposure: "nodePort"})
	if err != nil {
		t.Fatal(err)
	}
	want := "deployBackend: kubernetes\nregistry:\n  exposure: nodePort\n"
	if got != want {
		t.Errorf("helm minimal:\n got %q\nwant %q", got, want)
	}

	full, _ := renderHelm(helmData{Exposure: "clusterIP", AdvertiseHost: "reg:5000", Audience: "cornus"})
	wantFull := "deployBackend: kubernetes\nregistry:\n  exposure: clusterIP\n  advertiseHost: reg:5000\nauth:\n  jwt:\n    audience: cornus\n"
	if full != wantFull {
		t.Errorf("helm full:\n got %q\nwant %q", full, wantFull)
	}
}

func TestArtifactWriteGuardDeclinedOverwrite(t *testing.T) {
	ui := &scriptUI{
		selects:  []int{0},      // Write to a file
		confirms: []bool{false}, // decline overwrite
	}
	w, buf := newTestWizard(t, ui, "")
	wrote := false
	w.Stat = func(string) (os.FileInfo, error) { return nil, nil } // pretend it exists
	w.WriteFile = func(string, []byte, os.FileMode) error { wrote = true; return nil }

	if err := w.writeArtifacts(&Answers{Scenario: ScenarioSSHDocker, SSHRemoteAddr: "127.0.0.1:5000"}); err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("declining the overwrite must not call WriteFile")
	}
	if !strings.Contains(buf.String(), "ExecStart=/usr/local/bin/cornus serve") {
		t.Errorf("declined overwrite should print the artifact instead:\n%s", buf.String())
	}
}

func TestArtifactWriteSuccess(t *testing.T) {
	ui := &scriptUI{selects: []int{0}} // Write; file does not exist so no confirm
	w, _ := newTestWizard(t, ui, "")
	var gotName string
	var gotPerm os.FileMode
	var gotData []byte
	w.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	w.WriteFile = func(name string, data []byte, perm os.FileMode) error {
		gotName, gotData, gotPerm = name, data, perm
		return nil
	}
	if err := w.writeArtifacts(&Answers{Scenario: ScenarioSSHContainerd, SSHRemoteAddr: "127.0.0.1:5000"}); err != nil {
		t.Fatal(err)
	}
	if gotName != "cornus.service" || gotPerm != 0o644 {
		t.Errorf("write name/perm = %q %o, want cornus.service 644", gotName, gotPerm)
	}
	if !strings.Contains(string(gotData), "CORNUS_DEPLOY_BACKEND=containerd") {
		t.Errorf("written unit should be the containerd variant:\n%s", gotData)
	}
}

func TestArtifactSkip(t *testing.T) {
	ui := &scriptUI{selects: []int{2}} // Skip
	w, _ := newTestWizard(t, ui, "")
	called := false
	w.WriteFile = func(string, []byte, os.FileMode) error { called = true; return nil }
	if err := w.writeArtifacts(&Answers{Scenario: ScenarioSSHDocker}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("skip must not write")
	}
}

// The daemonless local server is offered a unit, because on `bare` cornus IS the
// workload supervisor: a server started from a shell stops applying every
// workload's restart policy the moment it exits, and the startup reconcile that
// rebuilds after a reboot only runs when cornus runs.
func TestLocalBareOffersASystemdUnit(t *testing.T) {
	ui := &scriptUI{selects: []int{0}} // Write to a file
	w, _ := newTestWizard(t, ui, "")
	var got []byte
	w.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	w.WriteFile = func(_ string, data []byte, _ os.FileMode) error { got = data; return nil }

	a := &Answers{Scenario: ScenarioLocal, LocalBackend: backendBare, Server: "http://127.0.0.1:5000"}
	if err := w.writeArtifacts(a); err != nil {
		t.Fatalf("writeArtifacts: %v", err)
	}
	for _, want := range []string{
		"CORNUS_DEPLOY_BACKEND=bare",
		"--addr 127.0.0.1:5000", // the unit must listen where the profile points
		"Restart=on-failure",    // survives a crash
		"WantedBy=multi-user.target",
		"User=root",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("local bare unit missing %q:\n%s", want, got)
		}
	}
}

// The unit must follow the profile: one that listens elsewhere points the saved
// context at nothing.
func TestLocalUnitFollowsTheProfileAddress(t *testing.T) {
	for server, want := range map[string]string{
		"http://127.0.0.1:5000": "127.0.0.1:5000",
		"http://127.0.0.1:8080": "127.0.0.1:8080",
		"http://box.local":      "box.local:5000", // no port: the documented default
		"":                      "127.0.0.1:5000",
		"::::not-a-url":         "127.0.0.1:5000",
	} {
		if got := hostPortOf(server); got != want {
			t.Errorf("hostPortOf(%q) = %q, want %q", server, got, want)
		}
	}
}

// The backends whose daemon supervises the workloads are NOT pushed a unit: a
// foreground `cornus serve` stays a reasonable dev loop there, and losing it
// loses the API rather than the workloads.
func TestLocalNonDaemonlessBackendsAreOfferedNoUnit(t *testing.T) {
	for _, b := range []string{backendDocker, backendContainerd, backendIncus, backendKubernetes} {
		ui := &scriptUI{}
		w, _ := newTestWizard(t, ui, "")
		w.WriteFile = func(string, []byte, os.FileMode) error {
			t.Fatalf("backend %q must not be offered a unit", b)
			return nil
		}
		if err := w.writeArtifacts(&Answers{Scenario: ScenarioLocal, LocalBackend: b}); err != nil {
			t.Fatalf("backend %q: %v", b, err)
		}
		if len(ui.offered) != 0 {
			t.Errorf("backend %q was offered an artifact: %+v", b, ui.offered)
		}
	}
}

// The container scenario deliberately ships NO artifact: it needs nothing but
// docker, and bundling a compose file would require Compose, while bundling a
// shell script would add something to audit. The run command lives in the guide
// instead.
func TestContainerScenarioOffersNoArtifact(t *testing.T) {
	ui := &scriptUI{}
	w, buf := newTestWizard(t, ui, "")
	w.WriteFile = func(string, []byte, os.FileMode) error {
		t.Fatal("the container scenario must not write an artifact")
		return nil
	}
	if err := w.writeArtifacts(&Answers{Scenario: ScenarioDockerContainer, ContainerDataDir: "/srv/cornus"}); err != nil {
		t.Fatalf("writeArtifacts: %v", err)
	}
	if len(ui.offered) != 0 {
		t.Errorf("no artifact prompt should be shown: %+v", ui.offered)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing should be printed: %q", buf.String())
	}
}

func TestPortOf(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:5000":      "5000",
		"http://127.0.0.1:8080":      "8080",
		"https://cornus.example.com": "5000", // no port: the documented default
		"":                           "5000",
		"::::not-a-url":              "5000",
	} {
		if got := portOf(in); got != want {
			t.Errorf("portOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// Defaults must still produce a usable command: a caller who accepts every
// prompt gets the documented layout, not a command with holes in it.
func TestContainerRunCommandDefaults(t *testing.T) {
	got := containerRunCommand(&Answers{Scenario: ScenarioDockerContainer})
	for _, want := range []string{
		// No socket, no runtime to deploy to.
		"-v /var/run/docker.sock:/var/run/docker.sock",
		// No data-dir bind, and client-local mounts are refused; without
		// rshared the server's own mount never reaches the daemon and the
		// workload silently gets an empty directory.
		"-v /srv/cornus:/var/lib/cornus:rshared",
		// The bind destination and the server's data dir must agree, and the
		// image's default cannot be assumed.
		"-e CORNUS_DATA=/var/lib/cornus",
		"-p 127.0.0.1:5000:5000",
		"--privileged",
		containerImage,
		"serve --addr :5000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("default run command missing %q:\n%s", want, got)
		}
	}
}

// The published port must follow the profile the wizard just saved, or the
// context it wrote points at a port nothing is listening on.
func TestContainerRunCommandUsesTheProfilePort(t *testing.T) {
	got := containerRunCommand(&Answers{Scenario: ScenarioDockerContainer, Server: "http://127.0.0.1:8080", ContainerDataDir: "/data"})
	if !strings.Contains(got, "-p 127.0.0.1:8080:8080") || !strings.Contains(got, "serve --addr :8080") {
		t.Errorf("run command does not follow the profile's port:\n%s", got)
	}
	if !strings.Contains(got, "-v /data:/var/lib/cornus:rshared") {
		t.Errorf("run command does not use the answered host data dir:\n%s", got)
	}
}

// The preflight must carry the same binds as the run, or it validates a
// configuration nobody is going to run.
func TestContainerPreflightMatchesTheRunBinds(t *testing.T) {
	a := &Answers{Scenario: ScenarioDockerContainer, ContainerDataDir: "/srv/data"}
	pre, run := containerPreflightCommand(a), containerRunCommand(a)
	binds := containerBinds(a.ContainerDataDir)
	if !strings.Contains(pre, binds) || !strings.Contains(run, binds) {
		t.Errorf("preflight and run disagree on the binds:\npre=%s\nrun=%s", pre, run)
	}
	if !strings.Contains(pre, "daemon preflight") || !strings.Contains(pre, "--rm") {
		t.Errorf("preflight command = %q, want a --rm run of daemon preflight", pre)
	}
}
