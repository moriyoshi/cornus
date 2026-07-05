package setupwiz

import (
	"fmt"
	"strings"
	"text/template"
)

// systemdData parameterizes the remote-host systemd unit.
type systemdData struct {
	RemoteAddr string
	Containerd bool
}

// helmData parameterizes the kube helm values snippet.
type helmData struct {
	Exposure      string // nodePort | clusterIP | ingress
	AdvertiseHost string
	Audience      string
}

var systemdTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description=cornus server
After=network-online.target
Wants=network-online.target

[Service]
Environment=CORNUS_DATA=/var/lib/cornus
{{- if .Containerd}}
Environment=CORNUS_DEPLOY_BACKEND=containerd
{{- end}}
ExecStart=/usr/local/bin/cornus serve --addr {{.RemoteAddr}}
Restart=on-failure
{{- if .Containerd}}
# containerd backend needs root and CNI plugins in /opt/cni/bin
User=root
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
	if d.RemoteAddr == "" {
		d.RemoteAddr = "127.0.0.1:5000"
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
	helmArtifactNotes = []string{
		"install with it: helm install cornus oci://ghcr.io/moriyoshi/charts/cornus -f cornus-values.yaml",
	}
)

// writeArtifacts offers the scenario's setup artifact (a systemd unit for SSH
// hosts, a helm values snippet for kube), each ask-before-write.
func (w *Wizard) writeArtifacts(a *Answers) error {
	switch a.Scenario {
	case ScenarioSSHDocker, ScenarioSSHContainerd:
		containerd := a.Scenario == ScenarioSSHContainerd
		return w.offerArtifact("cornus.service", sshArtifactNotes, func() (string, error) {
			return renderSystemd(systemdData{RemoteAddr: a.SSHRemoteAddr, Containerd: containerd})
		})
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
