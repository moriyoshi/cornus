package dockerhost

// Golden-body tests for the createBody -> SpecGenerator translation.
//
// These assert the exact KEYS and TYPES on the wire, decoded from the encoded
// JSON rather than compared against our own struct. That distinction is the
// point: round-tripping through specGenerator would confirm only that the
// encoder agrees with itself, which is true no matter how wrong the tags are.
//
// The failures being guarded are silent ones. libpod does not reject a
// Docker-spelled field; encoding/json simply has nowhere to put it, so the
// container is created successfully and comes up missing the setting. A test
// that asserted "create returned an id" would pass against every one of them.

import (
	"encoding/json"
	"testing"
)

// encodeSpec runs the translation and decodes the result into a generic map, so
// assertions see what libpod would see.
func encodeSpec(t *testing.T, name string, b createBody) map[string]any {
	t.Helper()
	s, err := toSpecGenerator(name, b)
	if err != nil {
		t.Fatalf("toSpecGenerator: %v", err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestSpecEnvIsAMapNotKVStrings: Docker sends []string{"K=V"}; libpod wants a
// map. Sending the array leaves env unset — the container starts with none of
// its configuration and fails in whatever way that application fails.
func TestSpecEnvIsAMapNotKVStrings(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image: "img",
		Env:   []string{"FOO=bar", "EMPTY=", "NOEQUALS"},
	})
	env, ok := got["env"].(map[string]any)
	if !ok {
		t.Fatalf("env = %#v, want a JSON object (libpod takes a map, not K=V strings)", got["env"])
	}
	if env["FOO"] != "bar" {
		t.Errorf(`env["FOO"] = %v, want "bar"`, env["FOO"])
	}
	if env["EMPTY"] != "" {
		t.Errorf(`env["EMPTY"] = %v, want ""`, env["EMPTY"])
	}
	// A bare name with no '=' means an empty value, as Docker treats it.
	if v, present := env["NOEQUALS"]; !present || v != "" {
		t.Errorf(`env["NOEQUALS"] = %v (present=%v), want "" present`, v, present)
	}
}

// TestSpecStopSignalIsANumber: Docker sends "SIGTERM", libpod wants 15. A string
// there is dropped, and the container gets libpod's default stop signal rather
// than the one the spec asked for.
func TestSpecStopSignalIsANumber(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64 // JSON numbers decode as float64
	}{
		{"SIGTERM", 15},
		{"TERM", 15},
		{"SIGKILL", 9},
		{"SIGHUP", 1},
		{"9", 9},
	} {
		got := encodeSpec(t, "app", createBody{Image: "img", StopSignal: tc.in})
		if got["stop_signal"] != tc.want {
			t.Errorf("stop_signal for %q = %#v, want %v (a number, not a name)", tc.in, got["stop_signal"], tc.want)
		}
	}
}

func TestSpecRejectsUnknownStopSignal(t *testing.T) {
	if _, err := toSpecGenerator("app", createBody{Image: "img", StopSignal: "SIGNOPE"}); err == nil {
		t.Error("an unknown stop signal was accepted; want an error rather than a silent 0 (which is not a signal)")
	}
}

// TestSpecNetworksUsesCapitalNAndDemandsBridge covers the two halves of the
// networks trap together, because they only work as a pair.
//
// The key is "Networks" (capital N) because the upstream field carries no JSON
// tag. And libpod's Validate() REJECTS a spec that has Networks without netns
// explicitly bridge — Docker infers bridge, libpod does not.
func TestSpecNetworksUsesCapitalNAndDemandsBridge(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image: "img",
		NetworkingConfig: &networkingConfig{EndpointsConfig: map[string]endpointSettings{
			"appnet": {Aliases: []string{"api"}},
		}},
	})
	if _, ok := got["Networks"]; !ok {
		t.Errorf(`spec has no "Networks" key (capital N); libpod's field is untagged so this is the wire name. body: %v`, got)
	}
	netns, ok := got["netns"].(map[string]any)
	if !ok {
		t.Fatalf(`netns = %#v, want {"nsmode":"bridge"} — libpod REJECTS a spec carrying Networks without it`, got["netns"])
	}
	if netns["nsmode"] != "bridge" {
		t.Errorf(`netns.nsmode = %v, want "bridge"`, netns["nsmode"])
	}
	nets := got["Networks"].(map[string]any)
	appnet := nets["appnet"].(map[string]any)
	aliases := appnet["aliases"].([]any)
	if len(aliases) != 1 || aliases[0] != "api" {
		t.Errorf("aliases = %v, want [api]", aliases)
	}
}

// TestSpecNetnsContainerSharing is what makes the caretaker companion work: it
// shares the workload's netns, which is the whole mechanism behind port-forward
// against a rootless daemon.
func TestSpecNetnsContainerSharing(t *testing.T) {
	got := encodeSpec(t, "companion", createBody{
		Image:      "img",
		HostConfig: hostConfig{NetworkMode: "container:abc123"},
	})
	netns, ok := got["netns"].(map[string]any)
	if !ok {
		t.Fatalf("netns = %#v, want a {nsmode,value} object", got["netns"])
	}
	if netns["nsmode"] != "container" || netns["value"] != "abc123" {
		t.Errorf(`netns = %v, want {nsmode:"container", value:"abc123"} — `+
			`Docker's "container:<id>" string has no meaning to libpod`, netns)
	}
}

// TestSpecNanoCpusBecomesQuotaAndPeriod: libpod has no NanoCpus. Dropping the
// conversion is wrong by four orders of magnitude.
func TestSpecNanoCpusBecomesQuotaAndPeriod(t *testing.T) {
	// 1.5 CPUs, as Docker spells it.
	got := encodeSpec(t, "app", createBody{Image: "img", HostConfig: hostConfig{NanoCpus: 1_500_000_000}})
	res, ok := got["resource_limits"].(map[string]any)
	if !ok {
		t.Fatalf("resource_limits = %#v, want an object", got["resource_limits"])
	}
	cpu := res["cpu"].(map[string]any)
	if cpu["quota"] != float64(150000) {
		t.Errorf("cpu.quota = %v, want 150000 (nanocpus/10000)", cpu["quota"])
	}
	if cpu["period"] != float64(100000) {
		t.Errorf("cpu.period = %v, want 100000", cpu["period"])
	}
	if _, leaked := got["NanoCpus"]; leaked {
		t.Error("spec carries a NanoCpus key, which libpod has no field for")
	}
}

// TestSpecMountOptionsCarrySemantics: read-only, propagation and relabel are
// option STRINGS in libpod, not fields. The rshared propagation the caretaker
// mount-relay depends on is exactly such an option, and losing it yields a mount
// that exists but does not propagate.
func TestSpecMountOptionsCarrySemantics(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image: "img",
		HostConfig: hostConfig{Mounts: []mountSpec{
			{Type: "bind", Source: "/host/data", Target: "/data", ReadOnly: true,
				BindOptions: &bindOptions{Propagation: "rslave"}},
		}},
	})
	mounts, ok := got["mounts"].([]any)
	if !ok || len(mounts) != 1 {
		t.Fatalf("mounts = %#v, want one entry", got["mounts"])
	}
	m := mounts[0].(map[string]any)
	if m["destination"] != "/data" || m["source"] != "/host/data" || m["type"] != "bind" {
		t.Errorf("mount = %v, want destination/source/type carried through", m)
	}
	opts := map[string]bool{}
	for _, o := range m["options"].([]any) {
		opts[o.(string)] = true
	}
	for _, want := range []string{"ro", "rslave", "rbind"} {
		if !opts[want] {
			t.Errorf("mount options %v missing %q — libpod expresses this as an fstab token, not a field", m["options"], want)
		}
	}
}

// TestSpecAnonymousVolumeIsMarked: an anonymous volume has an empty Name, and
// libpod needs IsAnonymous set rather than inferring it.
func TestSpecAnonymousVolume(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image: "img",
		HostConfig: hostConfig{Mounts: []mountSpec{
			{Type: "volume", Source: "", Target: "/cache"},
			{Type: "volume", Source: "named", Target: "/data"},
		}},
	})
	vols := got["volumes"].([]any)
	if len(vols) != 2 {
		t.Fatalf("volumes = %v, want 2", vols)
	}
	anon := vols[0].(map[string]any)
	if anon["IsAnonymous"] != true {
		t.Errorf("anonymous volume = %v, want IsAnonymous true", anon)
	}
	if _, hasName := anon["Name"]; hasName {
		t.Errorf("anonymous volume carries a Name: %v", anon)
	}
	named := vols[1].(map[string]any)
	// Untagged upstream, so the wire keys are the Go field names.
	if named["Name"] != "named" || named["Dest"] != "/data" {
		t.Errorf("named volume = %v, want Name/Dest (PascalCase — the upstream struct is untagged)", named)
	}
}

// TestSpecUnpublishedPortIsNotPublished: libpod reads host_port 0 as "pick a
// random port". Cornus's contract for an unspecified host port is "do not
// publish", so a 0 must be dropped rather than sent.
func TestSpecUnpublishedPortIsNotPublished(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image: "img",
		HostConfig: hostConfig{PortBindings: map[string][]portBinding{
			"80/tcp":    {{HostPort: "8080"}},
			"443/tcp":   {{HostPort: "0"}},
			"9000/​tcp": {{HostPort: ""}},
		}},
	})
	ports, _ := got["portmappings"].([]any)
	if len(ports) != 1 {
		t.Fatalf("portmappings = %v, want only the explicitly-published 8080 — "+
			"a 0 host port would make libpod publish on a RANDOM host port", ports)
	}
	p := ports[0].(map[string]any)
	if p["container_port"] != float64(80) || p["host_port"] != float64(8080) {
		t.Errorf("port mapping = %v, want container 80 -> host 8080 as numbers", p)
	}
	if p["protocol"] != "tcp" {
		t.Errorf("protocol = %v, want tcp", p["protocol"])
	}
}

// TestSpecSecurityOptDecomposed: libpod has no security_opt field at all.
func TestSpecSecurityOptDecomposed(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image: "img",
		HostConfig: hostConfig{SecurityOpt: []string{
			"label=type:svirt_apache_t",
			"apparmor=my-profile",
			"seccomp=/etc/seccomp.json",
			"no-new-privileges",
		}},
	})
	if _, leaked := got["security_opt"]; leaked {
		t.Error("spec carries security_opt, which libpod has no field for")
	}
	if got["apparmor_profile"] != "my-profile" {
		t.Errorf("apparmor_profile = %v", got["apparmor_profile"])
	}
	if got["seccomp_profile_path"] != "/etc/seccomp.json" {
		t.Errorf("seccomp_profile_path = %v", got["seccomp_profile_path"])
	}
	if got["no_new_privileges"] != true {
		t.Errorf("no_new_privileges = %v, want true", got["no_new_privileges"])
	}
	sel := got["selinux_opts"].([]any)
	if len(sel) != 1 || sel[0] != "type:svirt_apache_t" {
		t.Errorf("selinux_opts = %v, want the value with the label= prefix stripped", sel)
	}
}

// TestSpecUlimitsUseOCINames: Docker uses bare "nofile"; the OCI type wants
// RLIMIT_NOFILE.
func TestSpecUlimitsUseOCINames(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image:      "img",
		HostConfig: hostConfig{Ulimits: []ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}}},
	})
	rl := got["r_limits"].([]any)
	e := rl[0].(map[string]any)
	if e["type"] != "RLIMIT_NOFILE" {
		t.Errorf("r_limits[0].type = %v, want RLIMIT_NOFILE", e["type"])
	}
	if e["soft"] != float64(1024) || e["hard"] != float64(2048) {
		t.Errorf("r_limits[0] = %v, want soft 1024 hard 2048", e)
	}
}

// TestSpecHealthcheckIsPassedThrough: healthconfig is the one struct that is
// byte-identical to Docker's, which is what keeps ReportsHealth() true and
// compose's `depends_on: service_healthy` satisfiable on this backend.
func TestSpecHealthcheckIsPassedThrough(t *testing.T) {
	got := encodeSpec(t, "app", createBody{
		Image: "img",
		Healthcheck: &healthConfig{
			Test:     []string{"CMD-SHELL", "curl -f localhost || exit 1"},
			Interval: 30_000_000_000,
			Retries:  3,
		},
	})
	hc, ok := got["healthconfig"].(map[string]any)
	if !ok {
		t.Fatalf("healthconfig = %#v, want an object under the all-lowercase key", got["healthconfig"])
	}
	// Inner keys are PascalCase while the wrapper key is lowercase.
	if _, ok := hc["Test"]; !ok {
		t.Errorf("healthconfig inner keys = %v, want PascalCase Test/Interval/Retries", hc)
	}
	if hc["Interval"] != float64(30_000_000_000) {
		t.Errorf("Interval = %v, want integer nanoseconds", hc["Interval"])
	}
}

// TestSpecAlwaysEnablesCgroups: podman images commonly ship
// containers.conf with cgroups="disabled", which silently yields containers with
// no cgroup — and therefore no stats at all, on both endpoints.
func TestSpecAlwaysEnablesCgroups(t *testing.T) {
	got := encodeSpec(t, "app", createBody{Image: "img"})
	if got["cgroups_mode"] != "enabled" {
		t.Errorf("cgroups_mode = %v, want \"enabled\" so stats work even when "+
			"containers.conf disables cgroups", got["cgroups_mode"])
	}
}
