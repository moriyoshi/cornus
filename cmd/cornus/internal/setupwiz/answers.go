package setupwiz

import (
	"fmt"
	"strconv"
	"strings"

	"cornus/pkg/clientconfig"
)

// Scenario is the deployment topology the wizard is configuring a profile for.
type Scenario int

const (
	// ScenarioLocal is a cornus serve on this machine (plain HTTP loopback).
	ScenarioLocal Scenario = iota
	// ScenarioSSHDocker reaches a docker host over an SSH tunnel.
	ScenarioSSHDocker
	// ScenarioSSHContainerd reaches a containerd host over an SSH tunnel.
	ScenarioSSHContainerd
	// ScenarioKubePortForward reaches an in-cluster install via auto port-forward.
	ScenarioKubePortForward
	// ScenarioKubeURL reaches an in-cluster install via a direct URL (ingress).
	ScenarioKubeURL
	// ScenarioURL reaches a server at an already-known URL.
	ScenarioURL
	// ScenarioDockerContainer is a server running as a CONTAINER on the local
	// docker host it manages. The profile it produces is an ordinary loopback
	// one — what the scenario really adds is the run artifact, since the bind
	// mounts are the whole difficulty and getting one wrong fails silently.
	ScenarioDockerContainer
	// ScenarioSSHBare reaches a daemonless host over an SSH tunnel: the server
	// there drives an OCI runtime (runc/crun/youki) directly, with no dockerd
	// and no containerd.
	ScenarioSSHBare
	// ScenarioSSHIncus reaches an Incus host over an SSH tunnel. The incus
	// client is unix-socket-only, so the server necessarily runs ON the incus
	// daemon host and the client tunnels to it.
	ScenarioSSHIncus
	// ScenarioContainerdContainer is a server running as a CONTAINER on the local
	// containerd host it manages.
	//
	// Distinct from ScenarioDockerContainer rather than a backend answer on it,
	// because the artifact it produces is a different command with different
	// binds: `ctr run` rather than `docker run` (self-inspection only resolves
	// when the containerd being deployed through is the one that created the
	// server's container), plus the netns-pin bind and host networking that
	// backend's CNI plumbing needs.
	ScenarioContainerdContainer
	// ScenarioIncusContainer is a server running as a CONTAINER beside the incusd
	// it manages. It needs only the daemon socket — this backend hands incusd no
	// path of its own — plus host networking for a route to the incus bridge.
	ScenarioIncusContainer
)

// Deploy-backend identifiers, spelled as CORNUS_DEPLOY_BACKEND takes them. The
// empty string is dockerhost, which is the server's default and so is left
// unset rather than stated. They name the SERVER's runtime, never anything the
// client profile carries.
const (
	backendDocker = ""
	// backendPodman drives Podman over its NATIVE libpod API. Unlike every other
	// backend it has no default endpoint: how to reach podman is a separate,
	// explicit choice (CORNUS_PODMAN_SOCKET or CORNUS_PODMAN_SERVICE), which is
	// why its prerequisite line is longer than the others'.
	backendPodman     = "podman"
	backendContainerd = "containerd"
	backendBare       = "bare"
	backendIncus      = "incus"
	// backendKubernetes drives a cluster through client-go. It is "server-only"
	// in the sense that a serverless `cornus deploy` refuses it and falls back to
	// dockerhost — but `cornus serve` IS a server, so a local one selecting it is
	// a first-class setup: the backend tries in-cluster config, then the ordinary
	// KUBECONFIG / ~/.kube/config rules, which is how one binary deploys into a
	// kind cluster from a developer machine.
	backendKubernetes = "kubernetes"
)

// Answers is the flat set of values the wizard collects, before they are folded
// into a Context. Every field is optional; BuildContext emits only the non-zero
// ones. Keeping it flat (rather than one struct per scenario) lets BuildContext
// and SetContextCommand be pure, table-testable functions.
type Answers struct {
	Scenario Scenario
	Name     string

	Server              string
	RegistryHost        string
	Token               string
	KeyAuthIdentityFile string
	KeyAuthFingerprint  string

	// TLS material.
	CACert     string
	ServerName string
	Insecure   bool

	// Server-in-a-container: the HOST directory bound as the server's data dir.
	// The bind is what lets the daemon resolve the paths the server hands it.
	ContainerDataDir string

	// Containerized records that the server on the far end of an SSH tunnel runs
	// as a container rather than an installed binary. It changes only the guidance
	// and the artifacts — the tunnel reaches a published port either way, so the
	// saved profile is identical.
	Containerized bool

	// SSH tunnel.
	SSHHost         string
	SSHUser         string
	SSHIdentityFile string
	SSHRemoteAddr   string
	SSHTLS          bool

	// Kubernetes port-forward.
	KubeContext  string
	Namespace    string
	PFService    string
	PFRemotePort int

	// Kube-auth (cluster-minted ServiceAccount token).
	KubeAuthServiceAccount string
	KubeAuthAudience       string
	KubeAuthNamespace      string

	// Ingress-via-conduit (kube scenarios). IngressMode is "native", "emulate", or ""
	// (off); a non-empty value also selects the SOCKS5 conduit. The controller fields
	// apply to native mode (empty asks the client to learn the controller from the
	// server's /info at run time).
	IngressMode                string
	IngressControllerNamespace string
	IngressControllerService   string
	IngressControllerHTTPPort  int
	IngressControllerHTTPSPort int

	// Ingress tunnels. TunnelIngressHostMode is the default --host-mode for
	// `cornus ingress-tunnel` ("" leaves it unset, i.e. auto); TunnelAuthTokenFile
	// is a PATH to the tunnel credential, never the credential itself.
	TunnelIngressHostMode string
	TunnelAuthTokenFile   string

	// ServerReady records whether the user said the server is already set up. It
	// gates everything that needs a live server — the ingress probe, the SSH-key
	// enrollment, the connection test — so the wizard never spends a timeout on a
	// call that cannot succeed. It is not stored in the Context.
	ServerReady bool

	// LocalBackend is the CORNUS_DEPLOY_BACKEND value the not-yet-started local
	// server will drive (empty = the dockerhost default). It exists so the setup
	// guide can name that backend's real prerequisites; like ContainerDataDir it
	// is not stored in the Context.
	LocalBackend string
}

// BuildContext folds the answers into a clientconfig.Context, setting only the
// blocks whose source fields are non-zero. It is pure — no I/O — so the wizard can
// materialize (clientconfig.Save) at a single late atomic point, and so tests can
// assert the mapping directly.
func BuildContext(a Answers) *clientconfig.Context {
	ctx := &clientconfig.Context{}
	if a.Server != "" {
		ctx.Server = a.Server
	}
	if a.RegistryHost != "" {
		ctx.RegistryHost = a.RegistryHost
	}
	if a.Token != "" {
		ctx.Token = a.Token
	}
	if a.KeyAuthIdentityFile != "" || a.KeyAuthFingerprint != "" {
		ctx.KeyAuth = &clientconfig.KeyAuth{
			IdentityFile:   a.KeyAuthIdentityFile,
			KeyFingerprint: a.KeyAuthFingerprint,
			Name:           a.Name,
		}
	}
	if a.CACert != "" || a.ServerName != "" || a.Insecure {
		ctx.TLS = &clientconfig.TLS{
			CACert:             a.CACert,
			ServerName:         a.ServerName,
			InsecureSkipVerify: a.Insecure,
		}
	}
	if a.SSHHost != "" {
		ctx.SSHTunnel = &clientconfig.SSHTunnel{
			Addr:         a.SSHHost,
			User:         a.SSHUser,
			RemoteAddr:   a.SSHRemoteAddr,
			IdentityFile: a.SSHIdentityFile,
			RemoteTLS:    a.SSHTLS,
		}
	}
	if a.KubeContext != "" || a.Namespace != "" || a.PFService != "" || a.PFRemotePort != 0 {
		ctx.PortForward = &clientconfig.PortForward{
			KubeContext: a.KubeContext,
			Namespace:   a.Namespace,
			Service:     a.PFService,
			RemotePort:  a.PFRemotePort,
		}
	}
	if a.KubeAuthServiceAccount != "" || a.KubeAuthAudience != "" || a.KubeAuthNamespace != "" {
		ctx.KubeAuth = &clientconfig.KubeAuth{
			ServiceAccount: a.KubeAuthServiceAccount,
			Audience:       a.KubeAuthAudience,
			Namespace:      a.KubeAuthNamespace,
		}
	}
	if a.IngressMode != "" {
		if ctx.Conduit == nil {
			ctx.Conduit = &clientconfig.Conduit{}
		}
		// Ingress-via-conduit rides the SOCKS5 proxy, so enabling it selects that mode.
		ctx.Conduit.Mode = "socks5"
		ing := &clientconfig.Ingress{Mode: a.IngressMode}
		if a.IngressMode == "native" && a.IngressControllerService != "" {
			ing.Controller = &clientconfig.IngressController{
				Namespace: a.IngressControllerNamespace,
				Service:   a.IngressControllerService,
				HTTPPort:  a.IngressControllerHTTPPort,
				HTTPSPort: a.IngressControllerHTTPSPort,
			}
		}
		ctx.Conduit.Ingress = ing
	}
	if a.TunnelIngressHostMode != "" || a.TunnelAuthTokenFile != "" {
		ctx.Tunnel = &clientconfig.Tunnel{
			IngressHostMode: a.TunnelIngressHostMode,
			AuthTokenFile:   a.TunnelAuthTokenFile,
		}
	}
	return ctx
}

// SetContextArgs returns the `config set-context` argv (name first, then flags)
// that reproduces ctx, derived from the built Context so it cannot drift from what
// was saved. A non-empty token is redacted to the literal REDACTED (the caller
// notes that it must be replaced). The prefix ("cornus config set-context") is not
// included, so the slice feeds straight into a kong parse in tests.
func SetContextArgs(name string, ctx *clientconfig.Context) []string {
	args := []string{name}
	add := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	add("--server", ctx.Server)
	add("--registry-host", ctx.RegistryHost)
	if ctx.Token != "" {
		args = append(args, "--token", "REDACTED")
	}
	if ctx.TLS != nil {
		add("--tls-ca-cert", ctx.TLS.CACert)
		add("--tls-server-name", ctx.TLS.ServerName)
		if ctx.TLS.InsecureSkipVerify {
			args = append(args, "--insecure-skip-verify")
		}
	}
	if st := ctx.SSHTunnel; st != nil {
		add("--ssh-host", st.Addr)
		add("--ssh-user", st.User)
		add("--ssh-remote-addr", st.RemoteAddr)
		add("--ssh-identity-file", st.IdentityFile)
		if st.RemoteTLS {
			args = append(args, "--ssh-tls")
		}
	}
	if pf := ctx.PortForward; pf != nil {
		add("--pf-kube-context", pf.KubeContext)
		add("--namespace", pf.Namespace)
		add("--pf-service", pf.Service)
		if pf.RemotePort != 0 {
			args = append(args, "--pf-remote-port", strconv.Itoa(pf.RemotePort))
		}
	}
	if ka := ctx.KubeAuth; ka != nil {
		add("--kube-auth-service-account", ka.ServiceAccount)
		add("--kube-auth-audience", ka.Audience)
		add("--kube-auth-namespace", ka.Namespace)
	}
	if ka := ctx.KeyAuth; ka != nil {
		add("--key-auth-identity-file", ka.IdentityFile)
		add("--key-auth-fingerprint", ka.KeyFingerprint)
		add("--key-auth-name", ka.Name)
		add("--key-auth-scope", ka.Scope)
		add("--key-auth-ttl", ka.TTL)
	}
	if cd := ctx.Conduit; cd != nil {
		add("--conduit-mode", cd.Mode)
		if in := cd.Ingress; in != nil {
			add("--ingress-conduit", in.Mode)
			if c := in.Controller; c != nil {
				add("--ingress-controller", fmt.Sprintf("%s/%s:%d/%d", c.Namespace, c.Service, c.HTTPPort, c.HTTPSPort))
			}
			add("--ingress-emulate-ca", in.CAFile)
			add("--ingress-emulate-ca-key", in.CAKeyFile)
		}
	}
	return args
}

// SetContextCommand renders SetContextArgs as a copy-pasteable POSIX shell line,
// so a user can reproduce or script the profile the wizard just built.
func SetContextCommand(name string, ctx *clientconfig.Context) string {
	parts := []string{"cornus", "config", "set-context"}
	for _, a := range SetContextArgs(name, ctx) {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote quotes s for a POSIX shell: bare when it has no metacharacters, else
// single-quoted with embedded single quotes escaped as '\”.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, shellSpecial) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSpecial reports whether r needs quoting in a POSIX shell word.
func shellSpecial(r rune) bool {
	switch r {
	case '_', '-', '.', '/', ':', '@', '+', '%', ',', '=':
		return false
	}
	return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
}
