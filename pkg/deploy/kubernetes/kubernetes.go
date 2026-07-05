// Package kubernetes implements cornus's deploy.Backend against a Kubernetes
// cluster using client-go. A DeploySpec becomes a Deployment (plus a ClusterIP
// Service when it publishes ports). It works both in-cluster (the pod's
// ServiceAccount) and out-of-cluster (KUBECONFIG / default kubeconfig), so the
// same binary can deploy into a kind cluster from a developer machine.
package kubernetes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	scheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport"
	"k8s.io/client-go/transport/spdy"
	utilexec "k8s.io/client-go/util/exec"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"cornus/pkg/api"
	"cornus/pkg/caretaker"
	"cornus/pkg/creddelivery"
	"cornus/pkg/deploy"
	"cornus/pkg/deploy/kubernetes/internal/netdriver"
	"cornus/pkg/egresspolicy"
	"cornus/pkg/hub"
	"cornus/pkg/logging"
	"cornus/pkg/observability"
	"cornus/pkg/remotecompanion"
)

// replicasAnnotation remembers the desired replica count across a stop (scale to
// zero) so start can restore it.
const replicasAnnotation = "cornus.dev/replicas"

// restartAnnotation triggers a rollout when changed (like kubectl rollout restart).
const restartAnnotation = "cornus.dev/restartedAt"

// agentForwardAnnotation records whether the currently-applied spec had
// AgentForward set, so AgentForwardEnabled (which the server consults from
// handleDeployExecCreate/handleDeployExecAgentChannel, having only the
// deployment name at hand) can answer without decoding the caretaker's own
// config JSON.
const agentForwardAnnotation = "cornus.dev/agent-forward"

// managedIngressTLSLabel distinguishes certificate Secrets managed by Cornus
// from other deployment-owned Secrets and prevents overwriting external Secrets.
const managedIngressTLSLabel = "cornus.ingress-tls"

// Backend deploys onto a Kubernetes cluster.
type Backend struct {
	clientset kubernetes.Interface
	// restConfig drives the streaming exec/attach subresources (SPDY over the
	// pods/exec endpoint). Only New() populates it; NewWithClient (tests, fake
	// clientset) leaves it nil, which is fine because the unit tests never open a
	// stream.
	restConfig *rest.Config
	namespace  string
	// net realises user-network attachments (DeploySpec.Networks) via the
	// netdriver provider pipeline. Its dynamic client may be nil (tests, or a
	// config that cannot build one), in which case CRD-backed fabrics read as
	// unavailable and networks degrade to services-only DNS.
	net *netdriver.Engine
	// dyn is the dynamic client used for CRD-backed objects the typed clientset
	// cannot express — a Knative Serving Service (serving.knative.dev/v1). Nil on
	// the fake-clientset test path (NewWithClient) and when no config could build
	// one, in which case Knative reads as unavailable (knativeServed) and a
	// Knative deploy degrades to a plain Deployment. See knative.go.
	dyn dynamic.Interface
	// knativeCap caches whether the cluster serves serving.knative.dev/v1, probed
	// once via the discovery API (knativeServed).
	knativeCapOnce sync.Once
	knativeCapVal  bool
	// sidecarImage is the cornus image the caretaker / net-redirect / mount-agent
	// sidecars run (they need the cornus binary + iptables, independent of the
	// app image). Set by NewWithClients from CORNUS_K8S_SIDECAR_IMAGE, else the
	// server's own image discovered from its Pod (discoverSelfImage); empty only
	// when both are unavailable, in which case sidecarImageFor falls back to the
	// app image (works only when the app image already ships the cornus binary).
	sidecarImage string

	// allowPrivileged gates DeploySpec.Privileged (the user-requested privileged
	// app container — not cornus's own injected sidecars). Default false, since
	// the HTTP API is unauthenticated; New() sets it from CORNUS_ALLOW_PRIVILEGED.
	// Cluster PodSecurity admission is a separate, independent control.
	allowPrivileged bool

	// ingressDefaults are the server-wide fallbacks for DeploySpec.Ingress: a base
	// wildcard domain for host auto-derivation, a default IngressClassName, and a
	// default cert-manager cluster-issuer. From CORNUS_INGRESS_DOMAIN /
	// CORNUS_INGRESS_CLASS / CORNUS_INGRESS_TLS_ISSUER; all empty when unset.
	ingressDefaults ingressDefaults

	// caretakerToken is the SCOPED bearer token injected into server-bound caretaker
	// sidecars (mount / hub roles) so they authenticate to a server that has bearer
	// auth enabled. New() sets it from CORNUS_CARETAKER_TOKEN — a credential the
	// server accepts ONLY on /.cornus/v1/caretaker/attach, never on the client API or the
	// registry, so a sidecar credential leaked from a pod spec cannot deploy, build,
	// exec, or push images. Empty when unset (no auth, or the operator has not
	// configured a caretaker token — see AUTH_DESIGN_NOTE.md).
	//
	// caretakerTokenSecret / ...Key, when set (from CORNUS_CARETAKER_TOKEN_SECRET,
	// "name" or "name/key"), source the sidecar token from a Kubernetes Secret via
	// secretKeyRef instead of embedding the value — so the token never appears in the
	// pod spec as a literal. This is the hardened path; the embedded value is the
	// backward-compatible fallback. The referenced Secret must exist in the deploy
	// namespace; referencing it needs no extra cornus RBAC (the kubelet resolves it).
	caretakerToken          string
	caretakerTokenSecret    string
	caretakerTokenSecretKey string

	// caretakerTLSSecret, when set (from CORNUS_CARETAKER_TLS_SECRET, a Secret
	// name in the deploy namespace), makes every server-bound caretaker sidecar
	// (mount / hub roles) dial the server with TLS material projected from that
	// Secret: the backend mounts it read-only at caretakerTLSMountPath and points
	// the embedded caretaker config (Config.TLS) at the mounted paths. Keys follow
	// the kubernetes.io/tls convention — ca.crt (a PEM CA bundle added to the
	// system roots) plus, optionally, tls.crt / tls.key (an mTLS client pair). The
	// Secret is inspected at deploy time so a CA-only Secret works (server-auth
	// TLS without mTLS: the client-pair paths are omitted). Unset leaves pod specs
	// byte-identical to the pre-TLS shape. Like the token secret, referencing the
	// Secret from a pod needs no cornus RBAC (the kubelet resolves the volume);
	// only the key inspection reads it through the API (get, best-effort).
	caretakerTLSSecret string

	// clientToken / clientTokenSecret+Key are the CLIENT-scoped bearer token the
	// caretaker's Docker-API role (DeploySpec.Docker) authenticates with. Unlike
	// caretakerToken (accepted only on /.cornus/v1/caretaker/attach), this drives the full
	// client deploy API, so it is a deliberately separate, more-privileged credential
	// the operator provisions only for pods that opt into the Docker endpoint. Set
	// from CORNUS_CLIENT_TOKEN (literal fallback) / CORNUS_CLIENT_TOKEN_SECRET
	// ("name" or "name/key", the preferred secretKeyRef path so the token is never a
	// pod-spec literal). Empty when unset (no auth, or the Docker role is not used).
	clientToken          string
	clientTokenSecret    string
	clientTokenSecretKey string

	// mu guards sessions and the per-session exit state (exitCode/done). The
	// exec-session registry backs the Docker-style create/start/inspect/resize
	// split: Kubernetes exec is a single streaming call, so ExecCreate only
	// records the resolved pod + config, and ExecStart opens the stream.
	mu       sync.Mutex
	sessions map[string]*execSession
}

// execSession is a pending or in-flight exec against a resolved pod/container.
// sizeCh carries terminal-resize events from ExecResize to the running stream's
// TerminalSizeQueue (buffered so a resize before the stream starts is not lost;
// a full buffer drops the update). started is set when ExecStart opens the
// stream; exitCode/done are set when the stream ends. All three are read back
// by ExecInspect (guarded by Backend.mu).
type execSession struct {
	pod       string
	container string
	cfg       api.ExecConfig
	sizeCh    chan remotecommand.TerminalSize
	started   bool
	exitCode  int
	done      bool
	// finishedAt is when the stream ended (done set true), used to reap the
	// session from the registry after a grace period so long-lived backends do
	// not leak one entry per exec forever. Zero until finished.
	finishedAt time.Time
}

// execSessionTTL is how long a finished exec session is retained before it may
// be reaped from the registry. It only needs to outlast the caller's post-exec
// ExecInspect (which reads the exit code), so a few minutes is ample while
// still bounding the registry to in-flight execs plus recently-finished ones.
const execSessionTTL = 5 * time.Minute

// New builds a backend from in-cluster config, falling back to the local
// kubeconfig. The target namespace comes from CORNUS_K8S_NAMESPACE (default
// "default").
func New() (*Backend, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	// When telemetry is enabled, wrap the client-go transport so outgoing
	// Kubernetes API calls become child spans of the incoming request (context is
	// threaded through client-go). Gated on Enabled() so it is a strict no-op — no
	// wrapper installed at all — when telemetry is off. Set before NewForConfig,
	// which snapshots the config when it builds its transport.
	if observability.Enabled() {
		cfg.WrapTransport = wrapTransportOTel(cfg.WrapTransport)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	// The dynamic client serves CRD-backed network fabrics (Multus NADs).
	// Best-effort: without it those fabrics read as unavailable and networks
	// fall back to services-only DNS.
	dyn, _ := dynamic.NewForConfig(cfg)
	b := NewWithClients(cs, dyn, namespaceFromEnv())
	b.restConfig = cfg
	b.allowPrivileged = privilegedAllowedFromEnv()
	b.caretakerToken = os.Getenv("CORNUS_CARETAKER_TOKEN")
	b.caretakerTokenSecret, b.caretakerTokenSecretKey = parseSecretRef(os.Getenv("CORNUS_CARETAKER_TOKEN_SECRET"))
	b.caretakerTLSSecret = strings.TrimSpace(os.Getenv("CORNUS_CARETAKER_TLS_SECRET"))
	b.clientToken = os.Getenv("CORNUS_CLIENT_TOKEN")
	b.clientTokenSecret, b.clientTokenSecretKey = parseSecretRef(os.Getenv("CORNUS_CLIENT_TOKEN_SECRET"))
	return b, nil
}

// wrapTransportOTel returns a rest.Config transport wrapper that layers otelhttp
// instrumentation on top of any existing wrapper, so every client-go request to
// the Kubernetes API server becomes a client span propagating the caller's trace
// context. It composes with (never replaces) an existing WrapTransport.
func wrapTransportOTel(existing transport.WrapperFunc) transport.WrapperFunc {
	return func(rt http.RoundTripper) http.RoundTripper {
		if existing != nil {
			rt = existing(rt)
		}
		return otelhttp.NewTransport(rt)
	}
}

// parseSecretRef parses a "name" or "name/key" secret reference. An empty input
// yields empty results; a bare name defaults the key to "token".
func parseSecretRef(v string) (name, key string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	if i := strings.IndexByte(v, '/'); i >= 0 {
		name, key = v[:i], v[i+1:]
	} else {
		name = v
	}
	if key == "" {
		key = "token"
	}
	return name, key
}

// caretakerConfigEnv marshals a caretaker Config into the CORNUS_CARETAKER_CONFIG
// env var and, for server-bound configs (mount / hub roles), supplies the sidecar
// bearer token so it authenticates to a server with bearer auth enabled. The token
// is added only when the config actually dials the server, so the DNS- and
// proxy-only sidecars never carry it. Preferred path: a Secret secretKeyRef
// (CORNUS_CARETAKER_TOKEN_SECRET), so the token is not a literal in the pod spec;
// fallback: the value embedded in the config JSON (CORNUS_CARETAKER_TOKEN).
func (b *Backend) caretakerConfigEnv(cfg caretaker.Config, name string) []corev1.EnvVar {
	serverBound := len(cfg.Mounts) > 0 || len(cfg.Credentials) > 0 || cfg.Hub != nil || cfg.Egress != nil ||
		cfg.PortForward != nil || cfg.AgentRelay != nil
	var extra []corev1.EnvVar
	if serverBound {
		switch {
		case b.caretakerTokenSecret != "":
			// Hardened: the sidecar reads CORNUS_TOKEN from a Secret at runtime
			// (applyEnvToken), which takes precedence, so leave Config.Token unset —
			// the token never appears in the pod spec.
			extra = append(extra, corev1.EnvVar{
				Name: "CORNUS_TOKEN",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: b.caretakerTokenSecret},
					Key:                  b.caretakerTokenSecretKey,
				}},
			})
		case b.caretakerToken != "":
			cfg.Token = b.caretakerToken
		}
	}
	// The Docker role drives the CLIENT API with its own client-scoped token,
	// independent of the attach token above. Prefer a Secret secretKeyRef
	// (CORNUS_DOCKER_CLIENT_TOKEN, resolved at runtime by DockerRole.dockerClientToken)
	// so it is not a pod-spec literal; fallback: embed it in the config JSON. Copy the
	// role before mutating so the caller's struct is untouched.
	if cfg.Docker != nil {
		switch {
		case b.clientTokenSecret != "":
			d := *cfg.Docker
			d.Token = ""
			cfg.Docker = &d
			extra = append(extra, corev1.EnvVar{
				Name: "CORNUS_DOCKER_CLIENT_TOKEN",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: b.clientTokenSecret},
					Key:                  b.clientTokenSecretKey,
				}},
			})
		case b.clientToken != "":
			d := *cfg.Docker
			d.Token = b.clientToken
			cfg.Docker = &d
		}
	}
	// The Otel role's export headers may carry a secret (an auth token). Project
	// each from a Deployment-owned Secret (applyTelemetryHeaderSecret) via a
	// secretKeyRef env, and leave only the ENV-VAR NAME in the config JSON
	// (ExporterHeaderEnv) — the collector resolves the value from its process env
	// at runtime — so no header value appears in the pod spec. Copy the role
	// before mutating so the caller's struct is untouched.
	if cfg.Otel != nil && len(cfg.Otel.ExporterHeaders) > 0 {
		o := *cfg.Otel
		o.ExporterHeaderEnv = map[string]string{}
		for k := range cfg.Otel.ExporterHeaders {
			envVar := deploy.TelemetryHeaderEnvVar(k)
			o.ExporterHeaderEnv[k] = envVar
			extra = append(extra, corev1.EnvVar{
				Name: envVar,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: telemetryHeaderSecretName(name)},
					Key:                  envVar,
				}},
			})
		}
		o.ExporterHeaders = nil // the values now live in the Secret, not the config
		cfg.Otel = &o
	}
	raw, _ := json.Marshal(cfg)
	return append([]corev1.EnvVar{{Name: "CORNUS_CARETAKER_CONFIG", Value: string(raw)}}, extra...)
}

// caretakerContainerName is the sidecar container name every caretaker-injecting
// apply path uses (the "cornus-caretaker" literals throughout this file). Named
// here so the reverse read-back (ExistingMountSession) can find it.
const caretakerContainerName = "cornus-caretaker"

// ExistingMountSession implements deploy.MountSessionReader: it returns the
// deploy-attach mount session id already baked into name's currently-applied
// Deployment or Job caretaker sidecar, so the server can reuse it on re-apply and
// keep the id stable across client reconnects (see deploy.MountSessionReader). It
// returns "" — never an error — when name does not exist, has no caretaker, or
// carries no session; only a genuine API error (not NotFound) is surfaced.
func (b *Backend) ExistingMountSession(ctx context.Context, name string) (string, error) {
	tmpl, err := b.existingPodTemplate(ctx, name)
	if err != nil || tmpl == nil {
		return "", err
	}
	raw := caretakerConfigEnvValue(tmpl)
	if raw == "" {
		return "", nil
	}
	var cfg caretaker.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", nil // an unparsable/foreign config is treated as "no session"
	}
	return caretakerSessionID(cfg), nil
}

// existingPodTemplate returns name's currently-applied pod template — a Deployment
// (long-lived service) preferred, else a Job (one-shot) — or nil when neither
// exists. A NotFound on both is nil,nil; any other API error propagates.
func (b *Backend) existingPodTemplate(ctx context.Context, name string) (*corev1.PodSpec, error) {
	if dep, err := b.clientset.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return &dep.Spec.Template.Spec, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if job, err := b.clientset.BatchV1().Jobs(b.namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return &job.Spec.Template.Spec, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	return nil, nil
}

// caretakerConfigEnvValue returns the CORNUS_CARETAKER_CONFIG JSON from the pod's
// caretaker container (a native-sidecar init container or a regular container), or
// "" if there is none. The config JSON is always a pod-spec literal (only the
// token is projected from a Secret), so the session id is readable here.
func caretakerConfigEnvValue(pod *corev1.PodSpec) string {
	for _, list := range [][]corev1.Container{pod.InitContainers, pod.Containers} {
		for _, c := range list {
			if c.Name != caretakerContainerName {
				continue
			}
			for _, e := range c.Env {
				if e.Name == "CORNUS_CARETAKER_CONFIG" {
					return e.Value
				}
			}
		}
	}
	return ""
}

// caretakerSessionID extracts the shared deploy-attach session id from a caretaker
// config — every mount, credential, and egress role in one pod carries the same id
// (applyWithSidecarMounts mints one per pod) — or "" if the config has no session
// role.
func caretakerSessionID(cfg caretaker.Config) string {
	for _, m := range cfg.Mounts {
		if m.Session != "" {
			return m.Session
		}
	}
	for _, c := range cfg.Credentials {
		if c.Session != "" {
			return c.Session
		}
	}
	if cfg.Egress != nil && cfg.Egress.Session != "" {
		return cfg.Egress.Session
	}
	return ""
}

// telemetryHeaderSecretName is the Secret holding a deployment's Otel exporter
// header values (created by applyTelemetryHeaderSecret, referenced by the
// caretaker container's secretKeyRef env from caretakerConfigEnv).
func telemetryHeaderSecretName(deployment string) string { return "cornus-otel-hdr-" + deployment }

// applyTelemetryHeaderSecret creates/updates the Secret carrying the deployment's
// Otel exporter header values, owned by the workload so Kubernetes GC reclaims it
// on delete. No-op when telemetry is inactive or carries no headers.
func (b *Backend) applyTelemetryHeaderSecret(ctx context.Context, name string, t *api.TelemetrySpec, owner metav1.OwnerReference) error {
	if !t.Active() || len(t.Headers) == 0 {
		return nil
	}
	data := make(map[string]string, len(t.Headers))
	for k, v := range t.Headers {
		data[deploy.TelemetryHeaderEnvVar(k)] = v
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            telemetryHeaderSecretName(name),
			Namespace:       b.namespace,
			Labels:          labels(name),
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		StringData: data,
	}
	secrets := b.clientset.CoreV1().Secrets(b.namespace)
	if existing, err := secrets.Get(ctx, sec.Name, metav1.GetOptions{}); err == nil {
		sec.ResourceVersion = existing.ResourceVersion
		_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
		return err
	} else if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, sec, metav1.CreateOptions{})
		return err
	} else {
		return err
	}
}

// caretakerTLSMountPath is where the caretaker TLS Secret is projected into a
// server-bound sidecar (read-only); caretakerTLSVolume names the pod volume
// carrying it. The file names under the mount follow the kubernetes.io/tls key
// convention (ca.crt / tls.crt / tls.key).
const (
	caretakerTLSMountPath = "/cornus/tls"
	caretakerTLSVolume    = "cornus-caretaker-tls"
)

// caretakerTLSFiles resolves the caretaker.TLSFiles paths a server-bound sidecar
// loads at startup, or nil when the TLS knob is unset. The Secret's keys are
// inspected at deploy time so the paths reflect what will actually be mounted:
// a CA-only Secret yields just CAFile (server-auth TLS without mTLS), a Secret
// without ca.crt yields just the client pair (mTLS against publicly trusted
// server certs). A HALF-present client pair keeps both paths so the sidecar
// fails fast at startup (TLSFiles.load requires the pair together) instead of
// silently skipping mTLS. When the Secret cannot be read (missing, or no RBAC
// for secrets get) the full conventional layout is assumed with a loud warning:
// intended TLS must never silently degrade to a plaintext dial, and a missing
// Secret fails the pod's volume mount anyway.
func (b *Backend) caretakerTLSFiles(ctx context.Context) *caretaker.TLSFiles {
	if b.caretakerTLSSecret == "" {
		return nil
	}
	log := logging.FromContext(ctx, slog.String("component", "kubernetes"))
	full := &caretaker.TLSFiles{
		CAFile:   caretakerTLSMountPath + "/ca.crt",
		CertFile: caretakerTLSMountPath + "/tls.crt",
		KeyFile:  caretakerTLSMountPath + "/tls.key",
	}
	sec, err := b.clientset.CoreV1().Secrets(b.namespace).Get(ctx, b.caretakerTLSSecret, metav1.GetOptions{})
	if err != nil {
		log.WarnContext(ctx, "cannot inspect caretaker TLS secret; assuming the full ca.crt/tls.crt/tls.key layout (a CA-only secret then fails the sidecar at startup)",
			"secret", b.caretakerTLSSecret, "error", err)
		return full
	}
	has := func(k string) bool { _, ok := sec.Data[k]; return ok }
	tf := &caretaker.TLSFiles{}
	if has("ca.crt") {
		tf.CAFile = full.CAFile
	}
	if has("tls.crt") || has("tls.key") {
		tf.CertFile, tf.KeyFile = full.CertFile, full.KeyFile
	}
	if *tf == (caretaker.TLSFiles{}) {
		log.WarnContext(ctx, "caretaker TLS secret carries none of the conventional keys (ca.crt / tls.crt / tls.key); assuming the full layout so the sidecar fails fast instead of dialing without TLS",
			"secret", b.caretakerTLSSecret)
		return full
	}
	return tf
}

// addCaretakerTLS wires the caretaker TLS Secret into a server-bound sidecar:
// the pod gets a Secret volume, the caretaker container a read-only mount at
// caretakerTLSMountPath, and cfg.TLS the mounted file paths (so they ride the
// config JSON the container's env carries — call this BEFORE marshalling the
// config with caretakerConfigEnv). A strict no-op when the TLS knob is unset,
// keeping pod specs byte-identical.
func (b *Backend) addCaretakerTLS(ctx context.Context, podSpec *corev1.PodSpec, ctr *corev1.Container, cfg *caretaker.Config) {
	tf := b.caretakerTLSFiles(ctx)
	if tf == nil {
		return
	}
	cfg.TLS = tf
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: caretakerTLSVolume,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: b.caretakerTLSSecret},
		},
	})
	ctr.VolumeMounts = append(ctr.VolumeMounts, corev1.VolumeMount{
		Name:      caretakerTLSVolume,
		MountPath: caretakerTLSMountPath,
		ReadOnly:  true,
	})
}

// privilegedAllowedFromEnv reports whether CORNUS_ALLOW_PRIVILEGED opts the
// app container back into privileged mode (matching the dockerhost backend's
// env). Default (unset) is deny.
func privilegedAllowedFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORNUS_ALLOW_PRIVILEGED"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// checkPrivilege rejects a user-requested privileged app container unless the
// backend is configured to allow it. cornus's own injected sidecars (caretaker,
// net-redirect init) are unaffected — they are not built from spec.Privileged.
// AgentForwardEnabled implements deploy.AgentForwardCapable: it reports
// whether name's currently-applied spec had AgentForward set, by reading the
// agentForwardAnnotation off the live Deployment object (set in deployment()
// from spec.AgentForward on every Apply) rather than decoding the caretaker's
// own config JSON. Returns deploy.ErrNotFound (wrapped) when no such
// deployment exists, matching Status/Stop/Start/Restart's convention.
func (b *Backend) AgentForwardEnabled(ctx context.Context, name string) (bool, error) {
	dep, err := b.clientset.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("kubernetes: %s: %w", name, deploy.ErrNotFound)
		}
		return false, err
	}
	return dep.Annotations[agentForwardAnnotation] == "true", nil
}

func (b *Backend) checkPrivilege(spec api.DeploySpec) error {
	if spec.Privileged && !b.allowPrivileged {
		return fmt.Errorf("kubernetes: privileged containers are disabled by policy (set CORNUS_ALLOW_PRIVILEGED=1 to allow)")
	}
	return nil
}

// NewWithClient builds a backend over an existing clientset (used by tests with
// a fake clientset). No dynamic client: CRD-backed network fabrics are
// unavailable and networks degrade to services-only DNS.
func NewWithClient(cs kubernetes.Interface, namespace string) *Backend {
	return NewWithClients(cs, nil, namespace)
}

// NewWithClients builds a backend over existing typed and dynamic clients.
func NewWithClients(cs kubernetes.Interface, dyn dynamic.Interface, namespace string) *Backend {
	if namespace == "" {
		namespace = "default"
	}
	sidecarImage := os.Getenv("CORNUS_K8S_SIDECAR_IMAGE")
	if sidecarImage == "" {
		// No explicit override: discover this server's own running image so the
		// injected sidecars run the cornus binary regardless of the workload
		// image. Best-effort and non-fatal — falls back to the app image below.
		ctx, cancel := context.WithTimeout(context.Background(), selfImageTimeout)
		sidecarImage = discoverSelfImage(ctx, cs, namespace)
		cancel()
		if sidecarImage != "" {
			slog.Debug("kube deploy: discovered own image for sidecars", "image", sidecarImage)
		}
	}
	return &Backend{
		clientset:       cs,
		namespace:       namespace,
		net:             netdriver.New(cs, dyn, namespace),
		dyn:             dyn,
		sidecarImage:    sidecarImage,
		ingressDefaults: ingressDefaultsFromEnv(),
		sessions:        map[string]*execSession{},
	}
}

// selfContainerName is the name of the cornus server's own container in the
// StatefulSet / Deployment that runs it (see deploy/helm/cornus and
// deploy/k8s/cornus.yaml). discoverSelfImage matches on it to find "our" image.
const selfContainerName = "cornus"

// selfImageTimeout bounds the one self-Pod read at backend construction so a slow
// or unreachable API server cannot wedge startup; on timeout we fall back to the
// app image just as if discovery found nothing.
var selfImageTimeout = 5 * time.Second

// discoverSelfImage returns the image of this server's own cornus container by
// reading its own Pod, so the injected sidecars (caretaker, net-redirect,
// mount-agent) run the cornus binary even when the workload image does not ship
// it. Best-effort: "" when not running in a Pod, the Pod cannot be read, or no
// container named `cornus` is found. The Pod name comes from POD_NAME (downward
// API) or, failing that, the hostname (equal to the Pod name in Kubernetes, the
// same source server.go already trusts for the replica id); its namespace from
// POD_NAMESPACE or the backend namespace (the release namespace, where the
// server Pod lives and RBAC grants `pods get`).
func discoverSelfImage(ctx context.Context, cs kubernetes.Interface, namespace string) string {
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		if h, err := os.Hostname(); err == nil {
			podName = h
		}
	}
	if podName == "" {
		return ""
	}
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		ns = namespace
	}
	pod, err := cs.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == selfContainerName {
			return c.Image
		}
	}
	return ""
}

// sidecarImageFor returns the image the cornus sidecars (caretaker,
// net-redirect, mount-agent) run: the configured/discovered sidecar image (see
// NewWithClients — CORNUS_K8S_SIDECAR_IMAGE or the server's own discovered
// image), or the app image as a last-resort fallback. The app-image fallback
// only works when the app image itself ships the cornus binary; a sidecar
// pinned to `cornus` (the entrypoint) will otherwise fail to start.
func (b *Backend) sidecarImageFor(spec api.DeploySpec) string {
	if b.sidecarImage != "" {
		return b.sidecarImage
	}
	return spec.Image
}

func namespaceFromEnv() string {
	if ns := os.Getenv("CORNUS_K8S_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

func loadConfig() (*rest.Config, error) {
	cfg, err := func() (*rest.Config, error) {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, nil
		}
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	}()
	if err != nil {
		return nil, err
	}
	// Raise the client-go rate limiter well above its defaults (QPS 5 / Burst 10).
	// A single `compose up` deploys many services at once and each one's readiness
	// is polled every second (awaitReady, reportReconcile), so the default bucket is
	// exhausted almost immediately — every subsequent call then blocks ~1s on
	// "client-side throttling", starving PVC creation, scheduling, and status polling
	// until the whole bring-up crawls (and one-shot readiness waits time out). The
	// api-server's own priority/fairness is the real backpressure; this just stops
	// the client from throttling itself. Honor CORNUS_KUBE_QPS/CORNUS_KUBE_BURST for
	// operators who need to tune it back down.
	cfg.QPS = envFloat32("CORNUS_KUBE_QPS", 50)
	cfg.Burst = envInt("CORNUS_KUBE_BURST", 100)
	return cfg, nil
}

// Name returns the backend identifier.
func (b *Backend) Name() string { return "kubernetes" }

// Close is a no-op.
func (b *Backend) Close() error { return nil }

func labels(name string) map[string]string {
	return map[string]string{deploy.LabelManaged: "true", deploy.LabelApp: name}
}

// mergeAnnotations returns dst with every key of add copied in (allocating dst
// when nil), or nil when the result would be empty. add wins on a key clash.
func mergeAnnotations(dst, add map[string]string) map[string]string {
	if len(add) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]string, len(add))
	}
	for k, v := range add {
		dst[k] = v
	}
	return dst
}

// defaultVolumeSize is the storage request used for a managed volume when the
// VolumeSpec leaves Size empty (compose short-form anonymous volumes carry none).
const defaultVolumeSize = "1Gi"

// pvcName is the deterministic PVC name for the i-th ANONYMOUS volume of a
// deployment. It matches the ClaimName wired into the pod template so a repeated
// Apply reuses the same claim.
func pvcName(deployment string, i int) string {
	return fmt.Sprintf("%s-vol-%d", deployment, i)
}

// namedPVCName maps a NAMED volume's logical name (e.g. Compose's "myproj_cache",
// which is not a valid Kubernetes object name because of the underscore) to a
// stable, DNS-1123-safe PVC name. Every deployment referencing the same logical
// name resolves to the same claim, so they share one backing store. A short hash
// of the original disambiguates names that sanitise to the same string.
func namedPVCName(logical string) string {
	sum := sha256.Sum256([]byte(logical))
	base := sanitizeDNS1123(logical)
	if base == "" {
		base = "vol"
	}
	name := fmt.Sprintf("cornus-vol-%s-%s", base, hex.EncodeToString(sum[:4]))
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}

// sanitizeDNS1123 lowercases s and replaces every character outside [a-z0-9-]
// with '-', collapsing to a leading/trailing-trimmed label fragment.
func sanitizeDNS1123(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// sanitizeSubdomain sanitizes a dot-separated subdomain (e.g. "web.pr-123") to a
// DNS-1123 host fragment: each label is sanitized via sanitizeDNS1123 and empty
// labels are dropped, so "<service>.<project>" from raw compose names becomes a
// valid multi-label host prefix.
func sanitizeSubdomain(s string) string {
	var labels []string
	for _, part := range strings.Split(s, ".") {
		if l := sanitizeDNS1123(part); l != "" {
			labels = append(labels, l)
		}
	}
	return strings.Join(labels, ".")
}

// claimName is the PVC name backing the i-th managed volume of a spec: a stable
// shared name for a named volume, or the per-deployment anonymous name otherwise.
func claimName(spec api.DeploySpec, i int) string {
	if v := spec.Volumes[i]; v.Name != "" {
		return namedPVCName(v.Name)
	}
	return pvcName(spec.Name, i)
}

// volumeInitBase is where a populate initContainer mounts a managed volume's PVC
// (at a scratch path, NOT the target) so the image's baked content at the target
// stays visible and can be copied into the otherwise-empty volume.
const volumeInitBase = "/cornus/volinit"

// volumePopulateContainer builds the initContainer that seeds a freshly
// provisioned PVC from the image content at target, mirroring how Docker seeds an
// anonymous volume from the image. It mounts the same PVC (volName) at a scratch
// path and, ONLY when that volume is still empty (first start), copies the image's
// target directory into it. On restarts the volume already holds data, so the copy
// is skipped and the user's writes persist. This requires /bin/sh + cp/ls in the
// image — the same "full enough image" assumption the mount-agent path makes.
func volumePopulateContainer(image, volName, target string, i int) corev1.Container {
	scratch := fmt.Sprintf("%s/%d", volumeInitBase, i)
	script := fmt.Sprintf(
		`if [ -d %s ] && [ -z "$(ls -A %s 2>/dev/null)" ]; then cp -a %s/. %s/; fi`,
		shellQuote(target), shellQuote(scratch), shellQuote(target), shellQuote(scratch),
	)
	return corev1.Container{
		Name:            fmt.Sprintf("cornus-volinit-%d", i),
		Image:           image,
		Command:         []string{"/bin/sh", "-c", script},
		ImagePullPolicy: imagePullPolicy(),
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volName,
			MountPath: scratch,
		}},
	}
}

// shellQuote single-quotes s for safe interpolation into a /bin/sh -c script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// persistentVolumeClaim builds the PVC object for the i-th managed volume of a
// spec, requesting ReadWriteOnce access and an explicit StorageClassName only
// when the spec names one (nil selects the cluster default class).
//
// An anonymous volume's claim carries the deployment label and is named per
// deployment; applyDeployment owns it to the Deployment so it is reaped on
// delete. A named volume's claim is shared (a stable name derived from the
// logical name) and labelled only as cornus-managed — it is NOT owned by any
// deployment, so it survives `cornus delete` (Docker named-volume semantics).
func persistentVolumeClaim(spec api.DeploySpec, i int) *corev1.PersistentVolumeClaim {
	v := spec.Volumes[i]
	size := v.Size
	if size == "" {
		size = defaultVolumeSize
	}
	meta := metav1.ObjectMeta{Name: pvcName(spec.Name, i), Labels: labels(spec.Name)}
	if v.Name != "" {
		meta = metav1.ObjectMeta{
			Name:   namedPVCName(v.Name),
			Labels: map[string]string{deploy.LabelManaged: "true", deploy.LabelVolume: v.Name},
		}
	}
	// compose `volumes.<name>.labels` copy onto the PVC labels (best-effort; only
	// values that satisfy Kubernetes label syntax survive the apply — arbitrary
	// compose label values may not). cornus's own management labels are written
	// above and win on a key clash. The compose volume `driver` / `driver_opts`
	// are Docker-volume-plugin concepts with no PVC analogue (storage is chosen by
	// StorageClass) and are intentionally NOT mapped here.
	for k, val := range v.Labels {
		if _, taken := meta.Labels[k]; taken {
			continue
		}
		meta.Labels[k] = val
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: meta,
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
	if v.StorageClass != "" {
		sc := v.StorageClass
		pvc.Spec.StorageClassName = &sc
	}
	return pvc
}

// validateDockerRole rejects unsupported Docker-endpoint role combinations shared
// by every apply path: it cannot yet coexist with the enforcing proxy (which would
// redirect the endpoint's own client dials), and its transport must be one of the
// known forms.
func validateDockerRole(spec api.DeploySpec) error {
	if spec.Docker == nil {
		return nil
	}
	if spec.Proxy != nil {
		return fmt.Errorf("kubernetes: the Docker endpoint role cannot yet share a pod with the enforcing proxy (the proxy redirects the endpoint's own client dials)")
	}
	switch spec.Docker.Transport {
	case "", "tcp", "unix", "both":
		return nil
	default:
		return fmt.Errorf("kubernetes: docker.transport must be one of tcp, unix, both (got %q)", spec.Docker.Transport)
	}
}

// Apply creates or updates the Deployment (and Service) for a spec.
func (b *Backend) Apply(ctx context.Context, spec api.DeploySpec) (api.DeployStatus, error) {
	if spec.Name == "" || spec.Image == "" {
		return api.DeployStatus{}, fmt.Errorf("kubernetes: spec requires name and image")
	}
	if spec.Proxy != nil && (spec.DNS != nil || spec.Hub != nil) {
		return api.DeployStatus{}, fmt.Errorf("kubernetes: the enforcing proxy cannot yet share a pod with the caretaker DNS or hub role (conflicting egress interception)")
	}
	if err := validateDockerRole(spec); err != nil {
		return api.DeployStatus{}, err
	}
	if err := b.checkPrivilege(spec); err != nil {
		return api.DeployStatus{}, err
	}
	// Never turn a bind mount into a node hostPath: it is node-local, unsafe, and
	// almost never what the caller means. Client-local mounts must be streamed
	// into the pod via the deploy-attach sidecar path (see ApplyWithMounts).
	// (Mount.SELinux relabel is therefore N/A on k8s — there is no bind path to
	// relabel.)
	if len(spec.Mounts) > 0 {
		return api.DeployStatus{}, fmt.Errorf("kubernetes: bind mounts are not supported by the stateless deploy (cornus never uses hostPath); stream client-local mounts via the deploy-attach path — `cornus deploy --server <url> --local-mount ...` or stock docker/compose through `cornus daemon docker`")
	}
	return b.applyWorkload(ctx, spec, b.deployment(ctx, spec))
}

// ApplyWithMounts implements deploy.MountingBackend: each client-local mount
// becomes a live 9P mount inside the pod via a privileged native-sidecar
// mount-agent, so nothing is mounted on the node host and the pod can schedule
// anywhere. A bind mount with no matching client-local backing is rejected
// (cornus never realizes a bind mount as a node hostPath).
func (b *Backend) ApplyWithMounts(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount) (api.DeployStatus, error) {
	return b.ApplyWithAttachments(ctx, spec, mounts, nil, nil)
}

// ApplyWithAttachments implements deploy.AttachingBackend: it realizes both
// client-local mounts and client-sourced credentials in the SAME per-pod
// caretaker sidecar (one privileged sidecar for mounts; NET_ADMIN only when a
// credential binds a well-known address). ApplyWithMounts delegates here with no
// credentials.
func (b *Backend) ApplyWithAttachments(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount, creds []deploy.AttachCredential, egress *deploy.AttachEgress) (api.DeployStatus, error) {
	if spec.Name == "" || spec.Image == "" {
		return api.DeployStatus{}, fmt.Errorf("kubernetes: spec requires name and image")
	}
	if spec.Proxy != nil && (spec.DNS != nil || spec.Hub != nil) {
		return api.DeployStatus{}, fmt.Errorf("kubernetes: the enforcing proxy cannot yet share a pod with the caretaker DNS or hub role (conflicting egress interception)")
	}
	if egress != nil && spec.Proxy != nil {
		return api.DeployStatus{}, fmt.Errorf("kubernetes: client-side egress cannot share a pod with the enforcing proxy network (both intercept egress)")
	}
	if err := validateDockerRole(spec); err != nil {
		return api.DeployStatus{}, err
	}
	if egress != nil {
		if err := egress.Spec.Validate(); err != nil {
			return api.DeployStatus{}, fmt.Errorf("kubernetes: %w", err)
		}
	}
	if err := b.checkPrivilege(spec); err != nil {
		return api.DeployStatus{}, err
	}
	// A detached-primary network takes the pod off the cluster network, which is
	// exactly what a relay caretaker needs to reach the server: the mount agent for
	// the 9P relay, and a relay-mode egress caretaker (proxy/transparent) for the
	// egress relay. Env-mode egress needs no relay, so it is fine on a detached pod.
	if len(mounts) > 0 || (egress != nil && egress.Spec.NeedsRelay()) {
		who := "client-local mounts"
		if len(mounts) == 0 {
			who = "relay-mode client-side egress"
		}
		for _, n := range spec.Networks {
			if n.Default {
				return api.DeployStatus{}, fmt.Errorf("kubernetes: network %s: a default (detached) network is incompatible with %s — the caretaker needs the cluster network to reach the relay", n.Name, who)
			}
		}
		// proxy + mounts is supported: the single privileged caretaker runs both
		// roles, and (enforcing mode) is exempted from the egress redirect by a
		// firewall mark rather than by uid, since it must run as root for mounts.
		// See deploymentWithMounts / addProxyToMountCaretaker.
	}
	// Every bind mount must be handled by a sidecar; a mount with no matching
	// AttachMount would otherwise be silently dropped (we never fall back to
	// hostPath). Reject it instead.
	attach := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		attach[m.Target] = true
	}
	for _, m := range spec.Mounts {
		if !attach[m.Target] {
			return api.DeployStatus{}, fmt.Errorf("kubernetes: mount %q has no client-local 9P backing and cannot be a hostPath", m.Target)
		}
	}
	// The caretaker is needed only for RUNTIME attachments — mounts or a credential
	// with an endpoint/file delivery. An env-only credential is resolved at deploy
	// time into a Secret and needs no sidecar, so it uses the plain deployment
	// (plus the Secret below).
	desired := b.deployment(ctx, spec)
	if len(mounts) > 0 || hasRuntimeCredential(creds) || needsEgressCaretaker(egress) {
		desired = b.deploymentWithAttachments(ctx, spec, mounts, creds, egress)
	}
	// env-kind deliveries add a secretKeyRef env on the app container on EITHER
	// path (they are independent of the caretaker sidecar).
	b.addCredentialEnvVars(spec.Name, creds, &desired.Spec.Template.Spec)
	status, err := b.applyWorkload(ctx, spec, desired)
	if err != nil {
		return api.DeployStatus{}, err
	}
	// env-kind deliveries: materialize the server-resolved values into a Secret
	// owned by the Deployment (GC-reaped on delete); the pod already references
	// it via secretKeyRef.
	if err := b.applyCredentialEnvSecret(ctx, spec.Name, creds); err != nil {
		return api.DeployStatus{}, err
	}
	return status, nil
}

// credentialSecretName is the Secret holding a deployment's env-kind credential
// values (referenced by secretKeyRef on the app container).
func credentialSecretName(deployment string) string { return "cornus-cred-" + deployment }

// hasRuntimeCredential reports whether any credential needs a caretaker sidecar
// (an endpoint or file delivery). env-only credentials do not.
func hasRuntimeCredential(creds []deploy.AttachCredential) bool {
	for _, c := range creds {
		for _, d := range c.Deliver {
			if d.Kind != "env" { // endpoint/file need the caretaker; env does not
				return true
			}
		}
	}
	return false
}

// addCredentialEnvVars adds a secretKeyRef env on the app container for each
// env-kind delivery (the server pre-resolved the value into EnvVars). It runs on
// both the plain and the caretaker deployment paths. Env var names are valid
// Secret data keys, so key == var.
func (b *Backend) addCredentialEnvVars(deployment string, creds []deploy.AttachCredential, podSpec *corev1.PodSpec) {
	for _, c := range creds {
		for _, e := range c.EnvVars {
			podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, corev1.EnvVar{
				Name: e.Var,
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: credentialSecretName(deployment)},
					Key:                  e.Var,
				}},
			})
		}
	}
}

// applyCredentialEnvSecret creates/updates the Secret carrying every env-kind
// credential value for the deployment, owned by the Deployment so Kubernetes GC
// reclaims it on delete. No-op when there are no env deliveries.
func (b *Backend) applyCredentialEnvSecret(ctx context.Context, name string, creds []deploy.AttachCredential) error {
	data := map[string]string{}
	for _, c := range creds {
		for _, e := range c.EnvVars {
			data[e.Var] = e.Value
		}
	}
	if len(data) == 0 {
		return nil
	}
	dep, err := b.clientset.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("credential env secret: get deployment owner: %w", err)
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            credentialSecretName(name),
			Namespace:       b.namespace,
			Labels:          labels(name),
			OwnerReferences: []metav1.OwnerReference{deploymentOwnerRef(dep)},
		},
		StringData: data,
	}
	secrets := b.clientset.CoreV1().Secrets(b.namespace)
	if existing, err := secrets.Get(ctx, sec.Name, metav1.GetOptions{}); err == nil {
		sec.ResourceVersion = existing.ResourceVersion
		_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
		return err
	} else if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, sec, metav1.CreateOptions{})
		return err
	} else {
		return err
	}
}

// applyDeployment creates or updates the given Deployment, then the spec's
// Service and managed-volume PVCs — both owned by the Deployment so Kubernetes
// garbage-collects them when it is deleted (see Delete). The Deployment is
// created first because the owner reference needs its UID; the pod tolerates a
// briefly-missing PVC (it stays Pending until the claim appears).
func (b *Backend) applyDeployment(ctx context.Context, spec api.DeploySpec, desired *appsv1.Deployment) (api.DeployStatus, error) {
	deps := b.clientset.AppsV1().Deployments(b.namespace)
	var dep *appsv1.Deployment
	if _, err := deps.Get(ctx, spec.Name, metav1.GetOptions{}); err == nil {
		// Retry on optimistic-concurrency conflicts: the deployment controller
		// continually rewrites the object (status, revision annotations), so a
		// bare Get→Update races it and can surface a 409 (see updateDeployment).
		// Re-fetch the current ResourceVersion inside the loop before re-applying
		// the desired spec.
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cur, gerr := deps.Get(ctx, spec.Name, metav1.GetOptions{})
			if gerr != nil {
				return gerr
			}
			desired.ResourceVersion = cur.ResourceVersion
			updated, uerr := deps.Update(ctx, desired, metav1.UpdateOptions{})
			if uerr != nil {
				return uerr
			}
			dep = updated
			return nil
		}); err != nil {
			return api.DeployStatus{}, fmt.Errorf("update deployment: %w", err)
		}
	} else if apierrors.IsNotFound(err) {
		if dep, err = deps.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return api.DeployStatus{}, fmt.Errorf("create deployment: %w", err)
		}
	} else {
		return api.DeployStatus{}, err
	}
	if err := b.applyDependents(ctx, spec, deploymentOwnerRef(dep)); err != nil {
		return api.DeployStatus{}, err
	}
	return b.Status(ctx, spec.Name)
}

// applyDependents creates or updates the spec's Service, managed-volume PVCs,
// Ingress, and network provider objects, owned by the just-applied workload
// (Deployment or Job) so Kubernetes garbage-collects them when it is deleted. It
// is the shared tail of applyDeployment and applyJob — owner is the workload's
// owner reference (needs its UID, so the workload is created first).
// pvcDeleteTimeout bounds how long ensurePVC waits for a terminating claim of the
// same name to finish being garbage-collected before recreating it. pvcPollInterval
// is the poll cadence (a var only so tests can shrink it).
const pvcDeleteTimeout = 90 * time.Second

var pvcPollInterval = 250 * time.Millisecond

// ensurePVC creates pvc, tolerating a pre-existing LIVE claim (its spec is
// immutable, so an existing one is reused) but NOT a terminating one. A managed
// anonymous-volume PVC is owned by its workload, so removing the workload — a
// `compose down`, or a one-shot's server-side readiness-timeout teardown — cascades
// a Kubernetes garbage collection of the claim. A re-deploy's Create then races that
// GC: plain "ignore AlreadyExists" would silently ADOPT the doomed claim, and the
// new pod would wedge Unschedulable ("persistentvolumeclaim %q not found") the
// instant the GC completes — the observed one-shot bring-up failure. So on
// AlreadyExists we inspect the claim: a live one is reused; a terminating one is
// waited out (bounded by pvcDeleteTimeout and ctx) and then recreated fresh.
func (b *Backend) ensurePVC(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	pvcs := b.clientset.CoreV1().PersistentVolumeClaims(b.namespace)
	deadline := time.Now().Add(pvcDeleteTimeout)
	for {
		if _, err := pvcs.Create(ctx, pvc, metav1.CreateOptions{}); err == nil {
			return nil
		} else if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create pvc %s: %w", pvc.Name, err)
		}
		existing, err := pvcs.Get(ctx, pvc.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue // it finished terminating between our Create and Get — recreate
		}
		if err != nil {
			return fmt.Errorf("get pvc %s: %w", pvc.Name, err)
		}
		if existing.DeletionTimestamp == nil {
			return nil // a live claim of the same name — reuse it (spec is immutable)
		}
		// Terminating: wait for the GC to finish, then loop to recreate it fresh.
		if time.Now().After(deadline) {
			return fmt.Errorf("pvc %s stuck terminating after %s", pvc.Name, pvcDeleteTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pvcPollInterval):
		}
	}
}

func (b *Backend) applyDependents(ctx context.Context, spec api.DeploySpec, owner metav1.OwnerReference) error {
	// Network provider objects: shared network-scoped ones (NADs) created
	// idempotently and un-owned; workload-scoped ones (headless alias Services)
	// owner-ref'd so they are GC'd with the workload.
	if err := b.net.Apply(ctx, spec, labels(spec.Name), owner); err != nil {
		return err
	}

	// Otel exporter header Secret (sensitive auth headers projected via secretKeyRef
	// rather than a pod-spec config literal — see caretakerConfigEnv). No-op unless
	// telemetry is active with headers.
	if err := b.applyTelemetryHeaderSecret(ctx, spec.Name, spec.Telemetry, owner); err != nil {
		return err
	}

	// Managed-volume PVCs. Created once and left alone on re-apply (their spec is
	// immutable); an existing LIVE claim is reused. Anonymous claims are owned by the
	// workload so Kubernetes GC reaps them on delete; named (shared) claims are
	// left un-owned so they persist across a single deployment's delete.
	for i := range spec.Volumes {
		pvc := persistentVolumeClaim(spec, i)
		if spec.Volumes[i].Name == "" {
			pvc.OwnerReferences = []metav1.OwnerReference{owner}
		}
		if err := b.ensurePVC(ctx, pvc); err != nil {
			return err
		}
	}

	if svc := b.service(spec); svc != nil {
		svc.OwnerReferences = []metav1.OwnerReference{owner}
		svcs := b.clientset.CoreV1().Services(b.namespace)
		if existing, err := svcs.Get(ctx, spec.Name, metav1.GetOptions{}); err == nil {
			svc.ResourceVersion = existing.ResourceVersion
			svc.Spec.ClusterIP = existing.Spec.ClusterIP
			if _, err := svcs.Update(ctx, svc, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update service: %w", err)
			}
		} else if apierrors.IsNotFound(err) {
			if _, err := svcs.Create(ctx, svc, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("create service: %w", err)
			}
		} else {
			return err
		}
	}

	// Reconcile native-ingress TLS material before publishing references to it.
	if err := b.applyManagedIngressTLSSecrets(ctx, spec, owner); err != nil {
		return err
	}

	// Ingress: a public HTTP(S) front door for the Service, owner-ref'd to the
	// workload so Kubernetes GC reaps it on delete (same lifecycle as the
	// Service). Built only when the spec opts in.
	ing, err := b.ingress(spec)
	if err != nil {
		return err
	}
	if ing != nil {
		ing.OwnerReferences = []metav1.OwnerReference{owner}
		ings := b.clientset.NetworkingV1().Ingresses(b.namespace)
		if existing, err := ings.Get(ctx, spec.Name, metav1.GetOptions{}); err == nil {
			ing.ResourceVersion = existing.ResourceVersion
			if _, err := ings.Update(ctx, ing, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update ingress: %w", err)
			}
		} else if apierrors.IsNotFound(err) {
			if _, err := ings.Create(ctx, ing, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("create ingress: %w", err)
			}
		} else {
			return err
		}
	}
	return nil
}

// applyManagedIngressTLSSecrets creates or updates the kubernetes.io/tls Secrets
// carried in the deployment spec and removes obsolete Cornus-managed ingress
// certificate Secrets. Key material is never included in errors or logs.
func (b *Backend) applyManagedIngressTLSSecrets(ctx context.Context, spec api.DeploySpec, owner metav1.OwnerReference) error {
	// A deployment with no ingress TLS block manages no Cornus certificate
	// Secrets, so there is nothing to create, update, or prune. Return before any
	// Secret API call to keep `secrets` RBAC off the critical path for the common
	// case — a workload without ingress TLS must not require namespace Secret
	// access (least privilege). The prune List below is reachable only once a TLS
	// block is present, which is also the only way a Cornus-managed certificate
	// Secret could have been created, so nothing is leaked by skipping it here.
	if spec.Ingress == nil || spec.Ingress.TLS == nil || spec.Ingress.ClientEmulated {
		return nil
	}

	want := map[string]api.ManagedIngressCertificate{}
	for _, cert := range spec.Ingress.TLS.ManagedCertificates {
		name := strings.TrimSpace(cert.SecretName)
		if name == "" {
			return fmt.Errorf("ingress: managed TLS certificate requires a secretName")
		}
		if len(cert.CertificatePEM) == 0 || len(cert.PrivateKeyPEM) == 0 {
			return fmt.Errorf("ingress: managed TLS secret %q requires certificate and private key data", name)
		}
		if _, exists := want[name]; exists {
			return fmt.Errorf("ingress: managed TLS secret %q is declared more than once", name)
		}
		want[name] = cert
	}

	secrets := b.clientset.CoreV1().Secrets(b.namespace)
	for name, cert := range want {
		secretLabels := labels(spec.Name)
		secretLabels[managedIngressTLSLabel] = "true"
		desired := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Labels: secretLabels, OwnerReferences: []metav1.OwnerReference{owner}},
			Type:       corev1.SecretTypeTLS,
			Data: map[string][]byte{
				corev1.TLSCertKey:       append([]byte(nil), cert.CertificatePEM...),
				corev1.TLSPrivateKeyKey: append([]byte(nil), cert.PrivateKeyPEM...),
			},
		}
		existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			if _, err := secrets.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("create managed ingress TLS secret %q: %w", name, err)
			}
		case err != nil:
			return fmt.Errorf("get managed ingress TLS secret %q: %w", name, err)
		default:
			if existing.Labels[managedIngressTLSLabel] != "true" || existing.Labels[deploy.LabelApp] != spec.Name {
				return fmt.Errorf("ingress: refusing to overwrite existing Secret %q because it is not managed by Cornus for deployment %q", name, spec.Name)
			}
			desired.ResourceVersion = existing.ResourceVersion
			if _, err := secrets.Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("update managed ingress TLS secret %q: %w", name, err)
			}
		}
	}

	selector := fmt.Sprintf("%s=true,%s=%s", managedIngressTLSLabel, deploy.LabelApp, spec.Name)
	existing, err := secrets.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list managed ingress TLS secrets: %w", err)
	}
	for i := range existing.Items {
		name := existing.Items[i].Name
		if _, keep := want[name]; keep {
			continue
		}
		if err := secrets.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete obsolete managed ingress TLS secret %q: %w", name, err)
		}
	}
	return nil
}

// deploymentOwnerRef is the owner reference dependents (Service, PVCs) carry so
// Kubernetes garbage-collects them when the Deployment is deleted.
func deploymentOwnerRef(dep *appsv1.Deployment) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         "apps/v1",
		Kind:               "Deployment",
		Name:               dep.Name,
		UID:                dep.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// Status reports the Deployment's readiness as instances.
func (b *Backend) Status(ctx context.Context, name string) (api.DeployStatus, error) {
	dep, err := b.clientset.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// A one-shot workload is a Job, not a Deployment: fall through to it
			// before reporting "not found".
			if st, ok, jerr := b.jobStatus(ctx, name); jerr != nil {
				return api.DeployStatus{}, jerr
			} else if ok {
				return st, nil
			}
			// A Knative workload is a serving.knative.dev Service, not a
			// Deployment: fall through to it too.
			if st, ok, kerr := b.knativeStatus(ctx, name); kerr != nil {
				return api.DeployStatus{}, kerr
			} else if ok {
				return st, nil
			}
			return api.DeployStatus{Name: name, Backend: b.Name()}, nil
		}
		return api.DeployStatus{}, err
	}
	// Best-effort pod fetch enriches instances with per-container exit codes; a
	// list failure still yields a Deployment-derived status.
	var pods []corev1.Pod
	if pl, err := b.clientset.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelApp + "=" + name,
	}); err == nil {
		pods = pl.Items
	}
	return statusOf(dep, pods, b.Name()), nil
}

// List reports all cornus-managed Deployments.
func (b *Backend) List(ctx context.Context) ([]api.DeployStatus, error) {
	list, err := b.clientset.AppsV1().Deployments(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelManaged + "=true",
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.DeployStatus, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, statusOf(&list.Items[i], nil, b.Name()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete removes the Deployment; Kubernetes garbage collection then cascades to
// the Service and managed-volume PVCs, which carry the Deployment as their owner
// (see applyDeployment). Foreground propagation deletes those dependents before
// the Deployment object is finally removed.
//
// Managed (anonymous) volumes are ephemeral by design — they mirror
// `docker rm -v`: their PVCs (and backing storage) go with the deployment rather
// than being retained. Anyone needing durable storage should use a named/external
// volume, which cornus does not manage. Tying cleanup to ownership (not a manual
// multi-object sequence) means an out-of-band or interrupted delete cannot orphan
// the Service or PVCs.
func (b *Backend) Delete(ctx context.Context, name string) error {
	policy := metav1.DeletePropagationForeground
	opts := metav1.DeleteOptions{PropagationPolicy: &policy}
	if err := b.clientset.AppsV1().Deployments(b.namespace).Delete(ctx, name, opts); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	// A one-shot workload is a Job (see applyWorkload); delete that too. Foreground
	// propagation reaps its pods, and GC cascades to the owned Service/PVCs/Ingress.
	if err := b.clientset.BatchV1().Jobs(b.namespace).Delete(ctx, name, opts); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	// A Knative workload is a serving.knative.dev Service; delete that too.
	// Knative's own GC then cascades its Configuration/Revisions/Route.
	if err := b.deleteKnative(ctx, name, opts); err != nil {
		return err
	}
	// Reap shared network objects whose last member is gone. Best-effort: a
	// deployment still terminating counts as a member this round; the next
	// apply/delete sweeps it.
	b.net.GC(ctx)
	return nil
}

// RemoveVolume deletes the PVC backing a named, project-scoped volume
// (deploy.VolumeRemover, for `compose down --volumes`). The name passed is the
// logical VolumeSpec.Name (e.g. "myproj_cache"); namedPVCName maps it to the
// same PVC that persistentVolumeClaim created and labeled cornus.volume.
// Delete-if-exists: a missing PVC is a no-op success.
func (b *Backend) RemoveVolume(ctx context.Context, name string) error {
	pvc := namedPVCName(name)
	if err := b.clientset.CoreV1().PersistentVolumeClaims(b.namespace).Delete(ctx, pvc, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// updateDeployment applies mutate to a fresh copy of the Deployment and
// updates it, retrying on optimistic-concurrency conflicts. The deployment
// controller writes to the object concurrently (status, revision annotations),
// so a bare Get→Update races it and surfaces a 409 to the caller. A missing
// Deployment becomes an error wrapping deploy.ErrNotFound (the
// Stop/Start/Restart contract) instead of a raw apierrors NotFound.
func (b *Backend) updateDeployment(ctx context.Context, name string, mutate func(*appsv1.Deployment)) error {
	deps := b.clientset.AppsV1().Deployments(b.namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		dep, err := deps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("kubernetes: deployment %q: %w", name, deploy.ErrNotFound)
			}
			return err
		}
		mutate(dep)
		_, err = deps.Update(ctx, dep, metav1.UpdateOptions{})
		return err
	})
}

// knativeLifecycle reports whether name is a Knative workload, so Stop/Start/
// Restart can dispatch it differently from a Deployment. It only probes when the
// cluster serves Knative, so a Deployment-only cluster pays nothing.
func (b *Backend) knativeLifecycle(ctx context.Context, name string) (bool, error) {
	if !b.knativeServed() {
		return false, nil
	}
	return b.knativeExists(ctx, name)
}

// Stop scales the Deployment to zero, remembering the desired replica count.
func (b *Backend) Stop(ctx context.Context, name string) error {
	if ok, err := b.knativeLifecycle(ctx, name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("kubernetes: stop is not supported for a knative workload (scale-to-zero is automatic — set knative.minScale to bound it)")
	}
	return b.updateDeployment(ctx, name, func(dep *appsv1.Deployment) {
		if dep.Annotations == nil {
			dep.Annotations = map[string]string{}
		}
		cur := int32(1)
		if dep.Spec.Replicas != nil {
			cur = *dep.Spec.Replicas
		}
		if cur > 0 {
			dep.Annotations[replicasAnnotation] = strconv.Itoa(int(cur))
		}
		dep.Spec.Replicas = ptr.To[int32](0)
	})
}

// Start restores a stopped Deployment to its remembered replica count.
func (b *Backend) Start(ctx context.Context, name string) error {
	if ok, err := b.knativeLifecycle(ctx, name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("kubernetes: start is not supported for a knative workload (Knative scales up on demand — a request wakes a scaled-to-zero service)")
	}
	return b.updateDeployment(ctx, name, func(dep *appsv1.Deployment) {
		replicas := int32(1)
		if v := dep.Annotations[replicasAnnotation]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				replicas = int32(n)
			}
		}
		dep.Spec.Replicas = ptr.To(replicas)
	})
}

// Restart triggers a rolling restart by bumping a pod-template annotation. For a
// Knative workload it stamps the revision template instead, cutting a new
// Revision (the serverless analogue of a Deployment rollout).
func (b *Backend) Restart(ctx context.Context, name string) error {
	if ok, err := b.knativeLifecycle(ctx, name); err != nil {
		return err
	} else if ok {
		return b.restartKnative(ctx, name)
	}
	return b.updateDeployment(ctx, name, func(dep *appsv1.Deployment) {
		if dep.Spec.Template.Annotations == nil {
			dep.Spec.Template.Annotations = map[string]string{}
		}
		dep.Spec.Template.Annotations[restartAnnotation] = metav1.Now().Format("20060102150405")
	})
}

// Logs streams a deployment's pod logs to w. Kubernetes cannot separate stdout
// from stderr, so the Stdout/Stderr split in opts is ignored (both are merged
// into the single container log). The kube log stream is NOT stdcopy-framed, so
// to satisfy the deploy.Backend.Logs framing contract the raw stream is wrapped
// in stdcopy stdout framing. The deployment's first running pod is streamed (a
// documented limitation; multi-pod log fan-in is not implemented). ctx
// cancellation stops a follow.
func (b *Backend) Logs(ctx context.Context, name string, opts api.LogOptions, w io.Writer) error {
	// deploy.ParseSince is the shared cross-backend since grammar (Unix
	// seconds[.nanos], RFC3339, or a duration relative to now). The absolute
	// time it yields maps onto the kube API's SinceTime; garbage is an error
	// here — it must never silently degrade to "all logs".
	since, err := deploy.ParseSince(opts.Since, time.Now())
	if err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	// The kube pods/log API has no "until" bound; validate the value for a
	// consistent cross-backend error, then warn that it is not honored (logs
	// still stream to now) rather than silently dropping it.
	if opts.Until != "" {
		if _, err := deploy.ParseSince(opts.Until, time.Now()); err != nil {
			return fmt.Errorf("kubernetes: %w", err)
		}
		logging.FromContext(ctx, slog.Group("kubernetes", "deployment", name)).WarnContext(ctx,
			"logs --until is not supported (pods/log has no upper time bound); ignoring")
	}
	pod, err := b.firstPod(ctx, name)
	if err != nil {
		return err
	}
	podOpts := &corev1.PodLogOptions{
		Follow:     opts.Follow,
		Timestamps: opts.Timestamps,
	}
	if opts.Tail != "" && opts.Tail != "all" {
		if n, err := strconv.ParseInt(opts.Tail, 10, 64); err == nil {
			podOpts.TailLines = ptr.To(n)
		}
	}
	if !since.IsZero() {
		t := metav1.NewTime(since)
		podOpts.SinceTime = &t
	}
	stream, err := b.clientset.CoreV1().Pods(b.namespace).GetLogs(pod, podOpts).Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	_, err = io.Copy(stdcopy.NewStdWriter(w, stdcopy.Stdout), stream)
	return err
}

// Stats is not supported on the kubernetes backend: container metrics via the
// Kubernetes API require metrics-server and are out of scope. Use `kubectl top`.
func (b *Backend) Stats(_ context.Context, _ string, _ api.StatsOptions, _ io.Writer) error {
	return fmt.Errorf("stats not supported on the kubernetes backend (needs metrics-server); use kubectl top")
}

// StatPath is not supported on the kubernetes backend: cp/archive is tar-over-
// exec and out of scope. Use `kubectl cp`.
func (b *Backend) StatPath(_ context.Context, _, _ string) (api.PathStat, error) {
	return api.PathStat{}, fmt.Errorf("cp/archive not supported on the kubernetes backend; use kubectl cp")
}

// CopyFrom is not supported on the kubernetes backend (see StatPath).
func (b *Backend) CopyFrom(_ context.Context, _, _ string, _ io.Writer) (api.PathStat, error) {
	return api.PathStat{}, fmt.Errorf("cp/archive not supported on the kubernetes backend; use kubectl cp")
}

// CopyTo is not supported on the kubernetes backend (see StatPath).
func (b *Backend) CopyTo(_ context.Context, _, _ string, _ io.Reader, _ api.CopyToOptions) error {
	return fmt.Errorf("cp/archive not supported on the kubernetes backend; use kubectl cp")
}

// execContainer is the app container name every cornus deployment runs (see
// deployment()); exec/attach target it.
const execContainer = "app"

// ExecCreate resolves the target pod (the deployment's first running pod) and
// records an exec session. Kubernetes exec is a single streaming call with no
// separate "create" step, so no API call is made here — ExecStart opens the
// stream against the recorded pod. The returned id keys the session registry.
//
// Kubernetes PodExecOptions cannot express Docker's exec Env, WorkingDir, User,
// or Privileged. Env is honored by wrapping the command in env(1) (see
// execCommand); WorkingDir, User, and Privileged are NOT honored:
// warnUnsupportedExec logs a loud per-exec warning for each one that is set and
// the exec proceeds without it. (A shell wrapper to fake WorkingDir is
// deliberately avoided — the container may not ship a shell; env(1) is far more
// widely present and is not a shell.)
func (b *Backend) ExecCreate(ctx context.Context, name string, cfg api.ExecConfig) (string, error) {
	pod, err := b.firstPod(ctx, name)
	if err != nil {
		return "", err
	}
	warnUnsupportedExec(ctx, name, cfg)
	id, err := randomExecID()
	if err != nil {
		return "", err
	}
	sess := &execSession{
		pod:       pod,
		container: execContainer,
		cfg:       cfg,
		sizeCh:    make(chan remotecommand.TerminalSize, 1),
	}
	b.mu.Lock()
	// Reap finished sessions past their grace period so the registry stays
	// bounded on a long-lived backend (Kubernetes exec has no server-side
	// session to remove, so this create path is the only reliable cleanup hook).
	b.reapExpiredSessionsLocked(time.Now())
	b.sessions[id] = sess
	b.mu.Unlock()
	return id, nil
}

// execCommand returns the argv handed to pods/exec for cfg. Kubernetes exec has
// no per-exec environment, so when cfg.Env is set the command is wrapped in
// `env KEY=VALUE ... cmd...`: env(1) applies the variables and then exec()s the
// real command directly (no shell parsing). env(1) is present in essentially
// every image that ships a shell, so this restores Docker's exec-Env fidelity
// without the shell-quoting hazard of an `sh -c` wrapper. With no Env requested
// the command is passed through unchanged.
func execCommand(cfg api.ExecConfig) []string {
	if len(cfg.Env) == 0 {
		return cfg.Cmd
	}
	cmd := make([]string, 0, 1+len(cfg.Env)+len(cfg.Cmd))
	cmd = append(cmd, "env")
	cmd = append(cmd, cfg.Env...)
	cmd = append(cmd, cfg.Cmd...)
	return cmd
}

// warnUnsupportedExec logs a warning for each Docker exec-config field that the
// Kubernetes pods/exec subresource cannot express and this backend therefore
// cannot honor: WorkingDir, User, and Privileged. (Env is honored separately by
// execCommand, so it is not warned about here.) The exec still runs (the
// dockerproxy devcontainer flow depends on exec never hard-failing on these),
// just without the requested field.
func warnUnsupportedExec(ctx context.Context, deployment string, cfg api.ExecConfig) {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", deployment))
	warn := func(field string) {
		log.WarnContext(ctx, "backend cannot honor exec option (the pods/exec subresource does not support it); running the exec without it",
			"option", field)
	}
	if cfg.WorkingDir != "" {
		warn("WorkingDir")
	}
	if cfg.User != "" {
		warn("User")
	}
	if cfg.Privileged {
		warn("Privileged")
	}
}

// muxWriters returns the stdout/stderr writers for a non-TTY stream bridged
// onto the single conn: each side is stdcopy-framed (Docker's 8-byte stream
// headers) so the caller can demultiplex them — the deploy.Backend framing
// contract for non-TTY exec/attach output, and the same wrapping Logs applies.
func muxWriters(conn io.Writer) (stdout, stderr io.Writer) {
	return stdcopy.NewStdWriter(conn, stdcopy.Stdout), stdcopy.NewStdWriter(conn, stdcopy.Stderr)
}

// ExecStart opens the pods/exec SPDY stream for a previously-created exec and
// bridges it onto conn. conn is a single combined stream: it is the stdin the
// process reads (when AttachStdin) AND the sink for the process output. For a
// TTY exec (startCfg.Tty) stdout is written raw to conn and stderr is disabled
// (a PTY merges them at the source). For a non-TTY exec stdout and stderr are
// BOTH written to conn stdcopy-multiplexed (muxWriters), satisfying the
// deploy.Backend contract so the docker CLI can demux them.
//
// The exit code is captured from the stream's terminal error: a clean finish is
// exit 0, a client-go exec.CodeExitError carries the process exit code (both are
// returned as a nil error and surfaced via ExecInspect), and any other error is
// a transport failure returned to the caller.
func (b *Backend) ExecStart(ctx context.Context, execID string, startCfg api.ExecStartConfig, conn io.ReadWriteCloser) error {
	b.mu.Lock()
	sess := b.sessions[execID]
	b.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("kubernetes: no such exec %q", execID)
	}
	if b.restConfig == nil {
		return fmt.Errorf("kubernetes: exec requires a real cluster connection (rest.Config); not available on this backend")
	}

	req := b.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(sess.pod).
		Namespace(b.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: sess.container,
			Command:   execCommand(sess.cfg),
			Stdin:     sess.cfg.AttachStdin,
			Stdout:    true,
			Stderr:    !startCfg.Tty,
			TTY:       startCfg.Tty,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(b.restConfig, "POST", req.URL())
	if err != nil {
		return err
	}

	var stdin io.Reader
	if sess.cfg.AttachStdin {
		stdin = conn
	}
	stdout := io.Writer(conn)
	var stderr io.Writer
	var sizeQ remotecommand.TerminalSizeQueue
	if startCfg.Tty {
		// TTY: raw stream, a size queue drives window resizes; stderr is folded
		// into stdout at the source (the PTY).
		done := make(chan struct{})
		defer close(done)
		sizeQ = &terminalSizeQueue{ch: sess.sizeCh, done: done}
	} else {
		stdout, stderr = muxWriters(conn)
	}

	b.startSession(sess)
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             stdin,
		Stdout:            stdout,
		Stderr:            stderr,
		Tty:               startCfg.Tty,
		TerminalSizeQueue: sizeQ,
	})

	var codeErr utilexec.CodeExitError
	switch {
	case streamErr == nil:
		b.finishSession(sess, 0)
		return nil
	case errors.As(streamErr, &codeErr):
		b.finishSession(sess, codeErr.Code)
		return nil
	default:
		// Transport error: mark the session finished (exit code stays 0) and
		// surface the error to the caller.
		b.finishSession(sess, sess.exitCode)
		return streamErr
	}
}

// startSession marks an exec's stream as opened, so ExecInspect reports it
// Running (Docker semantics: an exec is not running between create and start).
func (b *Backend) startSession(sess *execSession) {
	b.mu.Lock()
	sess.started = true
	b.mu.Unlock()
}

// finishSession records an exec's terminal exit code and marks it not-running.
func (b *Backend) finishSession(sess *execSession, code int) {
	b.mu.Lock()
	sess.exitCode = code
	sess.done = true
	sess.finishedAt = time.Now()
	b.mu.Unlock()
}

// reapExpiredSessionsLocked deletes finished exec sessions whose grace period
// has elapsed. It bounds the session registry (otherwise every ExecCreate
// leaks one permanent entry, since Kubernetes exec has no server-side session
// to remove) without a background goroutine: callers sweep opportunistically
// while already holding b.mu. b.mu must be held.
func (b *Backend) reapExpiredSessionsLocked(now time.Time) {
	for id, sess := range b.sessions {
		if sess.done && now.Sub(sess.finishedAt) >= execSessionTTL {
			delete(b.sessions, id)
		}
	}
}

// ExecInspect reports the registry state of an exec, following the Docker exec
// lifecycle: created-but-not-started is NOT running, a started exec is Running
// until its stream ends, and a finished exec reports the captured process exit
// code. Pid is always 0: the process runs remotely behind the pods/exec
// subresource, which never surfaces its PID, and fabricating one would only
// mislead callers.
func (b *Backend) ExecInspect(_ context.Context, execID string) (api.ExecState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	sess := b.sessions[execID]
	if sess == nil {
		return api.ExecState{}, fmt.Errorf("kubernetes: no such exec %q", execID)
	}
	return api.ExecState{Running: sess.started && !sess.done, ExitCode: sess.exitCode}, nil
}

// ExecResize forwards a terminal resize to a running exec's TTY. The send is
// non-blocking: the size channel is buffered (depth 1) so a resize arriving
// before the stream starts is retained, and a resize that arrives faster than
// the stream consumes them is dropped (only the latest matters). It never
// blocks or panics when no stream is reading yet.
func (b *Backend) ExecResize(_ context.Context, execID string, height, width uint) error {
	b.mu.Lock()
	sess := b.sessions[execID]
	b.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("kubernetes: no such exec %q", execID)
	}
	size := remotecommand.TerminalSize{Width: uint16(width), Height: uint16(height)}
	select {
	case sess.sizeCh <- size:
	default:
		// Buffer full: an unconsumed resize is already queued; drop this one.
	}
	return nil
}

// Attach bridges conn to the deployment's first pod via the pods/attach
// subresource (the main process's stdio, not a new exec). Stream mapping onto
// the single conn mirrors ExecStart: since cornus deployments never allocate a
// container TTY the attach is non-TTY, so stdout and stderr are written to conn
// stdcopy-multiplexed (muxWriters, the deploy.Backend framing contract), and
// conn is used as stdin when cfg.Stdin is set. Note that stdin only reaches the
// process if the container was started with stdin open (cornus deployments do
// not set that), so attach is primarily useful for live stdout/stderr; use exec
// for interactive input.
func (b *Backend) Attach(ctx context.Context, name string, cfg api.AttachConfig, conn io.ReadWriteCloser) error {
	if b.restConfig == nil {
		return fmt.Errorf("kubernetes: attach requires a real cluster connection (rest.Config); not available on this backend")
	}
	pod, err := b.firstPod(ctx, name)
	if err != nil {
		return err
	}

	req := b.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(b.namespace).
		SubResource("attach").
		VersionedParams(&corev1.PodAttachOptions{
			Container: execContainer,
			Stdin:     cfg.Stdin,
			Stdout:    cfg.Stdout,
			Stderr:    cfg.Stderr,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(b.restConfig, "POST", req.URL())
	if err != nil {
		return err
	}

	var stdin io.Reader
	if cfg.Stdin {
		stdin = conn
	}
	outW, errW := muxWriters(conn)
	var stdout, stderr io.Writer
	if cfg.Stdout {
		stdout = outW
	}
	if cfg.Stderr {
		stderr = errW
	}
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    false,
	})
	var codeErr utilexec.CodeExitError
	if streamErr == nil || errors.As(streamErr, &codeErr) {
		return nil
	}
	return streamErr
}

// ForwardPort bridges conn to a TCP port inside the deployment's first pod
// (kubectl port-forward). It rides the pods/portforward SPDY subresource through
// the API server — like exec/attach — so it works from an out-of-cluster
// kubeconfig with no sidecar and reaches ports the pod never exposed via a
// Service. Kubernetes port-forward is TCP-only; a non-tcp proto is rejected. This
// is a single-connection port-forward: the caller opens one tunnel per accepted
// local connection, matching the one-exec-per-invocation model.
func (b *Backend) ForwardPort(ctx context.Context, name string, port int, proto string, conn io.ReadWriteCloser) error {
	if proto != "" && proto != "tcp" {
		return fmt.Errorf("kubernetes: unsupported port-forward protocol %q (the pods/portforward subresource is tcp-only)", proto)
	}
	if b.restConfig == nil {
		return fmt.Errorf("kubernetes: port-forward requires a real cluster connection (rest.Config); not available on this backend")
	}
	pod, err := b.firstPod(ctx, name)
	if err != nil {
		return err
	}

	req := b.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(b.namespace).
		SubResource("portforward")

	rt, upgrader, err := spdy.RoundTripperFor(b.restConfig)
	if err != nil {
		return err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, "POST", req.URL())
	streamConn, protocol, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return fmt.Errorf("kubernetes: upgrading port-forward connection: %w", err)
	}
	defer streamConn.Close()
	if protocol != portforward.PortForwardProtocolV1Name {
		return fmt.Errorf("kubernetes: unable to negotiate port-forward protocol: server returned %q", protocol)
	}

	// The port-forward protocol requires an error stream and a data stream sharing
	// one request id, both carrying the target port. Create the error stream first
	// (we never write to it; the server writes forwarding errors back on it), then
	// the data stream that we splice the caller's conn onto.
	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(port))
	headers.Set(corev1.PortForwardRequestIDHeader, "0")
	errorStream, err := streamConn.CreateStream(headers)
	if err != nil {
		return fmt.Errorf("kubernetes: creating port-forward error stream: %w", err)
	}
	errorStream.Close() // half-close: we only read from it
	defer streamConn.RemoveStreams(errorStream)

	errorChan := make(chan error, 1)
	go func() {
		msg, err := io.ReadAll(errorStream)
		switch {
		case err != nil:
			errorChan <- fmt.Errorf("kubernetes: port-forward error stream for port %d: %w", port, err)
		case len(msg) > 0:
			errorChan <- fmt.Errorf("kubernetes: forwarding port %d: %s", port, string(msg))
		default:
			errorChan <- nil
		}
	}()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := streamConn.CreateStream(headers)
	if err != nil {
		return fmt.Errorf("kubernetes: creating port-forward data stream: %w", err)
	}
	defer streamConn.RemoveStreams(dataStream)

	remoteDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, dataStream) // remote -> local
		close(remoteDone)
	}()
	go func() {
		defer dataStream.Close()         // signal EOF to the server when local closes
		_, _ = io.Copy(dataStream, conn) // local -> remote
	}()

	select {
	case <-remoteDone:
	case <-ctx.Done():
	}
	_ = dataStream.Reset() // discard unsent data so errorChan does not block
	conn.Close()
	return <-errorChan
}

// terminalSizeQueue adapts an execSession's size channel to
// remotecommand.TerminalSizeQueue. Next blocks until a resize arrives (from
// ExecResize) or the stream finishes and done is closed, in which case it
// returns nil so remotecommand stops polling and the goroutine exits.
type terminalSizeQueue struct {
	ch   <-chan remotecommand.TerminalSize
	done <-chan struct{}
}

func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case s := <-q.ch:
		return &s
	case <-q.done:
		return nil
	}
}

// randomExecID returns a 128-bit hex token used to key an exec session.
func randomExecID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// firstPod returns the name of the deployment's first pod, preferring a running
// one, selected by the cornus app label.
func (b *Backend) firstPod(ctx context.Context, name string) (string, error) {
	pods, err := b.clientset.CoreV1().Pods(b.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: deploy.LabelApp + "=" + name,
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("kubernetes: no pods for deployment %q: %w", name, deploy.ErrNotFound)
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return pods.Items[i].Name, nil
		}
	}
	return pods.Items[0].Name, nil
}

// deployment builds the Deployment object for a spec.
func (b *Backend) deployment(ctx context.Context, spec api.DeploySpec) *appsv1.Deployment {
	replicas := int32(deploy.Replicas(spec))
	sel := labels(spec.Name)

	var env []corev1.EnvVar
	for k, v := range spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Name < env[j].Name })

	var ports []corev1.ContainerPort
	for _, p := range spec.Ports {
		// PortMapping.HostIP (compose host_ip) is intentionally not applied here:
		// a ClusterIP Service / ContainerPort has no host-interface binding.
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: int32(p.Container),
			Protocol:      protocol(p.Protocol),
		})
	}

	// Bind mounts are never realized as hostPath (node-local, unsafe). Any
	// client-local mounts are added as live 9P sidecars by deploymentWithMounts;
	// the stateless Apply rejects bind mounts outright.
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	var initContainers []corev1.Container

	// Managed volumes (anonymous compose volumes) are backed by one
	// dynamically-provisioned PVC each; applyDeployment ensures the PVCs exist.
	// A freshly provisioned PVC mounts EMPTY, unlike a Docker anonymous volume,
	// which the daemon seeds with whatever the image ships at the mount path. To
	// match that, each volume gets a populate initContainer (below) that copies
	// the image's content at the target into the PVC on first start only.
	for i, v := range spec.Volumes {
		volName := fmt.Sprintf("vol-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName(spec, i),
					ReadOnly:  v.ReadOnly,
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: v.Target,
			ReadOnly:  v.ReadOnly,
		})
		initContainers = append(initContainers, volumePopulateContainer(spec.Image, volName, v.Target, i))
	}

	// compose tmpfs / shm_size -> memory-backed emptyDir volumes + mounts. Each
	// tmpfs path is an emptyDir{Medium: Memory}; shm_size is one such volume
	// (with a SizeLimit) mounted at /dev/shm. Compose per-mount size options in a
	// tmpfs entry (e.g. "/run:size=64m") cannot be expressed on a plain emptyDir
	// and are dropped (a warning is emitted). Devices and ulimits have no native
	// kubernetes equivalent and are warned about too.
	tv, tm := tmpfsVolumes(ctx, spec)
	volumes = append(volumes, tv...)
	mounts = append(mounts, tm...)

	container := corev1.Container{
		Name:            "app",
		Image:           spec.Image,
		Args:            spec.Command,
		Env:             env,
		Ports:           ports,
		VolumeMounts:    mounts,
		ImagePullPolicy: imagePullPolicy(),
		// compose working_dir / tty / stdin_open map directly onto the container.
		WorkingDir: spec.WorkingDir,
		TTY:        spec.TTY,
		Stdin:      spec.StdinOpen,
	}
	// Docker create-time semantics, which dockerhost/containerd implement:
	// spec.Command supplies the ARGUMENTS to the image's ENTRYPOINT, and only an
	// explicit spec.Entrypoint replaces the ENTRYPOINT itself. So spec.Command
	// always rides in Args (Kubernetes Command overrides the image ENTRYPOINT,
	// which a command-only spec must NOT do), and Command is set only from
	// spec.Entrypoint.
	if len(spec.Entrypoint) > 0 {
		container.Command = spec.Entrypoint
	}
	// Build the app-container securityContext from the toggles that map onto it:
	// privileged, read_only (readOnlyRootFilesystem), and a NUMERIC compose
	// `user` (runAsUser/runAsGroup). A username-form `user` cannot be expressed
	// here (Kubernetes takes only numeric ids) and is dropped with a warning.
	if sc := containerSecurityContext(ctx, spec); sc != nil {
		container.SecurityContext = sc
	}
	if probe := healthProbe(spec.Healthcheck); probe != nil {
		container.LivenessProbe = probe
		container.ReadinessProbe = probe.DeepCopy()
	}
	// compose deploy.resources.limits -> resources.limits and
	// deploy.resources.reservations -> resources.requests.
	if limits, requests := resourceLimits(spec.Resources), resourceRequests(spec.Resources); limits != nil || requests != nil {
		container.Resources = corev1.ResourceRequirements{Limits: limits, Requests: requests}
	}

	podSpec := corev1.PodSpec{
		InitContainers: initContainers,
		Containers:     []corev1.Container{container},
		Volumes:        volumes,
		// compose hostname -> pod Hostname.
		Hostname: spec.Hostname,
	}
	// compose stop_grace_period -> pod TerminationGracePeriodSeconds (whole
	// seconds). No-ops on kubernetes: compose `stop_signal` (no per-container
	// stop signal) and `init` (no tini equivalent).
	if secs, ok := deploy.StopGracePeriodSeconds(spec); ok {
		podSpec.TerminationGracePeriodSeconds = ptr.To(int64(secs))
	}
	// compose group_add / sysctls -> pod-level securityContext.
	if psc := podSecurityContext(ctx, spec); psc != nil {
		podSpec.SecurityContext = psc
	}
	// compose pid / ipc: only the "host" form maps to a pod field (HostPID /
	// HostIPC). Other forms (service:/container:/shareable/none) name a
	// runtime-shared namespace a Deployment pod cannot express and are warned
	// about. ulimits and devices have no native kubernetes equivalent.
	applyHostNamespaces(ctx, spec, &podSpec)
	warnUnsupportedResourceKeys(ctx, spec)
	// compose extra_hosts -> pod HostAliases (the cooperative proxy appends more
	// aliases later on the dep copy, so both coexist).
	podSpec.HostAliases = hostAliases(spec)
	// compose dns / dns_search / dns_opt -> pod DNSConfig. Applied here on the
	// base pod spec; a caretaker DNS role injected below overrides it (see
	// applyComposeDNS).
	applyComposeDNS(spec, &podSpec)
	// compose labels -> pod-template ANNOTATIONS. Compose label values do not
	// satisfy Kubernetes label syntax, so they ride as annotations, not labels.
	var podAnnotations map[string]string
	if len(spec.Labels) > 0 {
		podAnnotations = make(map[string]string, len(spec.Labels))
		for k, v := range spec.Labels {
			podAnnotations[k] = v
		}
	}
	// Origin lineage rides as Deployment annotations (its values — paths, URLs,
	// subjects — do not satisfy Kubernetes label syntax, same reason compose
	// labels ride as annotations). List/Status read them back off the Deployment.
	var depAnnotations map[string]string
	depAnnotations = mergeAnnotations(depAnnotations, deploy.OriginToLabels(spec.Origin))
	if spec.AgentForward {
		depAnnotations = mergeAnnotations(depAnnotations, map[string]string{agentForwardAnnotation: "true"})
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Labels: sel, Annotations: depAnnotations},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sel, Annotations: podAnnotations},
				Spec:       podSpec,
			},
		},
	}
	// compose deploy.update_config -> the Deployment rolling-update strategy.
	if strat := updateStrategy(spec.UpdateConfig); strat != nil {
		dep.Spec.Strategy = *strat
	}
	// A pinned user-network address (Multus static IPAM) is exclusive on its
	// segment: a rolling update would briefly run the old and new pod with the
	// SAME static IP on the same L2, so recreate instead. This wins over any
	// update_config strategy above.
	if hasPinnedNetIP(spec) {
		dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	if spec.Proxy != nil {
		b.injectProxy(spec, &dep.Spec.Template.Spec)
	}
	// Hub subsumes DNS injection (it folds the synthetic-IP records into its own DNS
	// role), so only inject the standalone DNS caretaker when there is no hub.
	if spec.Hub != nil {
		b.injectHub(ctx, spec, &dep.Spec.Template.Spec)
	} else if b.dnsActive(ctx, spec) {
		b.injectDNS(spec, &dep.Spec.Template.Spec)
	} else if spec.Docker != nil {
		// A Docker endpoint with no hub/dns role gets its own caretaker; when a hub
		// or dns role is present, the docker role folds into that one caretaker above.
		b.injectDocker(ctx, spec, &dep.Spec.Template.Spec)
	} else if spec.AgentForward {
		// AgentForward with no hub/dns/docker role gets its own minimal caretaker;
		// when any of those is present, the AgentRelay role folds into that one
		// caretaker above (see addAgentForwardRole).
		b.injectAgentForward(ctx, spec, &dep.Spec.Template.Spec)
	} else if spec.Telemetry.Active() && spec.Proxy == nil {
		// Telemetry with no hub/dns/docker/agentforward role gets its own minimal
		// caretaker; when any of those is present the OTel role folds into that one
		// caretaker above (see addTelemetryRole). When a proxy is present its
		// caretaker carries the OTel role instead (folded in injectProxy*), so skip
		// the standalone one to avoid a second "cornus-caretaker" container.
		b.injectTelemetry(ctx, spec, &dep.Spec.Template.Spec)
	}
	return dep
}

// containerSecurityContext builds the app container's securityContext from the
// compose toggles that map onto it: privileged, read_only
// (readOnlyRootFilesystem), and a NUMERIC compose `user` (runAsUser, and
// runAsGroup when a gid is given). It returns nil when none apply, so a plain
// spec keeps no securityContext (matching the pre-existing default). A
// username-form `user` (e.g. "nginx") cannot be expressed — Kubernetes takes
// only numeric ids — and is dropped with a warning.
func containerSecurityContext(ctx context.Context, spec api.DeploySpec) *corev1.SecurityContext {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))
	var sc *corev1.SecurityContext
	ensure := func() *corev1.SecurityContext {
		if sc == nil {
			sc = &corev1.SecurityContext{}
		}
		return sc
	}
	if spec.Privileged {
		ensure().Privileged = ptr.To(true)
	}
	if spec.ReadOnly {
		ensure().ReadOnlyRootFilesystem = ptr.To(true)
	}
	if spec.User != "" {
		if uid, gid, ok := parseNumericUser(spec.User); ok {
			ensure().RunAsUser = ptr.To(uid)
			if gid != nil {
				ensure().RunAsGroup = ptr.To(*gid)
			}
		} else {
			log.WarnContext(ctx, "compose user is a username, which securityContext cannot express (only numeric uid[:gid]); ignoring", "user", spec.User)
		}
	}
	// compose cap_add / cap_drop -> securityContext.capabilities.add/drop.
	if len(spec.CapAdd) > 0 || len(spec.CapDrop) > 0 {
		caps := &corev1.Capabilities{}
		for _, c := range spec.CapAdd {
			caps.Add = append(caps.Add, corev1.Capability(c))
		}
		for _, c := range spec.CapDrop {
			caps.Drop = append(caps.Drop, corev1.Capability(c))
		}
		ensure().Capabilities = caps
	}
	// compose security_opt -> best-effort securityContext mapping.
	applySecurityOpt(ctx, spec, ensure)
	return sc
}

// applySecurityOpt maps the compose `security_opt` entries onto the app
// container's securityContext, best-effort. Only the well-known options have a
// clean Kubernetes equivalent:
//   - `no-new-privileges[:true]` -> AllowPrivilegeEscalation=false (and the
//     explicit `no-new-privileges:false` -> true);
//   - `label=<k>:<v>` (SELinux user/role/type/level) -> SELinuxOptions; the
//     valueless `label=disable` has no SELinuxOptions field and is not mapped.
//
// `seccomp=...` and `apparmor=...` need profile objects / annotations (complex)
// and are not mapped; an unrecognised option is likewise skipped. None of these
// fail the deploy — dockerhost passes every option through verbatim, and here
// the unmapped ones are warned about.
func applySecurityOpt(ctx context.Context, spec api.DeploySpec, ensure func() *corev1.SecurityContext) {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))
	for _, opt := range spec.SecurityOpt {
		switch {
		case opt == "no-new-privileges", opt == "no-new-privileges:true":
			ensure().AllowPrivilegeEscalation = ptr.To(false)
		case opt == "no-new-privileges:false":
			ensure().AllowPrivilegeEscalation = ptr.To(true)
		case strings.HasPrefix(opt, "label="):
			if !applySELinuxLabel(strings.TrimPrefix(opt, "label="), ensure) {
				log.WarnContext(ctx, "security_opt label option is not mapped to SELinuxOptions", "security_opt", opt)
			}
		default:
			log.WarnContext(ctx, "security_opt is not mapped to securityContext; only no-new-privileges and label= are supported (seccomp/apparmor need profile objects)", "security_opt", opt)
		}
	}
}

// applySELinuxLabel maps a single compose `label=<k>:<v>` SELinux option onto
// the securityContext's SELinuxOptions. It reports whether the option mapped to
// a field (a valueless form such as `disable` does not).
func applySELinuxLabel(v string, ensure func() *corev1.SecurityContext) bool {
	key, val, ok := strings.Cut(v, ":")
	if !ok {
		return false
	}
	sc := ensure()
	if sc.SELinuxOptions == nil {
		sc.SELinuxOptions = &corev1.SELinuxOptions{}
	}
	switch key {
	case "user":
		sc.SELinuxOptions.User = val
	case "role":
		sc.SELinuxOptions.Role = val
	case "type":
		sc.SELinuxOptions.Type = val
	case "level":
		sc.SELinuxOptions.Level = val
	default:
		return false
	}
	return true
}

// podSecurityContext builds the pod-level securityContext from the compose keys
// that live there: `group_add` (supplementalGroups — NUMERIC GIDs only; a group
// name cannot be expressed and is skipped with a warning) and `sysctls` (the pod
// securityContext sysctls list, emitted in a deterministic key order). It returns
// nil when neither applies, so a plain spec keeps no pod securityContext.
func podSecurityContext(ctx context.Context, spec api.DeploySpec) *corev1.PodSecurityContext {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))
	var psc *corev1.PodSecurityContext
	ensure := func() *corev1.PodSecurityContext {
		if psc == nil {
			psc = &corev1.PodSecurityContext{}
		}
		return psc
	}
	for _, g := range spec.GroupAdd {
		gid, err := strconv.ParseInt(g, 10, 64)
		if err != nil {
			log.WarnContext(ctx, "group_add entry is a group name, which supplementalGroups cannot express (only numeric GIDs); ignoring", "group", g)
			continue
		}
		ensure().SupplementalGroups = append(ensure().SupplementalGroups, gid)
	}
	if len(spec.Sysctls) > 0 {
		keys := make([]string, 0, len(spec.Sysctls))
		for k := range spec.Sysctls {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ensure().Sysctls = append(ensure().Sysctls, corev1.Sysctl{Name: k, Value: spec.Sysctls[k]})
		}
	}
	return psc
}

// hostAliases translates compose `extra_hosts` ("host:ip" entries) into pod
// HostAliases, grouping every hostname that shares an IP into a single alias
// (Kubernetes keys HostAliases by IP). IP order follows first appearance for
// determinism. An entry is split on its FIRST colon so an IPv6 address (whose
// own colons follow) is preserved as the IP.
func hostAliases(spec api.DeploySpec) []corev1.HostAlias {
	if len(spec.ExtraHosts) == 0 {
		return nil
	}
	order := make([]string, 0, len(spec.ExtraHosts))
	byIP := map[string][]string{}
	for _, h := range spec.ExtraHosts {
		host, ip, ok := strings.Cut(h, ":")
		if !ok || host == "" || ip == "" {
			continue
		}
		if _, seen := byIP[ip]; !seen {
			order = append(order, ip)
		}
		byIP[ip] = append(byIP[ip], host)
	}
	var out []corev1.HostAlias
	for _, ip := range order {
		out = append(out, corev1.HostAlias{IP: ip, Hostnames: byIP[ip]})
	}
	return out
}

// applyComposeDNS points the pod resolver at the compose `dns` / `dns_search` /
// `dns_opt` values. It sets DNSPolicy=None + DNSConfig only when at least one of
// them is present, so the cluster default DNS is untouched otherwise. Each
// `dns_opt` entry is split on ':' into a {Name,Value} option.
//
// Precedence: this runs while the base pod spec is built, BEFORE the caretaker
// DNS role injection (injectDNS/injectHub -> setPodDNS). When a spec carries BOTH
// a compose `dns:` and the caretaker DNS *DNSSpec, the caretaker's setPodDNS runs
// later and overwrites DNSConfig — the caretaker resolver wins, which is correct
// (it forwards unknown names upstream, so peer resolution is not lost).
func applyComposeDNS(spec api.DeploySpec, podSpec *corev1.PodSpec) {
	if len(spec.DNSServers) == 0 && len(spec.DNSSearch) == 0 && len(spec.DNSOptions) == 0 {
		return
	}
	podSpec.DNSPolicy = corev1.DNSNone
	cfg := &corev1.PodDNSConfig{
		Nameservers: spec.DNSServers,
		Searches:    spec.DNSSearch,
	}
	for _, opt := range spec.DNSOptions {
		name, value, hasValue := strings.Cut(opt, ":")
		o := corev1.PodDNSConfigOption{Name: name}
		if hasValue {
			o.Value = ptr.To(value)
		}
		cfg.Options = append(cfg.Options, o)
	}
	podSpec.DNSConfig = cfg
}

// parseNumericUser parses a compose `user` value into a numeric uid and optional
// gid for a Kubernetes securityContext. It accepts "uid" and "uid:gid" where
// both components are integers; any username/group-name form yields ok=false so
// the caller can fall back (Kubernetes runAsUser/runAsGroup are numeric only).
func parseNumericUser(user string) (uid int64, gid *int64, ok bool) {
	uidStr, gidStr, hasGid := strings.Cut(user, ":")
	u, err := strconv.ParseInt(uidStr, 10, 64)
	if err != nil {
		return 0, nil, false
	}
	if hasGid {
		g, err := strconv.ParseInt(gidStr, 10, 64)
		if err != nil {
			return 0, nil, false
		}
		gid = &g
	}
	return u, gid, true
}

// hasPinnedNetIP reports whether any overlaid network attachment pins a static
// user-network address (compose plan-time allocation).
func hasPinnedNetIP(spec api.DeploySpec) bool {
	for _, n := range spec.Networks {
		if n.IP != "" && !n.Default {
			return true
		}
	}
	return false
}

// dnsActive reports whether the spec's caretaker DNS role should be injected.
// An explicit DNSSpec always is; records marked RequireUserNet (the compose
// planner's Multus secondary-IP records) are dropped — with a warning — when no
// attachment actually resolves to a Multus pipeline, so a cluster without the
// fabric degrades to the services-DNS baseline instead of resolving peers to
// addresses that will never exist.
func (b *Backend) dnsActive(ctx context.Context, spec api.DeploySpec) bool {
	switch {
	case spec.DNS == nil:
		return false
	case !spec.DNS.RequireUserNet, b.net.MultusActive(spec):
		return true
	}
	logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name)).WarnContext(ctx,
		"dropping user-network DNS records: no attachment resolved to a Multus fabric, so the secondary addresses they point at will not exist; peers resolve via the cluster DNS instead")
	return false
}

// healthProbe translates an api.Healthcheck into a kubernetes exec probe, or nil
// when there is nothing to probe (no test, or an explicit "NONE" disable). The
// Docker CMD form is mapped to an ExecAction: "CMD" execs the remaining
// arguments, "CMD-SHELL" runs the single string through /bin/sh, and a bare
// list (no CMD marker) is execed verbatim. Docker's durations become the probe's
// second-granularity timings (rounded up), and Retries maps to FailureThreshold.
func healthProbe(hc *api.Healthcheck) *corev1.Probe {
	if hc == nil || len(hc.Test) == 0 || hc.Disabled() {
		return nil
	}
	var cmd []string
	switch hc.Test[0] {
	case "CMD":
		cmd = hc.Test[1:]
	case "CMD-SHELL":
		cmd = []string{"/bin/sh", "-c", strings.Join(hc.Test[1:], " ")}
	default:
		cmd = hc.Test
	}
	if len(cmd) == 0 {
		return nil
	}
	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: cmd}},
	}
	if s := durationSeconds(hc.Interval); s > 0 {
		probe.PeriodSeconds = s
	}
	if s := durationSeconds(hc.Timeout); s > 0 {
		probe.TimeoutSeconds = s
	}
	if s := durationSeconds(hc.StartPeriod); s > 0 {
		probe.InitialDelaySeconds = s
	}
	// Healthcheck.StartInterval (compose start_interval) has no kubernetes
	// equivalent — a Probe has a single PeriodSeconds, not a distinct
	// start-period cadence — so it is intentionally not applied here.
	if hc.Retries > 0 {
		probe.FailureThreshold = int32(hc.Retries)
	}
	return probe
}

// durationSeconds parses a Go duration string into whole seconds, rounding up so
// a sub-second value still yields at least 1s (kubernetes probe timings are
// second-granularity). An empty or unparseable value yields 0.
func durationSeconds(s string) int32 {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	secs := (d + time.Second - 1) / time.Second
	return int32(secs)
}

// resourceLimits builds a kubernetes resources.limits list from an api.Resources,
// or nil when nothing is limited. CPU is expressed in millicores (0.5 -> "500m")
// and memory as a byte quantity.
func resourceLimits(r *api.Resources) corev1.ResourceList {
	if r == nil {
		return nil
	}
	limits := corev1.ResourceList{}
	if r.CPULimit > 0 {
		milli := int64(r.CPULimit*1000 + 0.5)
		limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(milli, resource.DecimalSI)
	}
	if r.MemoryLimit > 0 {
		limits[corev1.ResourceMemory] = *resource.NewQuantity(r.MemoryLimit, resource.BinarySI)
	}
	if len(limits) == 0 {
		return nil
	}
	return limits
}

// resourceRequests builds a kubernetes resources.requests list from an
// api.Resources' reservation axes (compose deploy.resources.reservations), or
// nil when nothing is reserved. Mirrors resourceLimits: CPU in millicores, memory
// as a byte quantity.
func resourceRequests(r *api.Resources) corev1.ResourceList {
	if r == nil {
		return nil
	}
	requests := corev1.ResourceList{}
	if r.ReservedCPU > 0 {
		milli := int64(r.ReservedCPU*1000 + 0.5)
		requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(milli, resource.DecimalSI)
	}
	if r.ReservedMemory > 0 {
		requests[corev1.ResourceMemory] = *resource.NewQuantity(r.ReservedMemory, resource.BinarySI)
	}
	if len(requests) == 0 {
		return nil
	}
	return requests
}

// updateStrategy maps a compose deploy.update_config onto a Deployment
// rolling-update strategy, or nil when unset (leaving the cluster default,
// itself RollingUpdate). Parallelism sizes the surge/unavailable magnitude (0 =>
// 1). Order selects the direction:
//
//   - "start-first": surge a new pod up BEFORE removing the old — maxSurge =
//     parallelism, maxUnavailable = 0.
//   - "stop-first" (default, and any other value): take an old pod down before
//     bringing a new one up — maxUnavailable = parallelism, maxSurge = 0.
//
// The remaining update_config knobs (delay, monitor, max_failure_ratio) have no
// Deployment equivalent and are dropped at translate time, so they never reach
// here.
func updateStrategy(uc *api.UpdateConfig) *appsv1.DeploymentStrategy {
	if uc == nil {
		return nil
	}
	n := uc.Parallelism
	if n <= 0 {
		n = 1
	}
	var surge, unavailable intstr.IntOrString
	if uc.Order == "start-first" {
		surge = intstr.FromInt32(int32(n))
		unavailable = intstr.FromInt32(0)
	} else {
		surge = intstr.FromInt32(0)
		unavailable = intstr.FromInt32(int32(n))
	}
	return &appsv1.DeploymentStrategy{
		Type:          appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &surge, MaxUnavailable: &unavailable},
	}
}

// tmpfsVolumes builds the memory-backed emptyDir volumes and matching mounts for
// compose `tmpfs` and `shm_size`. Each tmpfs entry ("path[:options]") becomes an
// emptyDir{Medium: Memory} mounted at that path; a non-zero shm_size becomes one
// such volume (with a SizeLimit) mounted at /dev/shm. Per-mount tmpfs options
// (the ":..." tail) have no emptyDir equivalent and are dropped with a warning.
// Volume names are deterministic so a repeated Apply produces the same pod spec.
func tmpfsVolumes(ctx context.Context, spec api.DeploySpec) ([]corev1.Volume, []corev1.VolumeMount) {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	for i, t := range spec.Tmpfs {
		path, opts, _ := strings.Cut(t, ":")
		if opts != "" {
			log.WarnContext(ctx, "tmpfs mount options are not supported on an emptyDir and were dropped", "tmpfs", t)
		}
		name := fmt.Sprintf("tmpfs-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name:         name,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: path})
	}
	if spec.ShmSize > 0 {
		ed := &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}
		q := resource.NewQuantity(spec.ShmSize, resource.BinarySI)
		ed.SizeLimit = q
		volumes = append(volumes, corev1.Volume{
			Name:         "cornus-shm",
			VolumeSource: corev1.VolumeSource{EmptyDir: ed},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "cornus-shm", MountPath: "/dev/shm"})
	}
	return volumes, mounts
}

// applyHostNamespaces maps compose `pid` / `ipc` onto the pod. Only the "host"
// form is expressible (HostPID / HostIPC); other forms name a runtime-shared
// namespace a Deployment pod cannot join and are warned about.
func applyHostNamespaces(ctx context.Context, spec api.DeploySpec, podSpec *corev1.PodSpec) {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))
	if spec.PIDMode != "" {
		if spec.PIDMode == "host" {
			podSpec.HostPID = true
		} else {
			log.WarnContext(ctx, "pid mode is not supported; only \"host\" maps to a pod field (HostPID)", "pid", spec.PIDMode)
		}
	}
	if spec.IPCMode != "" {
		if spec.IPCMode == "host" {
			podSpec.HostIPC = true
		} else {
			log.WarnContext(ctx, "ipc mode is not supported; only \"host\" maps to a pod field (HostIPC)", "ipc", spec.IPCMode)
		}
	}
}

// warnUnsupportedResourceKeys logs the compose resource keys with no native
// kubernetes equivalent: `ulimits` (a container has no per-process rlimit knob)
// and `devices` (host-device mapping needs a device plugin). The deploy proceeds
// without them, matching how the host backends report their own gaps.
func warnUnsupportedResourceKeys(ctx context.Context, spec api.DeploySpec) {
	log := logging.FromContext(ctx, slog.Group("kubernetes", "deployment", spec.Name))
	if len(spec.Ulimits) > 0 {
		log.WarnContext(ctx, "ulimits are not supported (no per-container rlimit field); ignoring")
	}
	if len(spec.Devices) > 0 {
		log.WarnContext(ctx, "devices are not supported (host-device mapping needs a device plugin); ignoring")
	}
}

// dnsRole builds the caretaker DNS role for a spec: its peer records, the pod's
// namespace search domain, and the discovered cluster-DNS upstream for unknowns.
func (b *Backend) dnsRole(spec api.DeploySpec) *caretaker.DNSRole {
	up := b.clusterDNSIP()
	if up != "" {
		up = fmt.Sprintf("%s:53", up)
	}
	return &caretaker.DNSRole{
		Records:  spec.DNS.Records,
		Domain:   b.namespace + ".svc.cluster.local",
		Upstream: up,
	}
}

// setPodDNS points the pod's resolver at the local caretaker DNS while preserving
// the cluster search domains, so a bare `peer` still expands to
// `peer.<ns>.svc.cluster.local` (which the caretaker owns) and every other name
// forwards to the cluster DNS.
func (b *Backend) setPodDNS(podSpec *corev1.PodSpec) {
	podSpec.DNSPolicy = corev1.DNSNone
	podSpec.DNSConfig = &corev1.PodDNSConfig{
		Nameservers: []string{"127.0.0.1"},
		Searches:    []string{b.namespace + ".svc.cluster.local", "svc.cluster.local", "cluster.local"},
		Options:     []corev1.PodDNSConfigOption{{Name: "ndots", Value: ptr.To("5")}},
	}
}

// injectDNS adds a standalone caretaker running the DNS role plus the pod
// dnsConfig that routes the app's resolver through it. The sidecar needs only
// NET_BIND_SERVICE (to bind :53), not privilege.
func (b *Backend) injectDNS(spec api.DeploySpec, podSpec *corev1.PodSpec) {
	always := corev1.ContainerRestartPolicyAlways
	cfg := caretaker.Config{DNS: b.dnsRole(spec)}
	ctr := corev1.Container{
		Name:            "cornus-caretaker",
		Image:           b.sidecarImageFor(spec),
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		RestartPolicy:   &always, // native sidecar: runs alongside the app
		ImagePullPolicy: imagePullPolicy(),
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_BIND_SERVICE"}},
		},
	}
	if spec.Docker != nil {
		b.addDockerRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	}
	b.addAgentForwardRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	b.addTelemetryRole(spec, podSpec, &cfg)
	ctr.Env = b.caretakerConfigEnv(cfg, spec.Name)
	podSpec.InitContainers = append(podSpec.InitContainers, ctr)
	b.setPodDNS(podSpec)
}

// hubDiscovery translates a spec's HubSpec into the caretaker hub role plus the
// synthetic-IP DNS records (import name -> 127.x.y.z) that make an app's dial of a
// peer name funnel through the hub. Exports register either dial-direct (the hub
// dials this workload's cluster Service) or for delivery (the hub relays to this
// pod, which dials localhost). Identity defaults to the deployment name.
func (b *Backend) hubDiscovery(spec api.DeploySpec) (*caretaker.HubRole, map[string]string) {
	h := spec.Hub
	identity := h.Identity
	if identity == "" {
		identity = spec.Name
	}
	role := &caretaker.HubRole{Server: os.Getenv("CORNUS_ADVERTISE_URL"), Identity: identity}
	for _, e := range h.Export {
		if e.Deliver {
			role.Register = append(role.Register, caretaker.HubService{
				Name: e.Name, Target: fmt.Sprintf("127.0.0.1:%d", e.Port), Protocol: e.Protocol,
			})
		} else {
			// Dial-direct: the hub UDP/TCP-dials this workload's cluster Service. The
			// Service port protocol comes from spec.Ports (a UDP export needs the port
			// declared udp there so the Service exposes it as UDP); Protocol tells the
			// hub which socket to open.
			role.Register = append(role.Register, caretaker.HubService{
				Name: e.Name, Addr: fmt.Sprintf("%s.%s.svc.cluster.local:%d", spec.Name, b.namespace, e.Port), Protocol: e.Protocol,
			})
		}
	}
	records := map[string]string{}
	for _, im := range h.Import {
		ip := hub.SyntheticIP(im.Name)
		records[im.Name] = ip
		role.Reach = append(role.Reach, caretaker.HubPeer{Name: im.Name, Listen: ip, Ports: im.Ports, Protocol: im.Protocol})
	}
	// Dynamic import discovery: the caretaker subscribes to catalog pushes and
	// binds/unbinds a listener at hub.SyntheticIP(name) on these ports as services
	// appear/vanish (excluding its own exports and the static imports above). No
	// DNS records are wired here — the discovered names are unknown at deploy
	// time; the app reaches a dynamic import via its deterministic synthetic IP.
	if h.ImportDynamic != nil {
		role.ReachDynamic = &caretaker.HubDynamicReach{Ports: h.ImportDynamic.Ports, Protocol: h.ImportDynamic.Protocol}
	}
	return role, records
}

// injectHub adds a caretaker running the hub role plus a DNS role whose records
// resolve each imported peer to its synthetic loopback IP (merged with any explicit
// spec.DNS records), and points the pod's resolver at it. The sidecar needs only
// NET_BIND_SERVICE (to bind :53 and the synthetic-IP listeners), not privilege.
func (b *Backend) injectHub(ctx context.Context, spec api.DeploySpec, podSpec *corev1.PodSpec) {
	role, records := b.hubDiscovery(spec)
	dnsRecords := map[string]string{}
	if spec.DNS != nil {
		for k, v := range spec.DNS.Records {
			dnsRecords[k] = v
		}
	}
	for name, ip := range records {
		dnsRecords[name] = ip
	}
	up := b.clusterDNSIP()
	if up != "" {
		up = fmt.Sprintf("%s:53", up)
	}
	always := corev1.ContainerRestartPolicyAlways
	cfg := caretaker.Config{
		Hub: role,
		DNS: &caretaker.DNSRole{Records: dnsRecords, Domain: b.namespace + ".svc.cluster.local", Upstream: up},
	}
	ctr := corev1.Container{
		Name:            "cornus-caretaker",
		Image:           b.sidecarImageFor(spec),
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		RestartPolicy:   &always,
		ImagePullPolicy: imagePullPolicy(),
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_BIND_SERVICE"}},
		},
	}
	if spec.Docker != nil {
		b.addDockerRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	}
	b.addAgentForwardRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	b.addTelemetryRole(spec, podSpec, &cfg)
	b.addCaretakerTLS(ctx, podSpec, &ctr, &cfg) // before the config marshals into the env
	ctr.Env = b.caretakerConfigEnv(cfg, spec.Name)
	podSpec.InitContainers = append(podSpec.InitContainers, ctr)
	b.setPodDNS(podSpec)
}

// dockerEndpointPort is the default loopback TCP port the caretaker Docker-API
// endpoint binds inside the shared pod netns (the conventional Docker API port).
const dockerEndpointPort = 2375

// dockerSocketVolume / dockerSocketDir back the unix-transport Docker endpoint: a
// shared emptyDir the caretaker and app container both mount, so the socket the
// caretaker binds is visible to the app.
const (
	dockerSocketVolume = "cornus-docker-sock"
	dockerSocketDir    = "/cornus/docker"
)

// agentSocketVolume is the shared emptyDir backing the AgentRelayRole's fixed
// socket (remotecompanion.AgentSocketPath), mounted at
// remotecompanion.AgentScratchDir in both the caretaker and the app container —
// the kubernetes analogue of dockerhost/containerdhost's rshared/rslave
// propagated bind for the same fixed path (a plain shared volume suffices here;
// kubernetes native sidecars already share nothing else that would need the
// propagation trick).
const agentSocketVolume = "cornus-agent-sock"

// addAgentForwardRole folds the AgentRelay role into a caretaker config when
// spec.AgentForward is set: it sets cfg.Instance (so the server can find this
// connection by instance, mirroring a dockerhost/containerdhost remote
// companion) and cfg.AgentRelay, and adds the shared emptyDir carrying the
// fixed agent-relay socket, mounted into both the app container and the
// caretaker (appended to caretakerMounts). A no-op when spec.AgentForward is
// false. Call before caretakerConfigEnv.
func (b *Backend) addAgentForwardRole(spec api.DeploySpec, podSpec *corev1.PodSpec, cfg *caretaker.Config, caretakerMounts *[]corev1.VolumeMount) {
	if !spec.AgentForward {
		return
	}
	cfg.Instance = remotecompanion.InstanceKey(spec.Name, 0)
	cfg.AgentRelay = &caretaker.AgentRelayRole{
		Server:     os.Getenv("CORNUS_ADVERTISE_URL"),
		SocketPath: remotecompanion.AgentSocketPath,
	}
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name:         agentSocketVolume,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
		Name: agentSocketVolume, MountPath: remotecompanion.AgentScratchDir,
	})
	*caretakerMounts = append(*caretakerMounts, corev1.VolumeMount{
		Name: agentSocketVolume, MountPath: remotecompanion.AgentScratchDir,
	})
}

// addDockerRole folds the Docker-API role into a caretaker config: it sets
// cfg.Docker (server = the advertised cornus URL, the client token supplied later
// by caretakerConfigEnv), advertises the endpoint to the app container via
// DOCKER_HOST, and — for the unix transport — adds a shared emptyDir carrying the
// socket, mounted into both the app container and the caretaker (appended to
// caretakerMounts). Call before caretakerConfigEnv.
func (b *Backend) addDockerRole(spec api.DeploySpec, podSpec *corev1.PodSpec, cfg *caretaker.Config, caretakerMounts *[]corev1.VolumeMount) {
	ds := spec.Docker
	transport := ds.Transport
	if transport == "" {
		transport = "tcp"
	}
	port := ds.Port
	if port == 0 {
		port = dockerEndpointPort
	}
	sockPath := ds.SocketPath
	if sockPath == "" {
		sockPath = dockerSocketDir + "/docker.sock"
	}
	envVar := ds.EnvVar
	if envVar == "" {
		envVar = "DOCKER_HOST"
	}

	role := &caretaker.DockerRole{Server: os.Getenv("CORNUS_ADVERTISE_URL")}
	var dockerHost string
	if transport == "tcp" || transport == "both" {
		role.TCPAddr = fmt.Sprintf("127.0.0.1:%d", port)
		dockerHost = fmt.Sprintf("tcp://127.0.0.1:%d", port)
	}
	if transport == "unix" || transport == "both" {
		role.UnixPath = sockPath
		if dockerHost == "" { // tcp wins for DOCKER_HOST in "both" mode
			dockerHost = "unix://" + sockPath
		}
		dir := filepath.Dir(sockPath)
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name:         dockerSocketVolume,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name: dockerSocketVolume, MountPath: dir,
		})
		*caretakerMounts = append(*caretakerMounts, corev1.VolumeMount{Name: dockerSocketVolume, MountPath: dir})
	}

	cfg.Docker = role
	podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, corev1.EnvVar{Name: envVar, Value: dockerHost})
}

// injectDocker adds a standalone caretaker running only the Docker-API role (for a
// pod that wants the loopback Docker endpoint but no mount/hub/dns role). The
// sidecar needs no special privilege: it binds a high loopback port and/or a unix
// socket on an emptyDir and dials out to the cornus server. Its startup probe gates
// the app container until the endpoint is live.
func (b *Backend) injectDocker(ctx context.Context, spec api.DeploySpec, podSpec *corev1.PodSpec) {
	always := corev1.ContainerRestartPolicyAlways
	cfg := caretaker.Config{}
	ctr := corev1.Container{
		Name:            "cornus-caretaker",
		Image:           b.sidecarImageFor(spec),
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		RestartPolicy:   &always,
		ImagePullPolicy: imagePullPolicy(),
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{"cornus", "caretaker-check"},
			}},
			PeriodSeconds:    1,
			FailureThreshold: 120,
		},
	}
	b.addDockerRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	b.addAgentForwardRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	b.addTelemetryRole(spec, podSpec, &cfg)
	b.addCaretakerTLS(ctx, podSpec, &ctr, &cfg) // before the config marshals into the env
	ctr.Env = b.caretakerConfigEnv(cfg, spec.Name)
	podSpec.InitContainers = append(podSpec.InitContainers, ctr)
}

// injectAgentForward adds a standalone caretaker running only the AgentRelay
// role (for a pod that opted into AgentForward but has no mount/hub/dns/docker
// role to fold it into). Like injectDocker, the sidecar needs no special
// privilege: it listens on a unix socket on a shared emptyDir and dials out to
// the cornus server.
func (b *Backend) injectAgentForward(ctx context.Context, spec api.DeploySpec, podSpec *corev1.PodSpec) {
	always := corev1.ContainerRestartPolicyAlways
	cfg := caretaker.Config{}
	ctr := corev1.Container{
		Name:            "cornus-caretaker",
		Image:           b.sidecarImageFor(spec),
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		RestartPolicy:   &always,
		ImagePullPolicy: imagePullPolicy(),
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{"cornus", "caretaker-check"},
			}},
			PeriodSeconds:    1,
			FailureThreshold: 120,
		},
	}
	b.addAgentForwardRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	b.addTelemetryRole(spec, podSpec, &cfg)
	b.addCaretakerTLS(ctx, podSpec, &ctr, &cfg) // before the config marshals into the env
	ctr.Env = b.caretakerConfigEnv(cfg, spec.Name)
	podSpec.InitContainers = append(podSpec.InitContainers, ctr)
}

// addTelemetryRole folds the embedded OpenTelemetry Collector role into a
// caretaker config when the spec opts into telemetry: it sets cfg.Otel from the
// resolved wiring and injects the OTEL_* variables into the app container so its
// SDK ships to the pod-loopback receiver. It is a no-op when telemetry is
// inactive, so it can be called unconditionally from every caretaker-assembly
// site (like addAgentForwardRole). No extra volume is needed — the receiver is a
// loopback listener in the shared pod netns. Call before caretakerConfigEnv.
func (b *Backend) addTelemetryRole(spec api.DeploySpec, podSpec *corev1.PodSpec, cfg *caretaker.Config) {
	if !spec.Telemetry.Active() {
		return
	}
	// Telemetry.Validate ran at the request boundary; a resolution error here is
	// unreachable, so skip defensively rather than panic.
	w, err := deploy.BuildTelemetryWiring(spec, spec.Name)
	if err != nil || w == nil {
		return
	}
	role := w.Role
	cfg.Otel = &role
	for _, kv := range w.EnvSorted() {
		podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, corev1.EnvVar{Name: kv[0], Value: kv[1]})
	}
}

// injectTelemetry adds a standalone caretaker running only the OTel Collector role
// (for a pod that opts into telemetry but has no proxy/hub/dns/docker/agentforward
// role to fold it into). Like injectDocker, the sidecar needs no special privilege:
// it binds loopback OTLP ports in the shared pod netns and exports outward. Its
// startup probe gates the app container until the receiver is live.
func (b *Backend) injectTelemetry(ctx context.Context, spec api.DeploySpec, podSpec *corev1.PodSpec) {
	always := corev1.ContainerRestartPolicyAlways
	cfg := caretaker.Config{}
	ctr := corev1.Container{
		Name:            "cornus-caretaker",
		Image:           b.sidecarImageFor(spec),
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		RestartPolicy:   &always,
		ImagePullPolicy: imagePullPolicy(),
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{"cornus", "caretaker-check"},
			}},
			PeriodSeconds:    1,
			FailureThreshold: 120,
		},
	}
	b.addTelemetryRole(spec, podSpec, &cfg)
	b.addAgentForwardRole(spec, podSpec, &cfg, &ctr.VolumeMounts)
	b.addCaretakerTLS(ctx, podSpec, &ctr, &cfg) // before the config marshals into the env
	ctr.Env = b.caretakerConfigEnv(cfg, spec.Name)
	podSpec.InitContainers = append(podSpec.InitContainers, ctr)
}

// AdvertisedRegistry derives the registry host a cluster node can pull images from
// by introspecting cornus's own Service in b.namespace. It implements the optional
// deploy.RegistryAdvertiser capability the server calls for GET /.cornus/v1/info. Only the
// Service types that are a deliberate, self-describing exposure choice are auto-
// advertised:
//
//   - NodePort:     localhost:<nodePort>   (node-portable: every node binds it)
//   - LoadBalancer: <lb-host-or-ip>:<port>
//
// A ClusterIP Service returns an empty ServerInfo — it is the default type, carries
// no signal of intent, and a bare ClusterIP is not reliably pullable (node trust,
// NetworkPolicy), so advertising it would silently break the single-node quick start
// (whose node only trusts localhost:5000). ClusterIP-advertise, hostPort/hostNetwork,
// TLS, and ingress topologies — none fully describable from the Service alone — are
// opted into explicitly with CORNUS_ADVERTISE_REGISTRY on the server instead, which
// takes precedence over this method. An empty return (not an error) makes the client
// fall back to its endpoint host, preserving today's behavior. The scheme is "http".
func (b *Backend) AdvertisedRegistry(ctx context.Context) (api.ServerInfo, error) {
	svc, err := b.ownService(ctx)
	if err != nil {
		return api.ServerInfo{}, err
	}
	if svc == nil {
		return api.ServerInfo{}, nil
	}
	port := registryServicePort(svc)
	if port == nil {
		return api.ServerInfo{}, nil
	}
	var host string
	switch svc.Spec.Type {
	case corev1.ServiceTypeNodePort:
		if port.NodePort != 0 {
			host = fmt.Sprintf("localhost:%d", port.NodePort)
		}
	case corev1.ServiceTypeLoadBalancer:
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			addr := ing.Hostname
			if addr == "" {
				addr = ing.IP
			}
			if addr != "" {
				host = fmt.Sprintf("%s:%d", addr, port.Port)
				break
			}
		}
	}
	if host == "" {
		return api.ServerInfo{}, nil
	}
	return api.ServerInfo{RegistryHost: host, RegistryScheme: "http"}, nil
}

// ownService finds cornus's own Service in b.namespace, trying the helm label
// (app.kubernetes.io/name=cornus) then the raw-manifest label (app=cornus), and
// skipping the headless hub Service. It returns nil (no error) when there is no
// unambiguous single match, mirroring how the port-forward discovery selects it.
func (b *Backend) ownService(ctx context.Context) (*corev1.Service, error) {
	for _, sel := range []string{"app.kubernetes.io/name=cornus", "app=cornus"} {
		list, err := b.clientset.CoreV1().Services(b.namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return nil, err
		}
		var cand *corev1.Service
		ambiguous := false
		for i := range list.Items {
			s := &list.Items[i]
			if s.Spec.ClusterIP == corev1.ClusterIPNone {
				continue // the multi-replica hub's headless Service
			}
			if cand != nil {
				ambiguous = true
				break
			}
			cand = s
		}
		if cand != nil && !ambiguous {
			return cand, nil
		}
	}
	return nil, nil
}

// registryServicePort picks the Service port carrying the registry: the one named
// "http", else port 5000, else the sole port. Mirrors svcforward's port selection.
func registryServicePort(svc *corev1.Service) *corev1.ServicePort {
	ports := svc.Spec.Ports
	for i := range ports {
		if ports[i].Name == "http" {
			return &ports[i]
		}
	}
	for i := range ports {
		if ports[i].Port == 5000 {
			return &ports[i]
		}
	}
	if len(ports) == 1 {
		return &ports[0]
	}
	return nil
}

// clusterDNSIP discovers the cluster DNS service (kube-system/kube-dns) ClusterIP
// the caretaker forwards unknown names to; empty if it cannot be resolved (the
// caretaker then answers only its own records, NODATA for the rest).
func (b *Backend) clusterDNSIP() string {
	svc, err := b.clientset.CoreV1().Services("kube-system").Get(context.Background(), "kube-dns", metav1.GetOptions{})
	if err != nil || svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return ""
	}
	return svc.Spec.ClusterIP
}

// proxyPort is the port the caretaker's enforcing proxy listens on for
// iptables-redirected app egress. proxyUID is the dedicated uid the proxy-only
// caretaker runs as so its own upstream dials are exempted from the redirect
// (Istio-style). proxyMark is the SO_MARK used instead when the caretaker also
// serves mounts and so must run as root: it marks its sockets and the redirect
// exempts that mark.
const (
	proxyPort = 15001
	proxyUID  = 1337
	proxyMark = 1337
)

// netRedirectInit builds the init container that programs the nftables egress
// redirect (via netlink, no CLI) before the app and caretaker start. It exempts
// the caretaker's own traffic by uid (proxy-only) or by firewall mark
// (proxy+mounts); pass 0 for the one not in use.
func netRedirectInit(image string, port, exemptUID, exemptMark int) corev1.Container {
	args := []string{"net-redirect", "--to-port", strconv.Itoa(port)}
	if exemptUID > 0 {
		args = append(args, "--exempt-uid", strconv.Itoa(exemptUID))
	}
	if exemptMark > 0 {
		args = append(args, "--exempt-mark", strconv.Itoa(exemptMark))
	}
	return corev1.Container{
		Name:            "cornus-net-redirect",
		Image:           image,
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            args,
		ImagePullPolicy: imagePullPolicy(),
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}},
		},
	}
}

// egressProxyPort is the loopback port the client-side-egress caretaker proxy
// listens on. It is distinct from proxyPort (the peer-isolation proxy) so the two
// can coexist in principle; a spec using both is rejected in ApplyWithAttachments.
const egressProxyPort = 15002

// needsEgressCaretaker reports whether an egress attachment requires the caretaker
// sidecar (proxy/transparent modes relay through it; env mode does not).
func needsEgressCaretaker(egress *deploy.AttachEgress) bool {
	if egress == nil || egress.Spec == nil {
		return false
	}
	switch egress.Spec.Mode {
	case "proxy", "transparent":
		return true
	}
	return false
}

// egressListenPort resolves the caretaker egress proxy's listen port.
func egressListenPort(e *api.EgressSpec) int {
	if e.ListenPort != 0 {
		return e.ListenPort
	}
	return egressProxyPort
}

// addEgressToCaretaker folds the egress role into the pod's caretaker config and
// wires the interception mechanism. Proxy mode injects the proxy environment
// variables on the app container (no privilege — the caretaker just binds loopback
// and dials the server). Transparent mode adds the NET_ADMIN net-redirect init
// container and exempts the caretaker's own egress: by a dedicated uid when the
// caretaker is egress-only, or by a firewall mark when it also carries mounts (then
// it must run as root and cannot be exempted by uid). Returns the RunAsUser the
// caretaker container must use (0 = none), which the caller applies to its security
// context.
func (b *Backend) addEgressToCaretaker(egress *deploy.AttachEgress, image string, mountsPresent bool, podSpec *corev1.PodSpec, cfg *caretaker.Config) int {
	e := egress.Spec
	port := egressListenPort(e)
	cfg.Egress = &caretaker.EgressRole{
		Server:     egress.RelayURL,
		Session:    egress.Session,
		Mode:       e.Mode,
		ListenPort: port,
		Rules:      e.Rules,
		Script:     e.Script,
		Default:    e.Default,
	}
	switch e.Mode {
	case "transparent":
		if mountsPresent {
			podSpec.InitContainers = append(podSpec.InitContainers, netRedirectInit(image, port, 0, proxyMark))
			cfg.Mark = proxyMark
			return 0
		}
		podSpec.InitContainers = append(podSpec.InitContainers, netRedirectInit(image, port, proxyUID, 0))
		return proxyUID
	default: // proxy
		for _, ev := range egressProxyEnv(e, port) {
			podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, ev)
		}
		return 0
	}
}

// egressProxyEnv builds the proxy env vars pointing the app container at the
// caretaker's loopback forward proxy. HTTP_PROXY/HTTPS_PROXY use the http:// scheme
// — the caretaker is a full HTTP proxy (CONNECT for HTTPS, absolute-form forwarding
// for plain HTTP), and an http:// proxy endpoint is the most universally-supported
// value. ALL_PROXY uses socks5h:// so SOCKS-aware and non-HTTP clients get the same
// reach with remote (terminus-side) DNS. Both upper- and lower-case spellings are
// set; NO_PROXY keeps loopback and any cluster-routed destinations off the proxy.
func egressProxyEnv(e *api.EgressSpec, port int) []corev1.EnvVar {
	m := egresspolicy.ProxyEnv(*e, port)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]corev1.EnvVar, 0, len(m))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: m[k]})
	}
	return out
}

// injectProxy adds the userspace egress proxy to a pod. Cooperative mode needs
// no privilege (DNS + loopback interception); enforcing mode (the default)
// captures all egress via an nftables-redirect init container.
func (b *Backend) injectProxy(spec api.DeploySpec, podSpec *corev1.PodSpec) {
	if strings.EqualFold(spec.Proxy.Mode, "cooperative") {
		b.injectProxyCooperative(spec, podSpec)
		return
	}
	b.injectProxyEnforcing(spec, podSpec)
}

// injectProxyCooperative wires the no-privilege proxy: each Allow peer's DNS
// name is pointed at a distinct loopback address (hostAliases) that a plain
// caretaker sidecar listens on and forwards to the peer's real Service. No init
// container, no NET_ADMIN, no special uid — but soft isolation (an app that
// dials a raw pod IP is not intercepted).
func (b *Backend) injectProxyCooperative(spec api.DeploySpec, podSpec *corev1.PodSpec) {
	aliases, coop := b.cooperativeAliases(spec)
	if len(coop) == 0 {
		return
	}
	podSpec.HostAliases = append(podSpec.HostAliases, aliases...)

	always := corev1.ContainerRestartPolicyAlways
	cfg := caretaker.Config{Proxy: &caretaker.ProxyRole{Mode: "cooperative", Coop: coop}}
	b.addTelemetryRole(spec, podSpec, &cfg) // fold the OTel collector into this one sidecar
	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            "cornus-caretaker",
		Image:           b.sidecarImageFor(spec),
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		Env:             b.caretakerConfigEnv(cfg, spec.Name),
		RestartPolicy:   &always, // native sidecar: runs alongside the app
		ImagePullPolicy: imagePullPolicy(),
	})
}

// cooperativeAliases computes the per-peer loopback hostAliases and the matching
// caretaker upstream table for cooperative mode: each Allow peer that declares
// ports gets a distinct 127/8 loopback the sidecar listens on and forwards to
// the peer's real headless-Service FQDN. Shared by the proxy-only and
// proxy+mounts paths.
func (b *Backend) cooperativeAliases(spec api.DeploySpec) ([]corev1.HostAlias, []caretaker.CoopUpstream) {
	allow := append([]string(nil), spec.Proxy.Allow...)
	sort.Strings(allow)
	var aliases []corev1.HostAlias
	var coop []caretaker.CoopUpstream
	for i, name := range allow {
		ports := spec.Proxy.Ports[name]
		if len(ports) == 0 {
			continue // nothing to intercept for a peer that declares no ports
		}
		ip := loopbackFor(i)
		aliases = append(aliases, corev1.HostAlias{IP: ip, Hostnames: []string{name}})
		fqdn := sanitizeDNS1123(name) + "." + b.namespace + ".svc.cluster.local"
		coop = append(coop, caretaker.CoopUpstream{Listen: ip, Forward: fqdn, Ports: ports})
	}
	return aliases, coop
}

// addProxyToMountCaretaker folds the proxy role into a mount caretaker's config
// so a pod using BOTH client-local mounts and the proxy carries exactly ONE
// (privileged, root) caretaker. Enforcing mode cannot exempt that root caretaker
// from the egress redirect by uid (the app is root too), so it uses a firewall
// mark instead: the caretaker marks its sockets (cfg.Mark) and a mark-exempting
// net-redirect init is added. Cooperative mode needs neither — just hostAliases
// and the loopback upstream table.
func (b *Backend) addProxyToMountCaretaker(spec api.DeploySpec, podSpec *corev1.PodSpec, cfg *caretaker.Config) {
	if strings.EqualFold(spec.Proxy.Mode, "cooperative") {
		aliases, coop := b.cooperativeAliases(spec)
		if len(coop) == 0 {
			return
		}
		podSpec.HostAliases = append(podSpec.HostAliases, aliases...)
		cfg.Proxy = &caretaker.ProxyRole{Mode: "cooperative", Coop: coop}
		return
	}
	port := spec.Proxy.ListenPort
	if port == 0 {
		port = proxyPort
	}
	podSpec.InitContainers = append(podSpec.InitContainers, netRedirectInit(b.sidecarImageFor(spec), port, 0, proxyMark))
	cfg.Mark = proxyMark // stamped on the caretaker's own sockets; the redirect exempts it
	cfg.Proxy = &caretaker.ProxyRole{ListenPort: port, Allow: spec.Proxy.Allow}
}

// loopbackFor maps a peer index to a distinct 127/8 loopback address, starting
// at 127.0.1.1 so it never collides with 127.0.0.1 (real localhost).
func loopbackFor(i int) string {
	v := 0x000101 + i
	return fmt.Sprintf("127.%d.%d.%d", (v>>16)&0xff, (v>>8)&0xff, v&0xff)
}

// injectProxyEnforcing adds the enforcing egress proxy to a pod: a privileged
// init container that iptables-redirects the app's outbound TCP into the
// caretaker, and the caretaker itself running the proxy role as the exempt uid.
// All of the app's egress is captured; only destinations resolving to an Allow
// peer are forwarded. The caretaker here runs the proxy role ONLY (mounts need
// root, so proxy+mounts is rejected upstream in ApplyWithMounts).
func (b *Backend) injectProxyEnforcing(spec api.DeploySpec, podSpec *corev1.PodSpec) {
	port := spec.Proxy.ListenPort
	if port == 0 {
		port = proxyPort
	}
	uid := int64(proxyUID)
	always := corev1.ContainerRestartPolicyAlways
	image := b.sidecarImageFor(spec)

	// Programs the nftables redirect, exempting the caretaker by uid.
	podSpec.InitContainers = append(podSpec.InitContainers, netRedirectInit(image, port, proxyUID, 0))

	cfg := caretaker.Config{Proxy: &caretaker.ProxyRole{ListenPort: port, Allow: spec.Proxy.Allow}}
	b.addTelemetryRole(spec, podSpec, &cfg) // fold the OTel collector into this one sidecar
	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            "cornus-caretaker",
		Image:           image,
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		Env:             b.caretakerConfigEnv(cfg, spec.Name),
		RestartPolicy:   &always, // native sidecar: runs alongside the app
		ImagePullPolicy: imagePullPolicy(),
		// Runs as the exempt uid so its forwarded dials escape the redirect; no
		// privilege needed for a userspace proxy.
		SecurityContext: &corev1.SecurityContext{RunAsUser: &uid},
	})
}

// sidecarMountBase is where the caretaker sidecar kernel-9p-mounts each
// backing inside the pod; the app container sees the same emptyDir at the
// mount's target via propagation.
const sidecarMountBase = "/cornus/mounts"

// deploymentWithMounts builds the Deployment for a spec whose AttachMounts are
// realized as live 9P mounts inside the pod. Every mount gets a shared emptyDir
// (the propagation medium) and an app-container volumeMount at the target with
// HostToContainer propagation; a SINGLE privileged native-sidecar "caretaker"
// then kernel-9p-mounts them all (Bidirectional) — one sidecar per pod, no
// matter how many mounts (nor, in future, network roles). Its config rides an
// env var, and its startup probe gates the app container until EVERY mount is
// live.
func (b *Backend) deploymentWithAttachments(ctx context.Context, spec api.DeploySpec, mounts []deploy.AttachMount, creds []deploy.AttachCredential, egress *deploy.AttachEgress) *appsv1.Deployment {
	// Build the base Deployment with no mounts at all — the caretaker supplies
	// every client-local mount, and hostPath is never used. (ApplyWithMounts has
	// already rejected any mount without a sidecar.) Proxy is cleared on the base
	// so b.deployment does not inject a SECOND caretaker; when the spec has a
	// proxy it is folded into this same (privileged) caretaker below.
	base := spec
	base.Mounts = nil
	base.Proxy = nil
	base.DNS = nil            // folded into the one mount caretaker below, not a second sidecar
	base.Hub = nil            // ditto — the hub role folds into the same caretaker below
	base.Credentials = nil    // realized from the AttachCredential list below, not the spec
	base.Egress = nil         // realized from the AttachEgress below, not the spec
	base.Docker = nil         // folded into the one caretaker below, not a second sidecar
	base.AgentForward = false // ditto — the AgentRelay role folds into the same caretaker below
	base.Telemetry = nil      // ditto — the OTel collector role folds into the same caretaker below
	dep := b.deployment(ctx, base)

	podSpec := &dep.Spec.Template.Spec
	bidir := corev1.MountPropagationBidirectional
	htoc := corev1.MountPropagationHostToContainer
	always := corev1.ContainerRestartPolicyAlways

	agentImage := b.sidecarImageFor(spec)
	var caretakerMounts []corev1.VolumeMount
	roles := make([]caretaker.MountRole, 0, len(mounts))
	for i, m := range mounts {
		vname := fmt.Sprintf("cornus-mount-%d", i)
		sidecarPath := fmt.Sprintf("%s/%d", sidecarMountBase, i)

		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name:         vname,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		// The app container receives the propagated 9p mount (read-only per the
		// mount's flag; the caretaker mounts it rw or ro to match).
		podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:             vname,
			MountPath:        m.Target,
			ReadOnly:         m.ReadOnly,
			MountPropagation: &htoc,
		})
		// The caretaker mounts the backing at the scratch path with Bidirectional
		// propagation so it reaches the app's emptyDir.
		caretakerMounts = append(caretakerMounts, corev1.VolumeMount{
			Name:             vname,
			MountPath:        sidecarPath,
			MountPropagation: &bidir,
		})
		roles = append(roles, caretaker.MountRole{
			Server:     m.RelayURL,
			Session:    m.Session,
			Name:       m.Name,
			Target:     sidecarPath,
			ReadOnly:   m.ReadOnly,
			AsyncCache: m.AsyncCache,
		})
		if m.AgentImage != "" {
			agentImage = m.AgentImage
		}
	}

	// The caretaker carries the mount roles, plus the proxy role when the spec is
	// isolated by a proxy network — one privileged (root) sidecar for both. The
	// net-redirect init (enforcing) / hostAliases (cooperative) are added here so
	// they precede the caretaker container appended just below.
	cctlCfg := caretaker.Config{Mounts: roles}

	// Credential roles: each client-sourced credential becomes one caretaker role
	// plus (per delivery) app-container env for an endpoint and/or a shared
	// emptyDir for a file. needNetAdmin is set when a delivery binds a well-known
	// address (e.g. 169.254.169.254), which needs NET_ADMIN.
	credNetAdmin := b.addCredentialRoles(ctx, creds, podSpec, &cctlCfg, &caretakerMounts)

	if spec.Proxy != nil {
		b.addProxyToMountCaretaker(spec, podSpec, &cctlCfg)
	}
	if b.dnsActive(ctx, spec) {
		cctlCfg.DNS = b.dnsRole(spec)
		b.setPodDNS(podSpec)
	}
	if spec.Hub != nil {
		// Fold the hub role into this same (privileged) caretaker, and its
		// synthetic-IP records into the DNS role (creating it if the spec has none)
		// so the app's dial of a peer name funnels through the hub.
		role, records := b.hubDiscovery(spec)
		cctlCfg.Hub = role
		if cctlCfg.DNS == nil {
			up := b.clusterDNSIP()
			if up != "" {
				up = fmt.Sprintf("%s:53", up)
			}
			cctlCfg.DNS = &caretaker.DNSRole{Domain: b.namespace + ".svc.cluster.local", Upstream: up}
		}
		if cctlCfg.DNS.Records == nil {
			cctlCfg.DNS.Records = map[string]string{}
		}
		for n, ip := range records {
			cctlCfg.DNS.Records[n] = ip
		}
		b.setPodDNS(podSpec)
	}
	if spec.Docker != nil {
		b.addDockerRole(spec, podSpec, &cctlCfg, &caretakerMounts)
	}
	b.addAgentForwardRole(spec, podSpec, &cctlCfg, &caretakerMounts)
	b.addTelemetryRole(spec, podSpec, &cctlCfg)
	egressUID := 0
	if egress != nil && egress.Spec != nil {
		if egress.AgentImage != "" {
			agentImage = egress.AgentImage
		}
		egressUID = b.addEgressToCaretaker(egress, agentImage, len(mounts) > 0, podSpec, &cctlCfg)
	}
	// Mounts need a privileged (root) sidecar for the kernel 9P mount; a
	// credential-only caretaker needs privilege only when a delivery binds a
	// well-known address (NET_ADMIN to add it to lo); a transparent-egress-only
	// caretaker runs as the dedicated uid the redirect exempts; and nothing
	// otherwise.
	var sc *corev1.SecurityContext
	switch {
	case len(mounts) > 0:
		sc = &corev1.SecurityContext{Privileged: ptr.To(true)}
	case credNetAdmin:
		sc = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}}
	case egressUID > 0:
		sc = &corev1.SecurityContext{RunAsUser: ptr.To(int64(egressUID))}
	}
	ctr := corev1.Container{
		Name:            "cornus-caretaker",
		Image:           agentImage,
		Command:         []string{"cornus"}, // pin the entrypoint; the sidecar image may not be a cornus image
		Args:            []string{"caretaker"},
		RestartPolicy:   &always, // native sidecar: starts before, runs alongside the app
		ImagePullPolicy: imagePullPolicy(),
		SecurityContext: sc,
		VolumeMounts:    caretakerMounts,
		// Gate the app container until every mount is live (self-contained check
		// via the cornus binary reading the same config env — no util-linux).
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
				Command: []string{"cornus", "caretaker-check"},
			}},
			PeriodSeconds:    1,
			FailureThreshold: 120,
		},
	}
	b.addCaretakerTLS(ctx, podSpec, &ctr, &cctlCfg) // before the config marshals into the env
	ctr.Env = b.caretakerConfigEnv(cctlCfg, spec.Name)
	podSpec.InitContainers = append(podSpec.InitContainers, ctr)
	return dep
}

// credEndpointBasePort is where loopback credential endpoints are bound inside
// the shared pod netns (well above the ephemeral range apps usually use).
const credEndpointBasePort = 19100

// credScratchBase is where the caretaker mounts each credential file's shared
// emptyDir; the app container sees the same volume at the file's directory.
const credScratchBase = "/cornus/creds"

// addCredentialRoles folds the AttachCredentials into the caretaker config: one
// CredentialRole per source, an app-container env var per endpoint delivery
// (resolved via the delivery provider), and a shared emptyDir per file delivery
// directory. It appends any caretaker volume mounts for files. It returns whether
// any delivery binds a well-known address (so the caller adds NET_ADMIN).
func (b *Backend) addCredentialRoles(ctx context.Context, creds []deploy.AttachCredential, podSpec *corev1.PodSpec, cfg *caretaker.Config, caretakerMounts *[]corev1.VolumeMount) bool {
	log := logging.FromContext(ctx, slog.String("component", "kubernetes"))
	needNetAdmin := false
	port := credEndpointBasePort
	vol := 0
	for _, c := range creds {
		role := caretaker.CredentialRole{Server: c.RelayURL, Session: c.Session, Name: c.Name, TTL: c.TTL}
		fileDirs := map[string]string{} // app dir -> caretaker scratch dir (per credential)
		for _, d := range c.Deliver {
			switch d.Kind {
			case "env":
				// Resolved server-side into EnvVars above; nothing runtime to serve.
			case "", "endpoint":
				var cfg map[string]string
				if d.Upstream != "" {
					cfg = map[string]string{"upstream": d.Upstream}
				}
				ep, err := creddelivery.Open(d.Provider, cfg)
				if err != nil {
					// Unknown provider: skip this delivery rather than fail the whole
					// deploy; the credential's other deliveries still land.
					log.WarnContext(ctx, "skipping credential endpoint", "name", c.Name, "provider", d.Provider, "error", err)
					continue
				}
				addr := ""
				wk := false
				if d.WellKnown && ep.WellKnownAddr() != "" {
					addr = ep.WellKnownAddr()
					wk = true
					needNetAdmin = true
				} else {
					addr = fmt.Sprintf("127.0.0.1:%d", port)
					port++
				}
				env := ep.Env(c.Name, addr)
				for _, k := range sortedEnvKeys(env) {
					podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, corev1.EnvVar{Name: k, Value: env[k]})
				}
				role.Deliver = append(role.Deliver, caretaker.CredentialDelivery{
					Kind: "endpoint", Provider: d.Provider, Addr: addr, WellKnown: wk, Upstream: d.Upstream,
				})
			case "file":
				appDir := filepath.Dir(d.Path)
				scratch, ok := fileDirs[appDir]
				if !ok {
					vname := fmt.Sprintf("cornus-cred-%d", vol)
					scratch = fmt.Sprintf("%s/%d", credScratchBase, vol)
					vol++
					podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
						Name:         vname,
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					})
					podSpec.Containers[0].VolumeMounts = append(podSpec.Containers[0].VolumeMounts, corev1.VolumeMount{
						Name: vname, MountPath: appDir, ReadOnly: true,
					})
					*caretakerMounts = append(*caretakerMounts, corev1.VolumeMount{Name: vname, MountPath: scratch})
					fileDirs[appDir] = scratch
				}
				role.Deliver = append(role.Deliver, caretaker.CredentialDelivery{
					Kind: "file", Path: scratch + "/" + filepath.Base(d.Path), Format: d.Format,
				})
			}
		}
		// A source with only env deliveries has no runtime relay — its value is
		// already in the Secret — so it needs no caretaker credential role.
		if len(role.Deliver) > 0 {
			cfg.Credentials = append(cfg.Credentials, role)
		}
	}
	return needNetAdmin
}

func sortedEnvKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// service builds a ClusterIP Service exposing the spec's container ports, or nil
// when the spec publishes none.
func (b *Backend) service(spec api.DeploySpec) *corev1.Service {
	if len(spec.Ports) == 0 {
		return nil
	}
	sel := labels(spec.Name)
	var ports []corev1.ServicePort
	for _, p := range spec.Ports {
		proto := protocol(p.Protocol)
		ports = append(ports, corev1.ServicePort{
			// Include the protocol so tcp+udp on the same container port yield
			// distinct, valid port names (Kubernetes rejects duplicate names in
			// a multi-port Service).
			Name:       fmt.Sprintf("p%d-%s", p.Container, strings.ToLower(string(proto))),
			Port:       int32(p.Container),
			TargetPort: intstr.FromInt32(int32(p.Container)),
			Protocol:   proto,
		})
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Labels: sel},
		Spec: corev1.ServiceSpec{
			Selector: sel,
			Ports:    ports,
			Type:     corev1.ServiceTypeClusterIP,
		},
	}
}

// ingressDefaults are the server-side fallbacks (ultimately Helm values) an operator
// configures once, so a deploy can enable ingress with no per-deployment host/class/
// issuer wiring. The client MAY override each default in its DeploySpec/compose; the
// server may pin the domain with enforceDomain so an override cannot escape it.
type ingressDefaults struct {
	domain        string // CORNUS_INGRESS_DOMAIN: base wildcard domain, e.g. "preview.example.com"
	className     string // CORNUS_INGRESS_CLASS: default IngressClassName
	tlsIssuer     string // CORNUS_INGRESS_TLS_ISSUER: default cert-manager cluster-issuer
	enforceDomain bool   // CORNUS_INGRESS_ENFORCE_DOMAIN: reject a resolved host outside `domain`
}

func ingressDefaultsFromEnv() ingressDefaults {
	return ingressDefaults{
		domain:        strings.TrimSpace(os.Getenv("CORNUS_INGRESS_DOMAIN")),
		className:     strings.TrimSpace(os.Getenv("CORNUS_INGRESS_CLASS")),
		tlsIssuer:     strings.TrimSpace(os.Getenv("CORNUS_INGRESS_TLS_ISSUER")),
		enforceDomain: envBool(os.Getenv("CORNUS_INGRESS_ENFORCE_DOMAIN")),
	}
}

// AdvertisedIngress reports the cluster's ingress front door for GET /.cornus/v1/info:
// the server's base domain and default class, plus the controller Service a native
// SOCKS5 passthrough tunnels to. The controller is taken from
// CORNUS_INGRESS_CONTROLLER when set, else discovered by well-known labels/names.
// It returns nil when there is nothing to advertise (no domain/class configured and
// no controller found), so the client falls back to client-side emulation.
// Best-effort: a discovery error yields a nil controller, never an error.
func (b *Backend) AdvertisedIngress(ctx context.Context) (*api.IngressInfo, error) {
	info := &api.IngressInfo{
		Domain:     b.ingressDefaults.domain,
		Class:      b.ingressDefaults.className,
		Controller: b.ingressController(ctx),
	}
	if info.Domain == "" && info.Class == "" && info.Controller == nil {
		return nil, nil
	}
	return info, nil
}

// ingressController resolves the ingress controller Service to tunnel to: the
// explicit CORNUS_INGRESS_CONTROLLER override (<ns>/<service>[:httpPort/httpsPort])
// wins, else it is discovered. Returns nil when none is found.
func (b *Backend) ingressController(ctx context.Context) *api.IngressController {
	if v := strings.TrimSpace(os.Getenv("CORNUS_INGRESS_CONTROLLER")); v != "" {
		return parseIngressController(v)
	}
	return b.discoverIngressController(ctx)
}

// wellKnownControllers are (namespace, service) pairs of common ingress controllers,
// tried by direct Get (RBAC-friendlier than a cluster-wide List) in order.
var wellKnownControllers = []struct{ namespace, service string }{
	{"ingress-nginx", "ingress-nginx-controller"},
	{"kube-system", "ingress-nginx-controller"},
	{"ingress-nginx", "ingress-nginx"},
	{"traefik", "traefik"},
	{"kube-system", "traefik"},
	{"projectcontour", "envoy"},
}

// discoverIngressController finds the cluster's ingress controller Service by trying
// each well-known (namespace, service) pair. Returns nil when none resolves.
func (b *Backend) discoverIngressController(ctx context.Context) *api.IngressController {
	for _, wk := range wellKnownControllers {
		svc, err := b.clientset.CoreV1().Services(wk.namespace).Get(ctx, wk.service, metav1.GetOptions{})
		if err != nil {
			continue
		}
		return controllerFromService(svc)
	}
	return nil
}

// controllerFromService extracts the ingress-controller target from a Service: its
// namespace/name and the ports named (or numbered) http/https, defaulting to 80/443.
func controllerFromService(svc *corev1.Service) *api.IngressController {
	return &api.IngressController{
		Namespace: svc.Namespace,
		Service:   svc.Name,
		HTTPPort:  servicePortByNameOrNumber(svc, "http", 80),
		HTTPSPort: servicePortByNameOrNumber(svc, "https", 443),
	}
}

// servicePortByNameOrNumber returns the Service port whose name is `name`, else the
// one whose number is `want`, else `want` itself (the conventional default).
func servicePortByNameOrNumber(svc *corev1.Service, name string, want int32) int {
	for _, p := range svc.Spec.Ports {
		if p.Name == name {
			return int(p.Port)
		}
	}
	for _, p := range svc.Spec.Ports {
		if p.Port == want {
			return int(p.Port)
		}
	}
	return int(want)
}

// parseIngressController parses a CORNUS_INGRESS_CONTROLLER value of the form
// <namespace>/<service>[:httpPort/httpsPort], defaulting the ports to 80/443.
// Returns nil when the namespace or service is missing.
func parseIngressController(v string) *api.IngressController {
	ns, rest, ok := strings.Cut(v, "/")
	if !ok || ns == "" || rest == "" {
		return nil
	}
	svc, ports, hasPorts := strings.Cut(rest, ":")
	if svc == "" {
		return nil
	}
	c := &api.IngressController{Namespace: ns, Service: svc, HTTPPort: 80, HTTPSPort: 443}
	if hasPorts {
		httpStr, httpsStr, _ := strings.Cut(ports, "/")
		if n, err := strconv.Atoi(strings.TrimSpace(httpStr)); err == nil && n > 0 && n <= 65535 {
			c.HTTPPort = n
		}
		if n, err := strconv.Atoi(strings.TrimSpace(httpsStr)); err == nil && n > 0 && n <= 65535 {
			c.HTTPSPort = n
		}
	}
	return c
}

// envBool reports whether v is a truthy env value (1/true/yes/on, case-insensitive).
func envBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// envFloat32 returns the float32 value of env var key, or def when unset/invalid.
func envFloat32(key string, def float32) float32 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil && f > 0 {
			return float32(f)
		}
	}
	return def
}

// envInt returns the int value of env var key, or def when unset/invalid.
func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ingressEnabled reports whether the spec asks for an Ingress. A non-empty host
// list implies enabled even if the bare Enabled flag was not set.
func ingressEnabled(in *api.IngressSpec) bool {
	// A client-emulated ingress is realized entirely client-side (a reverse proxy
	// reached through the conduit), so the backend creates NO cluster Ingress object
	// or managed TLS Secret for it — it reads as "no server ingress" here, which is
	// the single gate every server-side ingress-object path funnels through
	// (b.ingress, applyManagedIngressTLSSecrets). Without this, an emulate-mode
	// `compose up` fails at deploy trying to create an Ingress the deployment never
	// wanted (and that the server may lack RBAC for).
	return in != nil && !in.ClientEmulated && (in.Enabled || len(in.Hosts) > 0)
}

// canonicalIngressHost normalizes a DNS host to its canonical form: trimmed,
// lowercased, and without a trailing dot. DNS is case-insensitive, so the
// ingress rule hosts and the managed-certificate host set must agree on case;
// the client normalizes managed-certificate hosts the same way (see
// ingressemu.normalizeCertificateHost), so keying host lookups on this form
// keeps both sides comparable.
func canonicalIngressHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// ingress builds a networking.k8s.io/v1 Ingress fronting the spec's ClusterIP
// Service, or nil when the spec does not request one. It resolves the host
// (explicit or auto-derived from the server base domain), the target port, the
// ingress class, and optional cert-manager TLS from the spec and server defaults.
func (b *Backend) ingress(spec api.DeploySpec) (*networkingv1.Ingress, error) {
	in := spec.Ingress
	if !ingressEnabled(in) {
		return nil, nil
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if len(spec.Ports) == 0 {
		return nil, fmt.Errorf("ingress requires the deployment to publish at least one port")
	}

	// The base domain (client override, else server default) backs both host
	// auto-derivation and the "@" apex token.
	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		domain = b.ingressDefaults.domain
	}

	// Resolve hosts: explicit hosts win, with "@" mapping to the apex (the base
	// domain itself, no "<name>." prefix — DNS-zone convention). With no explicit
	// host, derive a single "<name>.<domain>". Fail clearly when a host is needed
	// but no base domain is available.
	var hosts []string
	for _, h := range in.Hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if h == "@" {
			if domain == "" {
				return nil, fmt.Errorf(`ingress: host "@" (apex) requires a base domain: set ingress.domain or configure CORNUS_INGRESS_DOMAIN on the server`)
			}
			hosts = append(hosts, canonicalIngressHost(domain))
			continue
		}
		hosts = append(hosts, canonicalIngressHost(h))
	}
	if len(hosts) == 0 {
		if domain == "" {
			return nil, fmt.Errorf("ingress requires a host: set ingress.hosts / ingress.domain, or configure CORNUS_INGRESS_DOMAIN on the server")
		}
		// The subdomain the client supplies (compose sets "<service>.<project>" so
		// projects do not collide on a flat name) is prefixed to the base domain;
		// with none, fall back to the deployment name. Sanitize per label so raw
		// compose service/project names (underscores, mixed case) become DNS-safe.
		sub := strings.TrimSpace(in.Subdomain)
		if sub == "" {
			sub = spec.Name
		}
		sub = sanitizeSubdomain(sub)
		if sub == "" {
			return nil, fmt.Errorf("ingress: cannot derive a host label from subdomain/name %q", spec.Name)
		}
		hosts = []string{canonicalIngressHost(sub + "." + domain)}
	}

	// Domain-enforcement policy: when the server pins its domain, every resolved host
	// (explicit or derived) must stay within it, so a shared ingress controller cannot
	// be made to serve an arbitrary hostname on the client's say-so.
	if b.ingressDefaults.enforceDomain && b.ingressDefaults.domain != "" {
		d := canonicalIngressHost(b.ingressDefaults.domain)
		for _, host := range hosts {
			if host != d && !strings.HasSuffix(host, "."+d) {
				return nil, fmt.Errorf("ingress: host %q violates the server ingress-domain policy (must be within %q)", host, d)
			}
		}
	}

	// Resolve target port: explicit must match a published port; otherwise use the
	// first published container port.
	targetPort := int32(spec.Ports[0].Container)
	if in.Port != 0 {
		found := false
		for _, p := range spec.Ports {
			if p.Container == in.Port {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("ingress: port %d is not among the deployment's published container ports", in.Port)
		}
		targetPort = int32(in.Port)
	}

	path := in.Path
	if path == "" {
		path = "/"
	}
	pathType := networkingv1.PathType(in.PathType)
	if in.PathType == "" {
		pathType = networkingv1.PathTypePrefix
	}

	annotations := map[string]string{}
	for k, v := range in.Annotations {
		annotations[k] = v
	}

	// The path/backend rule value is identical for every host, so build it once and
	// attach one rule per host.
	ruleValue := networkingv1.IngressRuleValue{
		HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{
				Path:     path,
				PathType: &pathType,
				Backend: networkingv1.IngressBackend{
					Service: &networkingv1.IngressServiceBackend{
						Name: spec.Name,
						Port: networkingv1.ServiceBackendPort{Number: targetPort},
					},
				},
			}},
		},
	}
	rules := make([]networkingv1.IngressRule, 0, len(hosts))
	for _, host := range hosts {
		rules = append(rules, networkingv1.IngressRule{Host: host, IngressRuleValue: ruleValue})
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Labels: labels(spec.Name)},
		Spec:       networkingv1.IngressSpec{Rules: rules},
	}

	// Ingress class: explicit, else server default, else leave nil (cluster default).
	if cls := in.ClassName; cls != "" {
		ing.Spec.IngressClassName = ptr.To(cls)
	} else if b.ingressDefaults.className != "" {
		ing.Spec.IngressClassName = ptr.To(b.ingressDefaults.className)
	}

	// TLS: managed certificate groups become distinct Ingress TLS entries. Any
	// hosts not covered by them retain the legacy Secret/cert-manager fallback.
	if in.TLS != nil {
		resolved := make(map[string]bool, len(hosts))
		for _, host := range hosts {
			resolved[host] = true
		}
		covered := make(map[string]bool, len(hosts))
		for _, cert := range in.TLS.ManagedCertificates {
			secretName := strings.TrimSpace(cert.SecretName)
			if secretName == "" {
				return nil, fmt.Errorf("ingress: managed TLS certificate requires a secretName")
			}
			if len(cert.Hosts) == 0 {
				return nil, fmt.Errorf("ingress: managed TLS secret %q requires at least one host", secretName)
			}
			entryHosts := make([]string, 0, len(cert.Hosts))
			for _, host := range cert.Hosts {
				host = canonicalIngressHost(host)
				if !resolved[host] {
					return nil, fmt.Errorf("ingress: managed TLS secret %q refers to host %q which is not an ingress host", secretName, host)
				}
				if covered[host] {
					return nil, fmt.Errorf("ingress: host %q is assigned to more than one managed TLS secret", host)
				}
				covered[host] = true
				entryHosts = append(entryHosts, host)
			}
			ing.Spec.TLS = append(ing.Spec.TLS, networkingv1.IngressTLS{Hosts: entryHosts, SecretName: secretName})
		}

		uncovered := make([]string, 0, len(hosts))
		for _, host := range hosts {
			if !covered[host] {
				uncovered = append(uncovered, host)
			}
		}
		secret := strings.TrimSpace(in.TLS.SecretName)
		issuer := strings.TrimSpace(in.TLS.ClusterIssuer)
		if issuer == "" {
			issuer = b.ingressDefaults.tlsIssuer
		}
		if len(uncovered) > 0 && secret == "" && issuer == "" {
			return nil, fmt.Errorf("ingress: tls requires a secretName or a cluster-issuer (set ingress.tls.clusterIssuer or CORNUS_INGRESS_TLS_ISSUER)")
		}
		if len(uncovered) > 0 && secret == "" {
			secret = spec.Name + "-tls"
		}
		if len(uncovered) > 0 && issuer != "" {
			annotations["cert-manager.io/cluster-issuer"] = issuer
		}
		if len(uncovered) > 0 {
			ing.Spec.TLS = append(ing.Spec.TLS, networkingv1.IngressTLS{Hosts: uncovered, SecretName: secret})
		}
	}

	if len(annotations) > 0 {
		ing.ObjectMeta.Annotations = annotations
	}
	return ing, nil
}

// imagePullPolicy honors CORNUS_K8S_IMAGE_PULL_POLICY (Always / IfNotPresent /
// Never); empty lets Kubernetes choose its default. The E2E kind target sets
// IfNotPresent so preloaded ("kind load") images are used without a registry pull.
func imagePullPolicy() corev1.PullPolicy {
	switch os.Getenv("CORNUS_K8S_IMAGE_PULL_POLICY") {
	case "Always":
		return corev1.PullAlways
	case "IfNotPresent":
		return corev1.PullIfNotPresent
	case "Never":
		return corev1.PullNever
	default:
		return ""
	}
}

func protocol(p string) corev1.Protocol {
	if p == "udp" {
		return corev1.ProtocolUDP
	}
	return corev1.ProtocolTCP
}

// statusOf maps a Deployment (and, when available, its pods) to a DeployStatus,
// reporting ready replicas as running instances. Instance count and running-ness
// come from the Deployment's replica counters; Health and ExitCode are enriched
// from the pods' app-container statuses.
//
// Kubernetes has no Docker-style health engine, so health == container
// readiness: an instance whose app container declares a readiness or liveness
// probe reports "healthy" once ready and "starting" until then; with no probe
// there is no meaningful health signal, so Health is "" (unknown). ExitCode is
// filled from a terminated app container's exit status.
func statusOf(dep *appsv1.Deployment, pods []corev1.Pod, backend string) api.DeployStatus {
	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	ready := dep.Status.ReadyReplicas
	hasProbe := appHasProbe(dep)
	// Deterministic pod order so instance i maps to the same pod across polls.
	sorted := append([]corev1.Pod(nil), pods...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	st := api.DeployStatus{
		Name:    dep.Name,
		Image:   imageOf(dep),
		Backend: backend,
		Origin:  deploy.OriginFromLabels(dep.Annotations),
	}
	for i := int32(0); i < desired; i++ {
		inst := api.InstanceStatus{
			ID:      fmt.Sprintf("%s-%d", dep.Name, i),
			State:   readyState(i < ready),
			Running: i < ready,
			Health:  healthFromReady(hasProbe, i < ready),
		}
		// Instances are synthesized from the Deployment's replica counters, so we
		// zip them to pods in name order; for the common single-replica case this
		// is exact, and for scaled deployments it is a best-effort association.
		//
		// Only surface an ExitCode for an instance that is NOT running (health ==
		// readiness, so a ready/"healthy" instance is by definition not terminated).
		// Reporting both a healthy state and an exit code at once is incoherent — the
		// readiness-count Running/Health and the name-sorted-pod terminated status
		// need not describe the same replica — so the exit code is suppressed while
		// the instance reads as running.
		if !inst.Running && int(i) < len(sorted) {
			if cs := appContainerStatus(&sorted[i]); cs != nil && cs.State.Terminated != nil {
				ec := int(cs.State.Terminated.ExitCode)
				inst.ExitCode = &ec
			}
			// Surface why a non-running instance is stuck (a crash-looping sidecar
			// or app container, an image-pull failure, an unschedulable pod) so the
			// deploy-attach readiness wait can report it instead of hanging.
			inst.Message = instanceDiagnostic(&sorted[i])
		}
		st.Instances = append(st.Instances, inst)
	}
	return st
}

// appHasProbe reports whether the Deployment's app container declares a
// readiness or liveness probe -- the signal that gives container readiness a
// Docker-style health meaning.
func appHasProbe(dep *appsv1.Deployment) bool {
	cs := dep.Spec.Template.Spec.Containers
	for i := range cs {
		if cs[i].Name == execContainer {
			return cs[i].ReadinessProbe != nil || cs[i].LivenessProbe != nil
		}
	}
	if len(cs) > 0 {
		return cs[0].ReadinessProbe != nil || cs[0].LivenessProbe != nil
	}
	return false
}

// appContainerStatus returns a pod's app-container status (by the well-known
// container name, else the first), or nil when the pod has reported none yet.
func appContainerStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == execContainer {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		return &pod.Status.ContainerStatuses[0]
	}
	return nil
}

// wedgedWaitReasons are container Waiting reasons that will not clear on their
// own — a bad image reference, a config error, or a crash loop. A pod stuck on
// one of these never becomes ready, so the deploy-attach wait surfaces it to the
// caller rather than waiting out the full timeout in silence. Benign startup
// reasons (ContainerCreating, PodInitializing) are intentionally excluded.
var wedgedWaitReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
}

// instanceDiagnostic returns a short human-readable reason a pod is not running,
// or "" when nothing notable is reported. It inspects the init/sidecar containers
// first (a crash-looping caretaker sidecar gates the app container, which then
// only shows a benign PodInitializing), then the app containers, then pod-level
// scheduling failures. Each container status is prefixed with its name so the
// caller can tell an app crash from a sidecar crash.
func instanceDiagnostic(pod *corev1.Pod) string {
	scan := func(css []corev1.ContainerStatus) string {
		for i := range css {
			cs := &css[i]
			if w := cs.State.Waiting; w != nil && wedgedWaitReasons[w.Reason] {
				return containerDiagLine(cs.Name, w.Reason, w.Message)
			}
			if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
				reason := t.Reason
				if reason == "" {
					reason = fmt.Sprintf("exit %d", t.ExitCode)
				}
				return containerDiagLine(cs.Name, reason, t.Message)
			}
		}
		return ""
	}
	// Native sidecars are init containers; a crash there is the usual culprit.
	if msg := scan(pod.Status.InitContainerStatuses); msg != "" {
		return msg
	}
	if msg := scan(pod.Status.ContainerStatuses); msg != "" {
		return msg
	}
	// No container-level signal: report an unschedulable pod (PodScheduled=False).
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
			return containerDiagLine("pod", c.Reason, c.Message)
		}
	}
	return ""
}

// containerDiagLine formats a "name: reason: message" diagnostic, trimming the
// trailing detail when the container reported no message.
func containerDiagLine(name, reason, message string) string {
	if message = strings.TrimSpace(message); message != "" {
		return fmt.Sprintf("%s: %s: %s", name, reason, message)
	}
	return fmt.Sprintf("%s: %s", name, reason)
}

// healthFromReady maps container readiness to Docker's health vocabulary. Only
// meaningful when a probe is defined; without one, health is unknown ("").
func healthFromReady(hasProbe, ready bool) string {
	if !hasProbe {
		return ""
	}
	if ready {
		return "healthy"
	}
	return "starting"
}

func imageOf(dep *appsv1.Deployment) string {
	if cs := dep.Spec.Template.Spec.Containers; len(cs) > 0 {
		return cs[0].Image
	}
	return ""
}

func readyState(ready bool) string {
	if ready {
		return "running"
	}
	return "pending"
}
