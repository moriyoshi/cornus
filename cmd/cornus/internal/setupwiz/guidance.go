package setupwiz

import (
	"strings"

	"cornus/pkg/clientconfig"
)

// scenarioGuide is everything the wizard has to say about a scenario beyond its
// questions, in one place so the mid-flow guide and the closing checklist cannot
// drift apart.
type scenarioGuide struct {
	// Synopsis is the one command line this arrangement comes down to, shown
	// above the steps like a manpage SYNOPSIS. The steps say how to get there;
	// this says where you are going, so a reader who already knows the shape can
	// stop at line one. Empty when there is nothing to run.
	Synopsis string
	// Setup is how to stand the SERVER up. It is shown mid-flow the moment the
	// user says no server exists yet, and repeated in the closing checklist —
	// but only then, so someone who already has a server is not told to install
	// one.
	Setup []string
	// Next is what to do once the server is up. Always shown.
	Next []string
	// Doc is the documentation route the user can land on for THIS arrangement —
	// a section anchor, not a page that merely mentions it. A pointer the reader
	// still has to search inside is barely a pointer.
	Doc string
}

// docsBase is the published documentation site. Every documentation reference
// the wizard prints is absolute: a site-relative path like
// "/guides/server-setup" is meaningful inside the site and useless in a
// terminal, where nobody can click it and pasting it into a browser goes
// nowhere. The one cost is that a reader on a different origin (a fork, a
// mirror) is sent here — which is still better than being sent nowhere.
const docsBase = "https://cornus.dev"

// docsURL turns a site-relative documentation route into the absolute URL the
// wizard prints. Keep every doc reference in this package going through it.
func docsURL(route string) string { return docsBase + route }

// setupGuideRoute is the one page with a runbook per arrangement. Its anchors
// are explicit ASCII ids (`{#local-bare}`) rather than slugified headings, so
// the same anchor resolves in every locale and a translated heading cannot
// silently break the link the wizard prints. Kept as a route (not a URL) so the
// test that greps the Markdown for those anchors can find the file.
const setupGuideRoute = "/guides/server-setup"

// setupGuideURL is what the wizard actually prints.
var setupGuideURL = docsURL(setupGuideRoute)

// backendAnchor is the anchor suffix naming a backend in the setup guide. It is
// separate from the CORNUS_DEPLOY_BACKEND value only because the docker default
// is the empty string, which cannot be part of an anchor.
func backendAnchor(backend string) string {
	if backend == backendDocker {
		return "docker"
	}
	return backend
}

// backendPrereqs is the one-line prerequisite statement for each deploy
// backend, phrased so it can follow either "run it there:" (SSH) or "start it:"
// (local). These are the real requirements from the backend reference, not a
// summary: a missing OCI runtime or a pre-6.3 incusd fails the deploy, not the
// startup, which is exactly the kind of failure a setup guide exists to prevent.
var backendPrereqs = map[string]string{
	backendDocker:     "Needs the Docker socket (`/var/run/docker.sock`).",
	backendContainerd: "Needs root, a containerd socket, and the CNI plugins in `/opt/cni/bin`.",
	backendBare:       "Daemonless. Needs root, an OCI runtime on `PATH` (`runc` by default; `CORNUS_BARE_RUNTIME` picks `crun`/`youki`/`runsc`), and the CNI plugins in `/opt/cni/bin`.",
	backendIncus:      "Needs incusd 6.3+ reachable at `CORNUS_INCUS_SOCKET` (default `/var/lib/incus/unix.socket`), with `skopeo` and `umoci` installed ON THE DAEMON HOST — incusd shells out to them to flatten the OCI image.",
	backendKubernetes: "Needs a cluster your `KUBECONFIG` (or `~/.kube/config`) reaches — k3s, kind, minikube, or a remote one — and RBAC to manage Deployments/Services in `CORNUS_K8S_NAMESPACE` (default `default`). No local container runtime is involved.",
}

// backendCaveats is the one trap per backend that a correct-looking setup still
// walks into. Only kubernetes has one at the local-server stage, and it is the
// classic one: the CLUSTER pulls the image, so a registry advertised as
// 127.0.0.1 resolves on the node to the node itself, and every deploy fails
// pulling an image that is sitting on your laptop.
var backendCaveats = map[string]string{
	backendBare:       "Run it under systemd, not from a shell. On this backend cornus IS the supervisor — it pidfd-waits each workload and applies the restart policy itself (`CORNUS_BARE_SHIM` would detach that, but it is off by default), so a server you started in a terminal takes workload supervision with it when it exits. The startup reconcile that reattaches survivors and rebuilds after a host reboot only runs when cornus runs, so `Restart=on-failure` and `WantedBy=multi-user.target` are what make workloads outlive both. The other backends delegate supervision to their daemon and merely lose the API.",
	backendKubernetes: "Registry reachability is the trap here: the cluster's nodes pull built images from this server's registry themselves, so a loopback address is useless to them. Set `CORNUS_ADVERTISE_REGISTRY` (or the client's `--registry-host`) to an address the nodes can reach.",
}

// backendAlternatives names a materially different arrangement worth knowing
// about before committing. Only kubernetes has one, and it is the arrangement
// the project is built around: cornus IN the cluster, where the registry is a
// real service endpoint the nodes reach by construction — which is exactly the
// trap backendCaveats warns about, avoided rather than worked around.
var backendAlternatives = map[string]string{
	backendKubernetes: "Worth knowing before you commit: cornus is primarily meant to run INSIDE the cluster, installed with `helm install cornus oci://ghcr.io/moriyoshi/charts/cornus`. That arrangement makes the registry a real service endpoint the nodes reach by construction, so the caveat above disappears. Re-run `cornus setup` and pick a Kubernetes scenario to configure that instead; see " + docsURL("/introduction/quick-start"),
}

// backendEnvPrefix renders the environment assignment that selects a backend,
// as a command prefix. The dockerhost default is left unstated rather than
// spelled out, matching how the server reads it.
func backendEnvPrefix(backend string) string {
	if backend == backendDocker {
		return ""
	}
	return "CORNUS_DEPLOY_BACKEND=" + backend + " "
}

// backendSudo reports the privilege prefix a backend's own commands need.
// containerd and bare drive namespaces, cgroups, and CNI directly; docker,
// incus, and kubernetes talk to something that does that for them.
func backendSudo(backend string) string {
	if backend == backendContainerd || backend == backendBare {
		return "sudo "
	}
	return ""
}

// localDataDir is where a local server of this backend would keep the registry
// CAS and build cache: a system path for the backends that already require
// root, the user's own data dir otherwise.
func localDataDir(backend string) string {
	if backendSudo(backend) != "" {
		return "/var/lib/cornus"
	}
	return "~/.local/share/cornus"
}

// localSynopsis is the single command a local server of this backend comes down
// to. It is built from the same pieces as the "Start the server" step, so the
// headline and the instruction cannot drift apart.
func localSynopsis(backend string) string {
	cmd := backendSudo(backend) + backendEnvPrefix(backend) + "cornus serve --data-dir " + localDataDir(backend)
	if backend == backendKubernetes {
		// Without this the deploy fails at image pull, so it belongs in the
		// headline rather than three steps down.
		cmd = "CORNUS_ADVERTISE_REGISTRY=HOST:5000 " + cmd
	}
	return cmd
}

// sshSynopsis is the single command that starts the server on the remote host.
func sshSynopsis(backend string) string {
	return "ssh HOST '" + backendSudo(backend) + backendEnvPrefix(backend) + "cornus serve --addr 127.0.0.1:5000'"
}

// containerImage is the published image a containerized server runs from.
const containerImage = "ghcr.io/moriyoshi/cornus:latest"

// containerBinds are the two mounts a containerized server cannot work without,
// as `docker run` arguments. They are the whole difficulty of this arrangement:
// a missing socket bind means no runtime to deploy to, and a data-dir bind
// without `:rshared` means the server's own mount never reaches the daemon and
// the workload silently gets an empty directory.
func containerBinds(hostDataDir string) string {
	if hostDataDir == "" {
		hostDataDir = "/srv/cornus"
	}
	return "-v /var/run/docker.sock:/var/run/docker.sock -v " + hostDataDir + ":/var/lib/cornus:rshared"
}

// containerRunCommand is the single `docker run` that starts a containerized
// server. It is given whole rather than bundled as a compose file or a shell
// script: this arrangement needs nothing but docker, and shipping a file would
// add a tool (or a script to audit) to a setup that does not otherwise require
// one.
func containerRunCommand(a *Answers) string {
	port := portOf(a.Server)
	return "docker run -d --name cornus --privileged --restart unless-stopped" +
		" -p 127.0.0.1:" + port + ":" + port +
		" -e CORNUS_DATA=/var/lib/cornus " + containerBinds(a.ContainerDataDir) +
		" " + containerImage + " serve --addr :" + port
}

// containerPreflightCommand runs the startup checks inside the image that will
// serve, with the same mounts, while it is still cheap to change them.
func containerPreflightCommand(a *Answers) string {
	return "docker run --rm " + containerBinds(a.ContainerDataDir) + " " + containerImage + " daemon preflight"
}

// sshUnitNote points at the generated unit. For the daemonless backend it also
// says why that is not merely convenience: cornus supervises the workloads
// there, so the unit is what keeps them supervised across a crash or a reboot.
func sshUnitNote(backend string) string {
	note := "This wizard offers a matching `cornus.service` systemd unit at the end — take it instead of composing the command by hand."
	if backend == backendBare {
		note += " On this backend that is not just convenience: cornus is the workload supervisor, so a server that dies unsupervised stops applying every workload's restart policy."
	}
	return note
}

// sshRemotePort is the port the tunnel dials on the remote host, taken from the
// answered remote address so a published container port cannot disagree with the
// profile that reaches it.
func sshRemotePort(remoteAddr string) string {
	if i := strings.LastIndex(remoteAddr, ":"); i >= 0 && i+1 < len(remoteAddr) {
		return remoteAddr[i+1:]
	}
	return "5000"
}

// sshContainerRunCommand starts a containerized server ON the remote host. It
// publishes to loopback there, which is exactly where the SSH tunnel exits, so
// nothing is exposed on that host's network.
func sshContainerRunCommand(a *Answers) string {
	port := sshRemotePort(a.SSHRemoteAddr)
	return "ssh " + orDefault(a.SSHHost, "HOST") + " 'docker run -d --name cornus --privileged --restart unless-stopped" +
		" -p 127.0.0.1:" + port + ":" + port +
		" -e CORNUS_DATA=/var/lib/cornus " + containerBinds(a.ContainerDataDir) +
		" " + containerImage + " serve --addr :" + port + "'"
}

// sshContainerSetup is the remote-host runbook when the server runs as a
// container there: no binary to install, and no systemd unit, because
// `--restart unless-stopped` is what survives a reboot.
func sshContainerSetup(a *Answers) []string {
	port := sshRemotePort(a.SSHRemoteAddr)
	return []string{
		"Nothing to install on the remote host but docker itself — the server runs from the published image.",
		"Check the binds there first: `ssh " + orDefault(a.SSHHost, "HOST") + " 'docker run --rm " + containerBinds(a.ContainerDataDir) + " " + containerImage + " daemon preflight'`",
		"Start it: `" + sshContainerRunCommand(a) + "`",
		"It publishes to 127.0.0.1:" + port + " on that host, which is where this tunnel exits — nothing is exposed on the remote network.",
		"No systemd unit is needed or offered: `--restart unless-stopped` is what brings it back after a reboot.",
		"Registry caveat: if the remote's deploy targets cannot pull from the derived host, set `--registry-host`.",
	}
}

// setupDockerContainer leads with the preflight, because the binds are the whole
// difficulty here and getting one wrong fails silently at deploy time rather
// than at startup.
func setupDockerContainer(a *Answers) []string {
	return []string{
		"Check the binds before committing, in the image you will serve from: `" + containerPreflightCommand(a) + "`",
		"Start the server: `" + containerRunCommand(a) + "`",
		"Cornus discovers the " + orDefault(a.ContainerDataDir, "/srv/cornus") + " to /var/lib/cornus correspondence by asking the daemon about its own container, so nothing further is needed.",
	}
}

// localSetup builds the local scenario's server-setup steps for the runtime the
// user picked. Every backend gets the same shape — install, check, start — with
// only the prerequisite line and the env prefix varying.
func localSetup(backend string) []string {
	env := backendEnvPrefix(backend)
	sudo := backendSudo(backend)
	steps := []string{
		"Install the cornus binary on this machine — see " + docsURL("/introduction/installation"),
		backendPrereqs[backend],
		"Check the host before starting: `" + env + "cornus daemon preflight` (it runs the same checks `cornus serve` runs at startup, and exits non-zero on a configuration it would refuse).",
		"Start the server: `" + sudo + env + "cornus serve` (add `--data-dir` to persist the registry + build cache).",
		"The build engine needs privilege of its own: run as root/privileged, or `cornus serve --rootless`.",
	}
	if c := backendCaveats[backend]; c != "" {
		steps = append(steps, c)
	}
	if alt := backendAlternatives[backend]; alt != "" {
		steps = append(steps, alt)
	}
	return steps
}

// sshSetup builds the remote-host setup steps for the backend the scenario
// names. The systemd unit the wizard offers at the end carries the same
// backend selection, so the two cannot disagree.
func sshSetup(backend string) []string {
	env := backendEnvPrefix(backend)
	sudo := backendSudo(backend)
	return []string{
		"Install the cornus binary on the remote host — see " + docsURL("/introduction/installation"),
		backendPrereqs[backend],
		"Verify the host there: `" + env + "cornus daemon preflight`.",
		"Run it there: `" + sudo + env + "cornus serve --addr 127.0.0.1:5000`.",
		sshUnitNote(backend),
		"Registry caveat: if the remote's deploy targets cannot pull from the derived host, set `--registry-host`.",
	}
}

var (
	nextSSH = []string{
		"Once the server is up, re-run the connection test or: `cornus --context NAME version`.",
		"Then deploy something: `cornus compose up`.",
	}
	// Helm leads deliberately, matching what the installation and quick-start
	// pages already call the recommended path. The chart is versioned and its
	// image tag tracks the chart version, so `helm install` needs no pinning to
	// give you a matching server; the raw manifest has to be pinned by hand to a
	// release tag, and getting that wrong installs a privileged StatefulSet from
	// a moving branch.
	setupKube = []string{
		"Install cornus in the cluster with Helm (recommended): `helm install cornus oci://ghcr.io/moriyoshi/charts/cornus` — the chart is versioned and its image tag tracks it, so you get a server and a manifest that match.",
		"This wizard offers a matching `cornus-values.yaml` snippet at the end; install with `-f cornus-values.yaml` to apply it.",
		"Prefer a raw manifest? Use the one pinned to a release tag, not a branch — see " + docsURL("/introduction/installation"),
		"Registry exposure: a NodePort registry auto-advertises the node address; for ClusterIP/ingress set `registry.advertiseHost` (or `--registry-host`).",
	}
	nextKube = []string{
		"If you chose kube-auth, the token audience must match the server's `CORNUS_JWT_AUDIENCE`.",
		"Try it: `cornus --context NAME version`, then `cornus compose up`.",
	}
	nextDockerContainerHint = []string{
		"The socket bind is what makes this the host's docker rather than none at all; `:rshared` on the data dir is what lets a mount cornus makes inside the container reach the host; `--privileged` is needed to build in-process and for the kernel 9P mount.",
	}
	nextDockerContainer = []string{
		// `cornus health`, not `cornus version --health`: the latter is not a flag
		// version accepts, and this line used to print it.
		"Confirm it is reachable: `cornus health`.",
		"Deploy something: `cornus compose up`.",
	}
	setupURL = []string{
		"This scenario points at a server someone else operates: ask them for its URL and, if it requires one, a credential.",
		"To stand one up yourself instead, re-run `cornus setup` and pick a scenario that names a runtime.",
	}
	nextURL = []string{
		"Try any command against it, e.g. `cornus --context NAME version`.",
		"For extra transport options (mTLS, via-server, conduit/SOCKS5) see: `cornus config set-context --help`.",
	}
	nextLocal = []string{
		"Try it: run `cornus compose up` in a project directory.",
	}
)

// guideFor assembles the guide for the chosen scenario, resolving the backend
// each one implies (or, for the local scenario, the one the user picked).
func guideFor(a *Answers) scenarioGuide {
	backend := scenarioBackend(a)
	if isSSHScenario(a.Scenario) {
		g := scenarioGuide{Synopsis: sshSynopsis(backend), Setup: sshSetup(backend), Next: nextSSH, Doc: setupGuideURL + "#ssh-" + backendAnchor(backend)}
		if a.Containerized {
			// Same tunnel, same profile — a different thing on the far end.
			g.Synopsis = sshContainerRunCommand(a)
			g.Setup = sshContainerSetup(a)
			g.Doc = setupGuideURL + "#ssh-container"
		}
		return g
	}
	switch a.Scenario {
	case ScenarioLocal:
		return scenarioGuide{Synopsis: localSynopsis(backend), Setup: localSetup(backend), Next: nextLocal, Doc: setupGuideURL + "#local-" + backendAnchor(backend)}
	case ScenarioKubePortForward, ScenarioKubeURL:
		return scenarioGuide{Synopsis: "helm install cornus oci://ghcr.io/moriyoshi/charts/cornus", Setup: setupKube, Next: nextKube, Doc: setupGuideURL + "#in-cluster"}
	case ScenarioDockerContainer:
		return scenarioGuide{Synopsis: containerRunCommand(a), Setup: setupDockerContainer(a), Next: append(append([]string{}, nextDockerContainerHint...), nextDockerContainer...), Doc: setupGuideURL + "#in-a-container"}
	}
	// No synopsis for a server someone else operates: there is no command to
	// summarize, and inventing one would imply this scenario stands a server up.
	return scenarioGuide{Setup: setupURL, Next: nextURL, Doc: setupGuideURL + "#existing"}
}

// showServerGuide prints the server-setup guide the moment the user says no
// server exists yet, before the scenario asks anything else.
//
// That is the point of it: the questions that follow — the server URL, the
// published port, the host data directory — are answers ABOUT the setup, so the
// reader needs to know what they are about to run before being asked to describe
// it. The values the guide shows for anything not yet asked are the wizard's own
// defaults, which are exactly what it will propose next, so the guide and the
// prompts agree unless the reader deliberately departs from both.
//
// It is printed once. Repeating it in the closing checklist would explain how to
// build a server whose parameters that checklist has already committed to disk.
//
// It narrates through the UI (stderr), the same channel the ingress steps use,
// so it interleaves correctly with both the rich and the plain prompts.
func (w *Wizard) showServerGuide(a *Answers) {
	g := guideFor(a)
	s := w.styles()
	// Every line goes through Note as a pre-rendered string rather than a format
	// string: the styled text carries ANSI escapes and, for a command, literal
	// '%' would be plausible, so it must never be re-interpreted as a verb.
	// Layout: heading, its rule, then the documentation pointer directly beneath
	// it, then a blank line and the body. The pointer sits with the heading
	// because it names the same thing the heading does — the full version of this
	// runbook — rather than being one more step to carry out; trailing it behind
	// the steps put the one durable reference where it was most likely to have
	// scrolled away.
	lines := []string{""}
	lines = append(lines, s.heading("No server yet — here is how to set one up for %s", ScenarioLabel(a.Scenario))...)
	lines = append(lines, s.docLine(g.Doc), "")
	lines = append(lines, s.synopsis(g.Synopsis)...)
	lines = append(lines, s.steps(g.Setup)...)
	lines = append(lines, "",
		s.dim.Render("Carry on: the questions that follow describe the server above, and nothing is written until they are all answered."), "")
	for _, line := range lines {
		w.ui.Note("%s", line)
	}
}

// describeServer renders the connection target the way ConfigGetContextsCmd's
// SERVER column does, so the summary matches what get-contexts will later show.
func describeServer(ctx *clientconfig.Context) string {
	switch {
	case ctx.Server != "":
		return ctx.Server
	case ctx.SSHTunnel != nil:
		st := ctx.SSHTunnel
		dest := st.Addr
		if st.User != "" {
			dest = st.User + "@" + st.Addr
		}
		remote := st.RemoteAddr
		if remote == "" {
			remote = "127.0.0.1:5000"
		}
		return "(ssh-tunnel " + dest + " -> " + remote + ")"
	case ctx.PortForward != nil:
		pf := ctx.PortForward
		if pf.Service == "" {
			return "(port-forward ns/" + pf.Namespace + ")"
		}
		return "(port-forward svc/" + pf.Service + ")"
	default:
		return ""
	}
}

// guidance prints the always-last summary and next-steps block: a KV summary on
// stdout (a result the user may pipe), then numbered steps, the equivalent
// set-context command, and a doc pointer on stderr. The server-setup half is
// included only for a server that is not up yet — telling someone who already
// runs one to go install it is noise that buries the steps that do apply.
func (w *Wizard) guidance(a *Answers, name string, ctx *clientconfig.Context) {
	d := w.d
	g := guideFor(a)
	d.Success("setup complete for context %q", name)

	_ = d.KV().
		Add("context", name).
		Add("server", describeServer(ctx)).
		Add("config", w.configPath).
		Flush()

	// Only what to do NEXT. The server-setup half was printed before the save,
	// while its commands could still be acted on and while the answers they are
	// built from were still open to change; repeating it here would explain how
	// to set up a server whose parameters this file has already committed.
	s := w.styles()
	d.Info("%s", s.title.Render("next steps:"))
	for _, line := range s.steps(g.Next) {
		d.Info("%s", line)
	}
	d.Info("%s", s.title.Render("equivalent command:"))
	d.Info("  %s", s.code.Render(SetContextCommand(name, ctx)))
	if ctx.Token != "" {
		d.Info("  %s", s.dim.Render("(replace REDACTED with the real bearer token)"))
	}
	d.Info("%s", s.docLine(g.Doc))
}
