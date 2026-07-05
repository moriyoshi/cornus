package setupwiz

import "cornus/pkg/clientconfig"

// Per-scenario next-steps checklists, printed last. They complete the server-side
// setup the profile assumes (running cornus serve, installing the chart, the
// registry-host caveat) that set-context alone leaves to the user.
var (
	stepsLocal = []string{
		"Start the server: cornus serve (add --data-dir to persist the registry + build cache).",
		"The build engine needs privilege: run as root/privileged or 'cornus serve --rootless'; mount the docker socket for the docker backend, or run as root with CNI plugins in /opt/cni/bin for containerd.",
		"Try it: run 'cornus compose up' in a project directory.",
	}
	stepsSSHDocker = []string{
		"Install the cornus binary on the remote host.",
		"Run it there: cornus serve --addr 127.0.0.1:5000 (or install the generated cornus.service systemd unit).",
		"Registry caveat: if the remote's deploy targets cannot pull from the derived host, set --registry-host.",
		"Once the server is up, re-run the connection test or: cornus --context NAME version.",
	}
	stepsSSHContainerd = []string{
		"Install the cornus binary on the remote host.",
		"Run it there as root with CORNUS_DEPLOY_BACKEND=containerd and CNI plugins in /opt/cni/bin: cornus serve --addr 127.0.0.1:5000 (or install the generated cornus.service systemd unit).",
		"Registry caveat: if the remote's deploy targets cannot pull from the derived host, set --registry-host.",
		"Once the server is up, re-run the connection test or: cornus --context NAME version.",
	}
	stepsKube = []string{
		"Install cornus in the cluster: kubectl apply -f https://raw.githubusercontent.com/moriyoshi/cornus/main/deploy/k8s/cornus.yaml, or helm install cornus oci://ghcr.io/moriyoshi/charts/cornus (see the generated values snippet).",
		"Registry exposure: a NodePort registry auto-advertises the node address; for ClusterIP/ingress set registry.advertiseHost (or --registry-host).",
		"If you chose kube-auth, the token audience must match the server's CORNUS_JWT_AUDIENCE.",
	}
	stepsURL = []string{
		"Try any command against it, e.g. cornus --context NAME version.",
		"For extra transport options (mTLS, via-server, conduit/SOCKS5) see: cornus config set-context --help.",
	}
)

func stepsFor(s Scenario) []string {
	switch s {
	case ScenarioLocal:
		return stepsLocal
	case ScenarioSSHDocker:
		return stepsSSHDocker
	case ScenarioSSHContainerd:
		return stepsSSHContainerd
	case ScenarioKubePortForward, ScenarioKubeURL:
		return stepsKube
	default:
		return stepsURL
	}
}

func docPathFor(s Scenario) string {
	switch s {
	case ScenarioSSHDocker, ScenarioSSHContainerd:
		return "/guides/remote-docker-hosts"
	case ScenarioKubePortForward, ScenarioKubeURL:
		return "/guides/remote-clusters (helm values: /reference/helm-values)"
	default:
		return "/cli/setup"
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
// set-context command, and a doc pointer on stderr.
func (w *Wizard) guidance(a *Answers, name string, ctx *clientconfig.Context) {
	d := w.d
	d.Success("setup complete for context %q", name)

	_ = d.KV().
		Add("context", name).
		Add("server", describeServer(ctx)).
		Add("config", w.configPath).
		Flush()

	d.Info("next steps:")
	for i, s := range stepsFor(a.Scenario) {
		d.Info("  %d. %s", i+1, s)
	}
	d.Info("equivalent command:")
	d.Info("  %s", SetContextCommand(name, ctx))
	if ctx.Token != "" {
		d.Info("  (replace REDACTED with the real bearer token)")
	}
	d.Info("docs: %s", docPathFor(a.Scenario))
}
