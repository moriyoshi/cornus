package setupwiz

import (
	"os"
	"strings"
	"testing"
)

func TestRenderSystemdDocker(t *testing.T) {
	got, err := renderSystemd(systemdData{RemoteAddr: "127.0.0.1:5000"})
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

func TestRenderSystemdContainerd(t *testing.T) {
	got, err := renderSystemd(systemdData{RemoteAddr: "10.0.0.5:5000", Containerd: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Environment=CORNUS_DEPLOY_BACKEND=containerd",
		"ExecStart=/usr/local/bin/cornus serve --addr 10.0.0.5:5000",
		"User=root",
		"/opt/cni/bin",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("containerd unit missing %q:\n%s", want, got)
		}
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

// The compose artifact is the whole point of the server-in-a-container scenario:
// the profile it saves is an ordinary loopback one, and what the operator
// actually needs is a run recipe whose binds are right. Each assertion below
// pins a bind whose absence fails SILENTLY at deploy time rather than loudly at
// startup.
func TestRenderContainerCompose(t *testing.T) {
	got, err := renderContainerCompose(containerComposeData{HostDataDir: "/srv/cornus", Port: "5000"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		// No socket, no runtime to deploy to.
		"/var/run/docker.sock:/var/run/docker.sock",
		// No data-dir bind, and client-local mounts are refused; without
		// rshared the server's own mount never reaches the daemon and the
		// workload silently gets an empty directory.
		"/srv/cornus:/var/lib/cornus:rshared",
		// The bind destination and the server's data dir must agree, and the
		// image's default cannot be assumed.
		"CORNUS_DATA: /var/lib/cornus",
		`"127.0.0.1:5000:5000"`,
		"/guides/server-in-a-container",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compose artifact missing %q:\n%s", want, got)
		}
	}
}

// The published port must follow the profile the wizard just saved, or the
// context it wrote points at a port nothing is listening on.
func TestRenderContainerComposeUsesTheProfilePort(t *testing.T) {
	got, err := renderContainerCompose(containerComposeData{HostDataDir: "/data", Port: portOf("http://127.0.0.1:8080")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"127.0.0.1:8080:8080"`) || !strings.Contains(got, `"--addr", ":8080"`) {
		t.Errorf("artifact does not follow the profile's port:\n%s", got)
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

// Defaults must still produce a usable file: a caller who accepts every prompt
// gets the documented layout, not an artifact with holes in it.
func TestRenderContainerComposeDefaults(t *testing.T) {
	got, err := renderContainerCompose(containerComposeData{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/srv/cornus:/var/lib/cornus:rshared") || !strings.Contains(got, `"127.0.0.1:5000:5000"`) {
		t.Errorf("defaults produced an incomplete artifact:\n%s", got)
	}
}
