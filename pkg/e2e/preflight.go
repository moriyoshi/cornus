package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	containerd "github.com/containerd/containerd"
	"sigs.k8s.io/yaml"
)

// Capability is a distinct environmental requirement a Target or scenario needs
// in order to run. Preflight probes each required capability once and reports a
// legible pass/fail up front, so a missing tool or a lack of privilege fails
// fast with a clear diagnostic instead of crashing deep inside a scenario.
type Capability int

const (
	// CapDocker is a docker CLI on PATH plus a reachable daemon. The docker
	// target deploys against it; the kube target inspects the kind network.
	CapDocker Capability = iota
	// CapKind is the `kind` binary (kube target: create/delete the cluster).
	CapKind
	// CapKubectl is the `kubectl` binary (kube target + pod_exec/kubectl builtins).
	CapKubectl
	// CapSSHTools is ssh-keygen/ssh-agent/ssh-add (scenarios calling ssh_agent()).
	CapSSHTools
	// CapBuildEngine is the ability to execute an in-process build: root, or a
	// rootless user-namespace stack (runc + overlayfs). Needed by build().
	CapBuildEngine
	// Cap9P is 9p filesystem support in the running kernel, used by the kube
	// mount sidecar (`mount -t 9p`) in the deploy-mounts/compose-mounts scenarios
	// and by lazy_9p build scenarios that kernel-mount an in-process p9 server.
	Cap9P
	// CapDevcontainerCLI is the official `devcontainer` binary
	// (@devcontainers/cli — the engine VS Code's Dev Containers extension shells
	// out to). Needed by scenarios calling devcontainer_cli().
	CapDevcontainerCLI
	// CapContainerd is a reachable containerd daemon (the containerd target
	// deploys against it via the Go client; socket access usually needs root).
	CapContainerd
	// CapSSHD is the OpenSSH server binary (sshd), used by scenarios calling
	// sshd() to stand up a local SSH endpoint for the ssh-tunnel connection path.
	CapSSHD
	// CapOCIRuntime is a low-level OCI runtime binary (runc/crun/youki/runsc) on
	// PATH plus root, required by the bare target which drives the runtime
	// directly (no daemon) and needs root for overlayfs/netns/CNI.
	CapOCIRuntime
)

// capInfo describes a capability for reporting.
type capInfo struct {
	name string // short stable id (docker, kind, build-engine, ...)
	hint string // how to satisfy it when missing
}

var capInfos = map[Capability]capInfo{
	CapDocker:          {"docker", "install the docker CLI and ensure a daemon is reachable (DOCKER_HOST)"},
	CapKind:            {"kind", "install kind (https://kind.sigs.k8s.io); only the kube target needs it"},
	CapKubectl:         {"kubectl", "install kubectl; only the kube target needs it"},
	CapSSHTools:        {"ssh-tools", "install openssh-client (ssh-keygen/ssh-agent/ssh-add); only ssh_agent() scenarios need it"},
	CapBuildEngine:     {"build-engine", "run as root or enable unprivileged user namespaces (rootless stack); needed by build() scenarios"},
	Cap9P:              {"9p", "load the 9p/9pnet kernel modules; needed by kube mount and lazy_9p build scenarios"},
	CapDevcontainerCLI: {"devcontainer-cli", "npm install -g @devcontainers/cli; only devcontainer_cli() scenarios need it"},
	CapContainerd:      {"containerd", "install and start containerd (socket access usually needs root; set CORNUS_CONTAINERD_ADDRESS) plus the CNI plugins package; only the containerd target needs it"},
	CapSSHD:            {"sshd", "install openssh-server (sshd); only the sshd() ssh-tunnel scenario needs it (it self-skips when absent)"},
	CapOCIRuntime:      {"oci-runtime", "install runc (or crun/youki/runsc) and run as root; the bare target drives it directly (no daemon) and needs root for overlayfs/netns/CNI. Set CORNUS_BARE_RUNTIME to pick the binary (runsc = gVisor), plus the CNI plugins package"},
}

// Name returns the short stable identifier of a capability.
func (c Capability) Name() string { return capInfos[c].name }

// CapHint returns advice on how to satisfy a capability when it is missing.
func CapHint(c Capability) string { return capInfos[c].hint }

// CheckResult is the outcome of probing one required capability.
type CheckResult struct {
	Cap        Capability
	OK         bool
	Detail     string   // what was found (or why it failed)
	RequiredBy []string // scenario paths (or "<target>") that need this capability
}

// targetNeeds returns the capabilities a target inherently requires, regardless
// of scenario. Values are keyed so callers can attribute "required by <target>".
func targetNeeds(t Target) []Capability {
	switch t.(type) {
	case *DockerTarget:
		return []Capability{CapDocker}
	case *ContainerdTarget:
		return []Capability{CapContainerd}
	case *BareTarget:
		// The bare backend drives an OCI runtime directly; it also needs the CNI
		// plugins and root, which the CapOCIRuntime probe + Setup enforce.
		return []Capability{CapOCIRuntime}
	case *KubeTarget:
		// The kube target creates a kind cluster (docker + kind + kubectl) and
		// its mount scenarios kernel-9p-mount inside privileged sidecars.
		return []Capability{CapDocker, CapKind, CapKubectl, Cap9P}
	default:
		return nil // local target: nothing beyond what a scenario itself needs
	}
}

// scenarioNeeds scans a scenario's source for the builtins whose use implies an
// environmental capability. A token scan (not execution) is deliberate: it lets
// preflight run before any target is set up, and the builtin names are ours to
// keep stable. It is intentionally conservative — it never under-reports a need
// a static reader can see.
func scenarioNeeds(path string) ([]Capability, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(src)
	var caps []Capability
	// build( catches build()/compose_build(; build_upload( is the raw POST /.cornus/v1/build
	// tar-upload builtin, which does NOT contain the build( token but still executes a
	// real build, so flag it explicitly.
	if strings.Contains(s, "build(") || strings.Contains(s, "build_upload(") {
		caps = append(caps, CapBuildEngine)
	}
	if strings.Contains(s, "ssh_agent(") {
		caps = append(caps, CapSSHTools)
	}
	// sshd( stands up a local OpenSSH server (needs sshd) and generates keys with
	// ssh-keygen (needs the ssh tools). The scenario self-skips when sshd is absent.
	if strings.Contains(s, "sshd(") {
		caps = append(caps, CapSSHTools, CapSSHD)
	}
	// devcontainer_cli( shells out to the official @devcontainers/cli binary.
	if strings.Contains(s, "devcontainer_cli(") {
		caps = append(caps, CapDevcontainerCLI)
	}
	// A build(lazy_9p=True) scenario backs its lazy contexts with a real kernel-9p
	// mount (CORNUS_LAZY_9P) to measure the pull, so it needs the 9p kernel
	// module — stronger than the build engine alone. The token scan mirrors the
	// build(/ssh_agent( approach above.
	if strings.Contains(s, "lazy_9p") {
		caps = append(caps, Cap9P)
	}
	// A scenario that drives Compose (compose_up/compose_build) against a compose
	// file with a service-level build: section also needs the build engine, even
	// though it never calls build() itself. Conservatively scan the referenced
	// compose file(s) for a build section and flag CapBuildEngine when found.
	if !hasCap(caps, CapBuildEngine) && scenarioDrivesComposeBuild(path, s) {
		caps = append(caps, CapBuildEngine)
	}
	return caps, nil
}

// hasCap reports whether caps already contains c.
func hasCap(caps []Capability, c Capability) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// yamlPathRe matches string literals that look like compose file paths.
var yamlPathRe = regexp.MustCompile(`["']([^"']+\.ya?ml)["']`)

// scenarioDrivesComposeBuild reports whether a scenario builds an image via
// Compose: it uses compose_up()/compose_build() and at least one referenced
// compose file has a service-level build: section. It is conservative — any
// read/parse error is treated as "no build" so preflight never crashes, but a
// compose file that is present and clearly has a build section flags the need.
func scenarioDrivesComposeBuild(scenarioPath, src string) bool {
	if !strings.Contains(src, "compose_up(") && !strings.Contains(src, "compose_build(") {
		return false
	}
	scenarioDir := filepath.Dir(scenarioPath)
	seen := map[string]bool{}
	for _, m := range yamlPathRe.FindAllStringSubmatch(src, -1) {
		ref := m[1]
		// Resolve relative to the current working directory (scenarios reference
		// repo-root-relative paths) and to the scenario file's own directory.
		for _, cand := range []string{ref, filepath.Join(scenarioDir, ref)} {
			if seen[cand] {
				continue
			}
			seen[cand] = true
			if composeFileHasBuild(cand) {
				return true
			}
		}
	}
	return false
}

// composeFileHasBuild reports whether the compose file at path exists, parses,
// and has a service with a non-empty build: section. Any error yields false.
func composeFileHasBuild(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		Services map[string]struct {
			Build json.RawMessage `json:"build"`
		} `json:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false
	}
	for _, svc := range doc.Services {
		b := strings.TrimSpace(string(svc.Build))
		if b != "" && b != "null" {
			return true
		}
	}
	return false
}

// Preflight aggregates the capabilities the given target and scenarios require,
// probes each once, and returns a result per required capability (sorted by
// capability id for stable output). An empty scenarios slice checks only the
// target's inherent needs.
func Preflight(ctx context.Context, t Target, scenarios []string) ([]CheckResult, error) {
	// requiredBy maps a capability to the things that need it, de-duplicated.
	requiredBy := map[Capability]map[string]bool{}
	add := func(c Capability, by string) {
		if requiredBy[c] == nil {
			requiredBy[c] = map[string]bool{}
		}
		requiredBy[c][by] = true
	}
	for _, c := range targetNeeds(t) {
		add(c, "<"+t.Name()+" target>")
	}
	for _, sc := range scenarios {
		needs, err := scenarioNeeds(sc)
		if err != nil {
			return nil, err
		}
		for _, c := range needs {
			add(c, sc)
		}
	}

	results := make([]CheckResult, 0, len(requiredBy))
	for c, by := range requiredBy {
		ok, detail := probe(ctx, t, c)
		names := make([]string, 0, len(by))
		for n := range by {
			names = append(names, n)
		}
		sort.Strings(names)
		results = append(results, CheckResult{Cap: c, OK: ok, Detail: detail, RequiredBy: names})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Cap < results[j].Cap })
	return results, nil
}

// probe checks a single capability, returning whether it is satisfied and a
// short human-readable detail either way. The target is threaded through so a
// probe can honour target-specific configuration (e.g. the containerd socket
// resolved from the --containerd-address flag rather than the environment).
func probe(ctx context.Context, t Target, c Capability) (bool, string) {
	switch c {
	case CapDocker:
		return probeDocker(ctx)
	case CapKind:
		return probeBinary("kind")
	case CapKubectl:
		return probeBinary("kubectl")
	case CapSSHTools:
		var missing []string
		for _, b := range []string{"ssh-keygen", "ssh-agent", "ssh-add"} {
			if _, err := exec.LookPath(b); err != nil {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			return false, "missing: " + strings.Join(missing, ", ")
		}
		return true, "ssh-keygen/ssh-agent/ssh-add on PATH"
	case CapBuildEngine:
		return probeBuildEngine()
	case Cap9P:
		return probe9P()
	case CapDevcontainerCLI:
		return probeBinary("devcontainer")
	case CapContainerd:
		return probeContainerd(ctx, containerdProbeAddress(t))
	case CapOCIRuntime:
		return probeOCIRuntime(t)
	case CapSSHD:
		for _, p := range sshdCandidatePaths {
			if _, err := os.Stat(p); err == nil {
				return true, p
			}
		}
		if p, err := exec.LookPath("sshd"); err == nil {
			return true, p
		}
		return false, "sshd not found (install openssh-server)"
	default:
		return false, "unknown capability"
	}
}

func probeBinary(name string) (bool, string) {
	p, err := exec.LookPath(name)
	if err != nil {
		return false, name + " not on PATH"
	}
	return true, p
}

func probeDocker(ctx context.Context) (bool, string) {
	if _, err := exec.LookPath("docker"); err != nil {
		return false, "docker not on PATH"
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return false, "daemon unreachable: " + firstLine(string(out))
	}
	return true, "daemon " + strings.TrimSpace(string(out))
}

// containerdProbeAddress resolves the socket the containerd probe should check.
// It mirrors the address the run will actually use: a *ContainerdTarget resolves
// it from the --containerd-address flag (via t.addr()), which kong populates into
// the struct but never mirrors into the environment, so containerdAddressFromEnv()
// alone would probe the wrong socket. Any other target falls back to the
// environment/stock default.
func containerdProbeAddress(t Target) string {
	if ct, ok := t.(*ContainerdTarget); ok {
		return ct.addr()
	}
	return containerdAddressFromEnv()
}

// probeContainerd reports whether a containerd daemon answers on the given socket
// (the address the containerd target would actually use). It probes with the Go
// client — the repo links it anyway — rather than a ctr binary.
func probeContainerd(ctx context.Context, addr string) (bool, string) {
	if _, err := os.Stat(addr); err != nil {
		return false, "socket " + addr + " not found"
	}
	client, err := containerd.New(addr, containerd.WithTimeout(5*time.Second))
	if err != nil {
		return false, "daemon unreachable at " + addr + ": " + firstLine(err.Error())
	}
	defer client.Close()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := client.Version(cctx)
	if err != nil {
		return false, "daemon unreachable at " + addr + ": " + firstLine(err.Error())
	}
	return true, "daemon " + v.Version + " at " + addr
}

// probeOCIRuntime reports whether the bare target's OCI runtime binary is on
// PATH and the process is root — the bare backend drives the runtime directly
// (no daemon) and needs root for the overlay snapshotter mount, per-instance
// netns, and CNI. The binary name matches what the run will use: a *BareTarget
// resolves it from its --bare-runtime flag, else CORNUS_BARE_RUNTIME/"runc".
func probeOCIRuntime(t Target) (bool, string) {
	bin := bareRuntimeFromEnv()
	if bt, ok := t.(*BareTarget); ok {
		bin = bt.runtimeBin()
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return false, bin + " not on PATH"
	}
	if os.Geteuid() != 0 {
		return false, p + " found but not root (bare needs root for overlayfs/netns/CNI; rootless unsupported)"
	}
	return true, p + " (euid 0)"
}

// probeBuildEngine reports whether an in-process build can execute here: root is
// always sufficient; otherwise an unprivileged user-namespace stack is required.
// This mirrors the gate in pkg/build/builder (root, or --rootless on a userns host).
func probeBuildEngine() (bool, string) {
	if os.Geteuid() == 0 {
		return true, "running as root (euid 0)"
	}
	if ok, detail := unprivilegedUserns(); ok {
		return true, "unprivileged user namespaces available (" + detail + "); use --rootless"
	}
	return false, "not root and unprivileged user namespaces unavailable; run privileged/as root or enable a rootless stack"
}

// unprivilegedUserns best-effort detects whether unprivileged user namespaces
// (the rootless build prerequisite) are enabled on this kernel.
func unprivilegedUserns() (bool, string) {
	// Debian/Ubuntu knob: 0 disables unprivileged userns entirely.
	if b, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(b)) == "0" {
			return false, "unprivileged_userns_clone=0"
		}
	}
	// Mainline knob: max_user_namespaces==0 means no userns for this user.
	if b, err := os.ReadFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			if n <= 0 {
				return false, "max_user_namespaces=0"
			}
			return true, "max_user_namespaces=" + strconv.Itoa(n)
		}
	}
	// Neither knob readable: assume available (common on default kernels) but say so.
	return true, "userns knobs not restrictive"
}

// probe9P reports whether the kernel supports the 9p filesystem, used by the
// kube mount sidecar. A loadable-but-not-loaded module still counts as usable
// inside a privileged container (the sidecar mounts on demand).
func probe9P() (bool, string) {
	if b, err := os.ReadFile("/proc/filesystems"); err == nil {
		if strings.Contains(string(b), "\t9p\n") || strings.HasSuffix(strings.TrimSpace(string(b)), "\t9p") {
			return true, "9p in /proc/filesystems"
		}
	}
	// Built as a module but not yet loaded: present under /sys/module or in the
	// modules dep list means a privileged mount can pull it in on demand.
	if _, err := os.Stat("/sys/module/9p"); err == nil {
		return true, "9p module loaded"
	}
	// Consult the kernel's modules.dep: on stock distro kernels 9p is a loadable
	// module that stays unloaded (and thus absent from /proc/filesystems and
	// /sys/module) until something mounts it — which the privileged sidecar does.
	if rel, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		dep := filepath.Join("/lib/modules", strings.TrimSpace(string(rel)), "modules.dep")
		if b, err := os.ReadFile(dep); err == nil && modulesDepHas9P(string(b)) {
			return true, "9p loadable module listed in " + dep
		}
	}
	return false, "9p not in /proc/filesystems, not loaded, and no loadable 9p module found"
}

// modulesDepHas9P reports whether a modules.dep listing declares the 9p
// filesystem module. Each entry is "path/to/mod.ko[.comp]: dep1 dep2 ...", so we
// match the module basename (the 9p fs module is 9p.ko, possibly compressed to
// 9p.ko.xz / 9p.ko.zst / 9p.ko.gz), never the 9pnet transport it depends on.
func modulesDepHas9P(depContent string) bool {
	for _, line := range strings.Split(depContent, "\n") {
		mod, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		base := filepath.Base(strings.TrimSpace(mod))
		if base == "9p.ko" || strings.HasPrefix(base, "9p.ko.") {
			return true
		}
	}
	return false
}

// FormatPreflight renders results as an aligned table for terminal/CI logs.
func FormatPreflight(results []CheckResult) string {
	var b strings.Builder
	for _, r := range results {
		mark := "✓"
		if !r.OK {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s %-13s %s\n", mark, r.Cap.Name(), r.Detail)
		fmt.Fprintf(&b, "    required by: %s\n", strings.Join(r.RequiredBy, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// FirstFailure returns the first not-OK result, or nil if all passed.
func FirstFailure(results []CheckResult) *CheckResult {
	for i := range results {
		if !results[i].OK {
			return &results[i]
		}
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
