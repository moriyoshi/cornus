package dockerproxy

import (
	"encoding/json"
	"testing"

	"cornus/pkg/api"
)

func TestToDeploySpec(t *testing.T) {
	req := createRequest{
		Image: "localhost:5000/web:v1",
		Cmd:   []string{"nginx", "-g", "daemon off;"},
		Env:   []string{"LOG=info", "PORT=8080"},
		HostConfig: hostConfig{
			Binds:         []string{"/local/conf:/etc/app:ro", "data:/var/lib"}, // 2nd is a named volume
			PortBindings:  map[string][]portBinding{"80/tcp": {{HostPort: "8080"}}},
			RestartPolicy: restartPolicy{Name: "always"},
			Mounts:        []mountPoint{{Type: "bind", Source: "/local/src", Target: "/src", ReadOnly: false}},
		},
	}
	spec := toDeploySpec("web", req)

	if spec.Name != "web" || spec.Image != "localhost:5000/web:v1" || spec.Replicas != 1 {
		t.Fatalf("spec basics = %+v", spec)
	}
	if len(spec.Command) != 3 || spec.Command[0] != "nginx" {
		t.Errorf("command = %v", spec.Command)
	}
	if spec.Env["LOG"] != "info" || spec.Env["PORT"] != "8080" {
		t.Errorf("env = %v", spec.Env)
	}
	if spec.Restart != "always" {
		t.Errorf("restart = %q", spec.Restart)
	}
	if len(spec.Ports) != 1 || spec.Ports[0].Host != 8080 || spec.Ports[0].Container != 80 || spec.Ports[0].Protocol != "tcp" {
		t.Errorf("ports = %+v", spec.Ports)
	}
	// The bare-name "data:/var/lib" is a NAMED volume (source is not a host path),
	// so it becomes a managed VolumeSpec, not a bind. The path binds stay Mounts.
	var roConf, rwSrc bool
	for _, m := range spec.Mounts {
		if m.Source == "/local/conf" && m.Target == "/etc/app" && m.ReadOnly {
			roConf = true
		}
		if m.Source == "/local/src" && m.Target == "/src" && !m.ReadOnly {
			rwSrc = true
		}
		if m.Source == "data" {
			t.Errorf("named volume must not be a bind mount: %+v", m)
		}
	}
	if !roConf {
		t.Errorf("missing ro bind /local/conf:/etc/app: %+v", spec.Mounts)
	}
	if !rwSrc {
		t.Errorf("missing bind-mount /local/src:/src: %+v", spec.Mounts)
	}
	if len(spec.Volumes) != 1 || spec.Volumes[0].Name != "data" || spec.Volumes[0].Target != "/var/lib" {
		t.Errorf("volumes = %+v, want one named {Name:data, Target:/var/lib}", spec.Volumes)
	}
}

// TestToDeploySpecNamedVolume covers both ways a named volume reaches the proxy:
// a bare-name `-v name:target` bind string, and a compose `Type:"volume"` mount
// point (whose Source the compose plugin has already project-scoped). Both must
// become shared VolumeSpecs, while a host-path source stays a bind.
func TestToDeploySpecNamedVolume(t *testing.T) {
	req := createRequest{
		Image: "img",
		HostConfig: hostConfig{
			Binds: []string{"cache:/var/cache:ro", "/host/data:/data"},
			Mounts: []mountPoint{
				{Type: "volume", Source: "proj_shared", Target: "/shared"},
				{Type: "bind", Source: "/host/etc", Target: "/etc/app", ReadOnly: true},
			},
		},
	}
	spec := toDeploySpec("svc", req)

	byTarget := map[string]api.VolumeSpec{}
	for _, v := range spec.Volumes {
		byTarget[v.Target] = v
	}
	if v := byTarget["/var/cache"]; v.Name != "cache" || !v.ReadOnly {
		t.Errorf("bare-name bind: got %+v, want named {Name:cache, ReadOnly:true}", v)
	}
	if v := byTarget["/shared"]; v.Name != "proj_shared" {
		t.Errorf("volume mount: got %+v, want named {Name:proj_shared}", v)
	}
	if len(spec.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want exactly the two named volumes", spec.Volumes)
	}
	// Host-path sources remain binds, never volumes.
	var sawData, sawEtc bool
	for _, m := range spec.Mounts {
		sawData = sawData || (m.Source == "/host/data" && m.Target == "/data")
		sawEtc = sawEtc || (m.Source == "/host/etc" && m.Target == "/etc/app" && m.ReadOnly)
	}
	if !sawData || !sawEtc {
		t.Errorf("host binds missing: mounts = %+v", spec.Mounts)
	}
}

// TestToDeploySpecNetworks confirms EndpointsConfig entries become
// NetworkAttachments (sorted by name) carrying the endpoint aliases, with the
// compose service label ensured as the first alias when absent.
func TestToDeploySpecNetworks(t *testing.T) {
	req := createRequest{
		Image:  "img",
		Labels: map[string]string{"com.docker.compose.service": "web"},
		NetworkingConfig: networkingConfig{EndpointsConfig: map[string]endpointConfig{
			"proj_front": {Aliases: []string{"www"}},
			"proj_back":  {Aliases: []string{"web", "abc123"}},
			// Docker's builtin: sent by a plain `docker run`, must NOT become a
			// user network (the backend would 403 trying to create it).
			"default": {},
		}},
	}
	spec := toDeploySpec("proj-web-1", req)

	if len(spec.Networks) != 2 {
		t.Fatalf("networks = %+v, want 2 (predefined \"default\" skipped)", spec.Networks)
	}
	// Sorted: proj_back before proj_front.
	back, front := spec.Networks[0], spec.Networks[1]
	if back.Name != "proj_back" || front.Name != "proj_front" {
		t.Fatalf("network order = [%s %s], want sorted [proj_back proj_front]", back.Name, front.Name)
	}
	// "web" already present => not duplicated.
	if got, want := back.Aliases, []string{"web", "abc123"}; !slicesEqual(got, want) {
		t.Errorf("back aliases = %v, want %v", got, want)
	}
	// "web" absent => prepended from the service label.
	if got, want := front.Aliases, []string{"web", "www"}; !slicesEqual(got, want) {
		t.Errorf("front aliases = %v, want %v", got, want)
	}

	// No networks => no attachments.
	if got := toDeploySpec("x", createRequest{Image: "img"}); len(got.Networks) != 0 {
		t.Errorf("no-network create produced attachments: %+v", got.Networks)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestToDeploySpecEntrypoint confirms a create-time Entrypoint override reaches
// the spec (the devcontainer CLI creates with Entrypoint=["/bin/sh"] plus a
// keepalive Cmd; dropping it would leave argv ["-c", ...]).
func TestToDeploySpecEntrypoint(t *testing.T) {
	spec := toDeploySpec("dc", createRequest{
		Image:      "alpine:3.20",
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-c", "sleep infinity"},
	})
	if len(spec.Entrypoint) != 1 || spec.Entrypoint[0] != "/bin/sh" {
		t.Errorf("entrypoint = %v, want [/bin/sh]", spec.Entrypoint)
	}
	if len(spec.Command) != 2 || spec.Command[0] != "-c" {
		t.Errorf("command = %v, want [-c, sleep infinity]", spec.Command)
	}

	if spec := toDeploySpec("plain", createRequest{Image: "img", Cmd: []string{"run"}}); len(spec.Entrypoint) != 0 {
		t.Errorf("entrypoint without override = %v, want empty (image default)", spec.Entrypoint)
	}
}

func TestSplitPortProto(t *testing.T) {
	for _, tc := range []struct {
		in    string
		port  int
		proto string
	}{
		{"80/tcp", 80, "tcp"},
		{"53/udp", 53, "udp"},
		{"8080", 8080, "tcp"},
		{"bad", 0, "tcp"},
	} {
		p, pr := splitPortProto(tc.in)
		if p != tc.port || pr != tc.proto {
			t.Errorf("splitPortProto(%q) = (%d,%q), want (%d,%q)", tc.in, p, pr, tc.port, tc.proto)
		}
	}
}

func TestDeploymentName(t *testing.T) {
	id := "abcdef0123456789abcdef0123456789"
	if got := deploymentName("/My_App.1", id); got != "my-app-1" {
		t.Errorf("deploymentName sanitize = %q", got)
	}
	if got := deploymentName("", id); got != "cornus-abcdef012345" {
		t.Errorf("deploymentName unnamed = %q", got)
	}
}

// TestToDeploySpecRestartNoIsCarried is the regression test for a defect that made
// `docker wait` hang for the MOST COMMON case: a plain `docker run -d` container
// that exits on its own.
//
// Docker's default restart policy is unset, and an explicit `--restart=no` means
// the same thing: do not restart. Both used to leave spec.Restart EMPTY, and
// deploy.RestartPolicy defaults empty to "unless-stopped" — so dockerhost
// resurrected the container forever. A restarting container inspects as
// State.Running=true, so InstanceStatus.ExitCode stays nil,
// DeployStatus.TerminalExitCode never resolves, and the wait never answers.
//
// Measured before the fix: `docker run -d` + self-exit hung (rc 124 from a
// timeout), while `--restart=on-failure` + exit 0 was the only case that settled.
// So there was no way to say "let it die and tell me the code".
func TestToDeploySpecRestartNoIsCarried(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		want   string
	}{
		{"docker default (unset) means do not restart", "", "no"},
		{"explicit no", "no", "no"},
		{"always is passed through", "always", "always"},
		{"unless-stopped is passed through", "unless-stopped", "unless-stopped"},
		{"on-failure is passed through", "on-failure", "on-failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := toDeploySpec("app", createRequest{
				Image:      "nginx:1",
				HostConfig: hostConfig{RestartPolicy: restartPolicy{Name: tt.policy}},
			})
			if spec.Restart != tt.want {
				t.Fatalf("Restart = %q, want %q", spec.Restart, tt.want)
			}
			// The property that actually matters: the effective policy must never
			// come out as a restarting one for a container Docker would let die.
			if tt.want == "no" && deployRestartIsRestarting(spec.Restart) {
				t.Fatalf("effective policy for %q restarts; `docker wait` would hang", tt.policy)
			}
		})
	}
}

// deployRestartIsRestarting mirrors deploy.RestartPolicy's defaulting so the test
// asserts the EFFECTIVE policy, not just the string the translator set.
func deployRestartIsRestarting(restart string) bool {
	if restart == "" {
		restart = "unless-stopped" // deploy.RestartPolicy's default
	}
	return restart != "no"
}

// deployRestartIsUnbounded mirrors how the backends READ the pair of restart
// fields: a restarting policy with RestartMaxAttempts == 0 retries forever
// (dockerhost sends MaximumRetryCount: 0, barehost's supervisor takes
// MaxAttempts: 0 as no cap, kubernetes leaves the Job backoffLimit at its
// default). Asserting on this rather than on the translated string is what makes
// the sibling test below about behavior instead of about a struct field.
func deployRestartIsUnbounded(spec api.DeploySpec) bool {
	return deployRestartIsRestarting(spec.Restart) && spec.RestartMaxAttempts == 0
}

// TestToDeploySpecRestartMaxRetriesIsCarried is the regression test for the
// sibling of the defect above: `docker run --restart=on-failure:2` asks for AT
// MOST two retries, and the translator read only RestartPolicy.Name — the
// MaximumRetryCount next to it was dropped on the floor. The resulting spec said
// "on-failure" with no cap, which every backend reads as retry forever, so a
// container that keeps failing was restarted without end and `docker wait` never
// answered. That is the same failure mode as the empty-policy bug, reached from
// the opposite direction: there the cap was irrelevant because the policy was
// wrong, here the policy is right and the cap is missing.
//
// api.DeploySpec already keeps the two apart (pkg/compose's Restart.split
// produces exactly this pair for `on-failure:N`), so nothing had to be invented:
// dockerhost maps RestartMaxAttempts to RestartPolicy.MaximumRetryCount,
// barehost to its supervisor's MaxAttempts, kubernetes to a Job backoffLimit.
func TestToDeploySpecRestartMaxRetriesIsCarried(t *testing.T) {
	tests := []struct {
		name        string
		policy      string
		retries     int
		wantRestart string
		wantMax     int
	}{
		{"on-failure with a cap keeps the cap", "on-failure", 2, "on-failure", 2},
		{"on-failure without a cap stays uncapped", "on-failure", 0, "on-failure", 0},
		// Docker refuses a count on any other policy, so a stray one carries no
		// meaning and must not silently bound an always-restart container.
		{"a stray count on always is ignored", "always", 5, "always", 0},
		{"a stray count on no is ignored", "no", 5, "no", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := toDeploySpec("app", createRequest{
				Image: "nginx:1",
				HostConfig: hostConfig{RestartPolicy: restartPolicy{
					Name:              tt.policy,
					MaximumRetryCount: tt.retries,
				}},
			})
			if spec.Restart != tt.wantRestart {
				t.Fatalf("Restart = %q, want %q", spec.Restart, tt.wantRestart)
			}
			if spec.RestartMaxAttempts != tt.wantMax {
				t.Fatalf("RestartMaxAttempts = %d, want %d", spec.RestartMaxAttempts, tt.wantMax)
			}
			// The property that actually matters: a request that named a retry
			// budget must not come out as an unbounded restart loop.
			if tt.policy == "on-failure" && tt.retries > 0 && deployRestartIsUnbounded(spec) {
				t.Fatalf("--restart=on-failure:%d translated to an unbounded restart policy", tt.retries)
			}
		})
	}
}

// TestRestartPolicyDecodesDockersRetryCount pins the wire field name. The count
// only reaches toDeploySpec if createRequest actually decodes it, and Docker's
// JSON key is "MaximumRetryCount" — a typo here would leave the translator
// correct and the behavior broken, with no other test noticing.
func TestRestartPolicyDecodesDockersRetryCount(t *testing.T) {
	var req createRequest
	body := `{"Image":"nginx:1","HostConfig":{"RestartPolicy":{"Name":"on-failure","MaximumRetryCount":3}}}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal create request: %v", err)
	}
	if got := req.HostConfig.RestartPolicy.MaximumRetryCount; got != 3 {
		t.Fatalf("MaximumRetryCount = %d, want 3 (decoded from %s)", got, body)
	}
	if spec := toDeploySpec("app", req); spec.RestartMaxAttempts != 3 {
		t.Fatalf("RestartMaxAttempts = %d, want 3", spec.RestartMaxAttempts)
	}
}
