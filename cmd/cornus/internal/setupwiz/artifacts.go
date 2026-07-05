package setupwiz

import (
	"fmt"
	"net/url"
	"strings"
	"text/template"
)

// systemdData parameterizes the systemd unit. It serves both the remote-host
// scenarios and the local daemonless one, so the listen address is just Addr.
type systemdData struct {
	Addr string
	// Backend is the CORNUS_DEPLOY_BACKEND the unit selects: "" (the dockerhost
	// default, left unstated), "containerd", "bare", or "incus".
	Backend string
}

// NeedsRoot reports whether the backend has to run as root. containerd and bare
// both drive namespaces, cgroups, and CNI directly; docker and incus talk to a
// daemon that does that for them.
func (d systemdData) NeedsRoot() bool {
	return d.Backend == backendContainerd || d.Backend == backendBare
}

// helmData parameterizes the kube helm values snippet.
type helmData struct {
	Exposure      string // nodePort | clusterIP | ingress
	AdvertiseHost string
	Audience      string
}

// systemdTemplate is the remote-host unit. The per-backend comments name the
// prerequisite each backend actually has, because none of them fails at unit
// start: a missing OCI runtime, absent CNI plugins, or an incusd without
// skopeo/umoci all surface later as a failed deploy, by which point the unit
// looks healthy and the cause is three layers away.
var systemdTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description=cornus server
After=network-online.target
Wants=network-online.target
{{- if eq .Backend "incus"}}
# incusd must be up before cornus opens its socket.
After=incus.service
{{- end}}

[Service]
Environment=CORNUS_DATA=/var/lib/cornus
{{- if .Backend}}
Environment=CORNUS_DEPLOY_BACKEND={{.Backend}}
{{- end}}
{{- if eq .Backend "bare"}}
# Daemonless: cornus drives an OCI runtime itself. runc is the default; set
# CORNUS_BARE_RUNTIME to use crun, youki, or runsc (gVisor) instead.
#Environment=CORNUS_BARE_RUNTIME=crun
{{- end}}
{{- if eq .Backend "incus"}}
# Defaults shown; set them only to override.
#Environment=CORNUS_INCUS_SOCKET=/var/lib/incus/unix.socket
#Environment=CORNUS_INCUS_PROJECT=default
{{- end}}
ExecStart=/usr/local/bin/cornus serve --addr {{.Addr}}
Restart=on-failure
{{- if eq .Backend "containerd"}}
# The containerd backend needs root and the CNI plugins in /opt/cni/bin.
{{- end}}
{{- if eq .Backend "bare"}}
# The bare backend needs root (snapshotter mounts, netns, cgroups) plus an OCI
# runtime on PATH and the CNI plugins in /opt/cni/bin.
{{- end}}
{{- if .NeedsRoot}}
User=root
{{- end}}
{{- if eq .Backend "incus"}}
# Needs access to the incus socket: run as root, or add this unit's user to the
# incus-admin group. incusd must be 6.3+ (older releases have no OCI support),
# and skopeo + umoci must be installed ON THIS HOST — incusd shells out to them
# to flatten the image.
{{- end}}

[Install]
WantedBy=multi-user.target
`))

var helmTemplate = template.Must(template.New("values").Parse(`deployBackend: kubernetes
registry:
  exposure: {{.Exposure}}
{{- if .AdvertiseHost}}
  advertiseHost: {{.AdvertiseHost}}
{{- end}}
{{- if .Audience}}
auth:
  jwt:
    audience: {{.Audience}}
{{- end}}
`))

func renderSystemd(d systemdData) (string, error) {
	if d.Addr == "" {
		d.Addr = "127.0.0.1:5000"
	}
	var b strings.Builder
	if err := systemdTemplate.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

func renderHelm(d helmData) (string, error) {
	if d.Exposure == "" {
		d.Exposure = "nodePort"
	}
	var b strings.Builder
	if err := helmTemplate.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

var (
	sshArtifactNotes = []string{
		"copy it to the remote host: scp cornus.service HOST:/etc/systemd/system/",
		"enable it: ssh HOST 'sudo systemctl daemon-reload && sudo systemctl enable --now cornus'",
	}
	localUnitNotes = []string{
		"install it: sudo cp cornus.service /etc/systemd/system/",
		"enable it: sudo systemctl daemon-reload && sudo systemctl enable --now cornus",
	}
	helmArtifactNotes = []string{
		"install with it: helm install cornus oci://ghcr.io/moriyoshi/charts/cornus -f cornus-values.yaml",
	}
)

// writeArtifacts offers the scenario's setup artifact (a systemd unit for SSH
// hosts, a helm values snippet for kube), each ask-before-write.
func (w *Wizard) writeArtifacts(a *Answers) error {
	// A containerized remote server has no binary for a unit to start, and
	// `--restart unless-stopped` already covers the reboot the unit would.
	if isSSHScenario(a.Scenario) && !a.Containerized {
		return w.offerArtifact("cornus.service", sshArtifactNotes, func() (string, error) {
			return renderSystemd(systemdData{Addr: a.SSHRemoteAddr, Backend: scenarioBackend(a)})
		})
	}
	// A local daemonless server gets the same offer: on `bare` cornus supervises
	// the workloads itself, so running it from a shell is not a lesser setup but
	// an unsupervised one. The other local backends delegate supervision to their
	// daemon, so a foreground `cornus serve` remains a reasonable dev loop and no
	// unit is pushed on them.
	if a.Scenario == ScenarioLocal && a.LocalBackend == backendBare {
		return w.offerArtifact("cornus.service", localUnitNotes, func() (string, error) {
			return renderSystemd(systemdData{Addr: hostPortOf(a.Server), Backend: a.LocalBackend})
		})
	}
	switch a.Scenario {
	case ScenarioKubePortForward, ScenarioKubeURL:
		return w.offerArtifact("cornus-values.yaml", helmArtifactNotes, func() (string, error) {
			idx, err := w.ui.Select("Registry exposure for the helm values", "", []Option{
				{Label: "NodePort", Desc: "auto-advertises the node address (default)"},
				{Label: "ClusterIP", Desc: "in-cluster only; set advertiseHost"},
				{Label: "Ingress", Desc: "behind an ingress; set advertiseHost"},
			}, 0)
			if err != nil {
				return "", err
			}
			exposure := []string{"nodePort", "clusterIP", "ingress"}[idx]
			return renderHelm(helmData{Exposure: exposure, AdvertiseHost: a.RegistryHost, Audience: a.KubeAuthAudience})
		})
	}
	return nil
}

// portOf extracts the port from a server URL, defaulting to 5000. The artifact
// publishes and binds that port, so it must match the profile the wizard just
// saved or the context points at nothing.
func portOf(server string) string {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil {
		return "5000"
	}
	if p := u.Port(); p != "" {
		return p
	}
	return "5000"
}

// hostPortOf extracts host:port from a server URL for a unit's --addr,
// defaulting to the documented loopback default when the URL says nothing
// usable. A unit that listened somewhere other than the profile just saved
// would point the context at nothing.
func hostPortOf(server string) string {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || u.Host == "" {
		return "127.0.0.1:5000"
	}
	if u.Port() == "" {
		return u.Hostname() + ":5000"
	}
	return u.Host
}

// offerArtifact runs the {Write, Print, Skip} choice for one artifact. render is
// called lazily (only when not skipped) so a Skip never asks the artifact's
// follow-up questions. A Write onto an existing file confirms the overwrite and
// falls back to printing when declined.
func (w *Wizard) offerArtifact(name string, notes []string, render func() (string, error)) error {
	idx, err := w.ui.Select(fmt.Sprintf("Setup artifact: %s", name), "", []Option{
		{Label: "Write to a file", Desc: "create " + name + " in the current directory"},
		{Label: "Print to stdout", Desc: ""},
		{Label: "Skip", Desc: ""},
	}, 0)
	if err != nil {
		return err
	}
	if idx == 2 {
		return nil
	}
	content, err := render()
	if err != nil {
		return err
	}
	if idx == 1 {
		w.printArtifact(name, content, notes)
		return nil
	}
	// Write, guarding an existing file.
	if _, serr := w.Stat(name); serr == nil {
		ok, err := w.ui.Confirm(fmt.Sprintf("%s already exists. Overwrite it?", name), false)
		if err != nil {
			return err
		}
		if !ok {
			w.printArtifact(name, content, notes)
			return nil
		}
	}
	if err := w.WriteFile(name, []byte(content), 0o644); err != nil {
		return err
	}
	w.d.Done("wrote %s", name)
	for _, n := range notes {
		w.d.Info("%s", n)
	}
	return nil
}

// printArtifact writes the artifact to stdout (a result the user may pipe) and
// its follow-up notes to stderr.
func (w *Wizard) printArtifact(name, content string, notes []string) {
	w.d.Info("--- %s ---", name)
	fmt.Fprint(w.d.Out(), content)
	for _, n := range notes {
		w.d.Info("%s", n)
	}
}
