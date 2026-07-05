// Package knative loads a Knative Serving Service manifest (a "ksvc",
// serving.knative.dev/v1 Kind: Service) as a cornus deployment descriptor,
// translating it into the internal api.DeploySpec — a first-class input format
// alongside the native spec, docker-compose, and devcontainers. The resulting
// DeploySpec carries a KnativeSpec block, so the kubernetes backend can
// round-trip it back into a native ksvc on a Knative-enabled cluster while every
// other backend runs it as an ordinary container (see pkg/deploy/kubernetes and
// api.KnativeSpec). Only the Serving Service Kind is handled; Eventing and
// multi-revision traffic splitting are out of scope.
package knative

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"cornus/pkg/api"
)

// autoscalingPrefix is the annotation-key prefix Knative uses for autoscaler
// knobs on the revision template.
const autoscalingPrefix = "autoscaling.knative.dev/"

// Detect reports whether data looks like a Knative Serving Service manifest, so
// `cornus deploy -f` can route it to Load instead of parsing it as a native
// DeploySpec. It sniffs only apiVersion/kind, so a malformed body still reaches
// Load (which reports the real error).
func Detect(data []byte) bool {
	var head struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return false
	}
	return strings.HasPrefix(head.APIVersion, "serving.knative.dev/") && head.Kind == "Service"
}

// --- Minimal ksvc shape (only the fields v1 translates; unknown keys ignored) ---

type ksvcManifest struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   objectMeta `json:"metadata"`
	Spec       ksvcSpec   `json:"spec"`
}

type objectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

type ksvcSpec struct {
	Template revisionTemplate `json:"template"`
	Traffic  []trafficTarget  `json:"traffic"`
}

type trafficTarget struct {
	RevisionName   string `json:"revisionName"`
	Tag            string `json:"tag"`
	Percent        *int   `json:"percent"`
	LatestRevision *bool  `json:"latestRevision"`
}

type revisionTemplate struct {
	Metadata objectMeta   `json:"metadata"`
	Spec     revisionSpec `json:"spec"`
}

type revisionSpec struct {
	ContainerConcurrency *int        `json:"containerConcurrency"`
	TimeoutSeconds       *int        `json:"timeoutSeconds"`
	Containers           []container `json:"containers"`
	Volumes              []anyObject `json:"volumes"`
}

type container struct {
	Name           string          `json:"name"`
	Image          string          `json:"image"`
	Command        []string        `json:"command"`
	Args           []string        `json:"args"`
	WorkingDir     string          `json:"workingDir"`
	Env            []envVar        `json:"env"`
	Ports          []containerPort `json:"ports"`
	Resources      *resourceReq    `json:"resources"`
	LivenessProbe  *probe          `json:"livenessProbe"`
	ReadinessProbe *probe          `json:"readinessProbe"`
	VolumeMounts   []anyObject     `json:"volumeMounts"`
}

type envVar struct {
	Name      string     `json:"name"`
	Value     string     `json:"value"`
	ValueFrom *anyObject `json:"valueFrom"`
}

type containerPort struct {
	Name          string `json:"name"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type resourceReq struct {
	Limits   map[string]string `json:"limits"`
	Requests map[string]string `json:"requests"`
}

type probe struct {
	Exec                *execAction `json:"exec"`
	HTTPGet             *anyObject  `json:"httpGet"`
	TCPSocket           *anyObject  `json:"tcpSocket"`
	GRPC                *anyObject  `json:"grpc"`
	InitialDelaySeconds int         `json:"initialDelaySeconds"`
	PeriodSeconds       int         `json:"periodSeconds"`
	TimeoutSeconds      int         `json:"timeoutSeconds"`
	FailureThreshold    int         `json:"failureThreshold"`
}

type execAction struct {
	Command []string `json:"command"`
}

// anyObject captures a sub-tree we only test for presence (to warn on an
// unsupported construct) without modelling its shape.
type anyObject map[string]any

// Load parses a single-document Knative Serving Service manifest and translates
// it into a DeploySpec with a populated Knative block (Enabled=true). The
// returned warnings describe constructs that were dropped or approximated
// (non-value env, non-exec probes, extra ports/containers, traffic splitting);
// the caller surfaces them to the user. An error means the manifest is
// unusable — bad YAML, wrong Kind, no container, or no image.
func Load(data []byte) (api.DeploySpec, []string, error) {
	var m ksvcManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return api.DeploySpec{}, nil, fmt.Errorf("knative: parsing manifest: %w", err)
	}
	if !strings.HasPrefix(m.APIVersion, "serving.knative.dev/") || m.Kind != "Service" {
		return api.DeploySpec{}, nil, fmt.Errorf("knative: not a serving.knative.dev Service (apiVersion %q, kind %q)", m.APIVersion, m.Kind)
	}
	if m.Metadata.Name == "" {
		return api.DeploySpec{}, nil, fmt.Errorf("knative: metadata.name is required")
	}
	containers := m.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return api.DeploySpec{}, nil, fmt.Errorf("knative: spec.template.spec.containers is empty")
	}
	if len(containers) > 1 {
		return api.DeploySpec{}, nil, fmt.Errorf("knative: multi-container Services are not supported yet (found %d containers)", len(containers))
	}
	c := containers[0]
	if c.Image == "" {
		return api.DeploySpec{}, nil, fmt.Errorf("knative: container image is required")
	}

	var warnings []string
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	spec := api.DeploySpec{
		Name:       m.Metadata.Name,
		Image:      c.Image,
		WorkingDir: c.WorkingDir,
		// A ksvc container's command is the entrypoint override and args are its
		// arguments — the same split DeploySpec draws between Entrypoint and
		// Command, so the kubernetes backend round-trips them byte-for-byte.
		Entrypoint: c.Command,
		Command:    c.Args,
	}

	// Env: only the literal value form survives; valueFrom (secretKeyRef,
	// fieldRef, ...) cannot be expressed as a DeploySpec env value.
	if len(c.Env) > 0 {
		spec.Env = map[string]string{}
		for _, e := range c.Env {
			if e.ValueFrom != nil {
				warn("env %q uses valueFrom, which is not translated; set it another way", e.Name)
				continue
			}
			spec.Env[e.Name] = e.Value
		}
		if len(spec.Env) == 0 {
			spec.Env = nil
		}
	}

	// Volumes / volumeMounts have no portable DeploySpec analogue for a serverless
	// workload in v1 and are dropped.
	if len(m.Spec.Template.Spec.Volumes) > 0 || len(c.VolumeMounts) > 0 {
		warn("volumes/volumeMounts are not supported with Knative yet and were dropped")
	}

	kn := &api.KnativeSpec{Enabled: true}

	// Ports: a Knative revision exposes exactly one port. Take the first and warn
	// on extras; record the routed port in KnativeSpec.Port too.
	if len(c.Ports) > 0 {
		if len(c.Ports) > 1 {
			warn("a Knative revision exposes a single port; using containerPort %d and dropping %d other(s)", c.Ports[0].ContainerPort, len(c.Ports)-1)
		}
		p := c.Ports[0]
		if p.ContainerPort > 0 {
			spec.Ports = []api.PortMapping{{Container: p.ContainerPort, Protocol: strings.ToLower(p.Protocol)}}
			kn.Port = p.ContainerPort
		}
	}

	// Resources: convert k8s quantities to the DeploySpec's core/byte scalars.
	if c.Resources != nil {
		res, rwarn := translateResources(c.Resources)
		spec.Resources = res
		warnings = append(warnings, rwarn...)
	}

	// Probes: only an exec probe maps onto the Docker-style Healthcheck; Knative's
	// default httpGet/tcpSocket readiness probe is left to Knative itself.
	if hc, pwarn := translateProbe(c.ReadinessProbe, c.LivenessProbe); hc != nil {
		spec.Healthcheck = hc
	} else if pwarn != "" {
		warn("%s", pwarn)
	}

	// Concurrency / request timeout.
	kn.Concurrency = m.Spec.Template.Spec.ContainerConcurrency
	kn.TimeoutSeconds = m.Spec.Template.Spec.TimeoutSeconds

	// Autoscaling annotations on the revision template.
	translateAutoscaling(m.Spec.Template.Metadata.Annotations, kn, warn)

	// Traffic splitting across named revisions/tags is out of scope; we always
	// deploy the latest revision.
	if hasExplicitTraffic(m.Spec.Traffic) {
		warn("spec.traffic (named revisions / tags / percentage splitting) is not supported yet; deploying the latest revision only")
	}

	spec.Knative = kn
	if err := kn.Validate(); err != nil {
		return api.DeploySpec{}, warnings, err
	}
	return spec, warnings, nil
}

// translateResources maps a Knative container's ResourceRequirements onto the
// DeploySpec's api.Resources (CPU in cores, memory in bytes). Unparseable or
// non-cpu/memory resources are warned about and skipped.
func translateResources(rr *resourceReq) (*api.Resources, []string) {
	var warnings []string
	res := &api.Resources{}
	set := false
	for kind, q := range rr.Limits {
		switch kind {
		case "cpu":
			if cores, err := cpuCores(q); err != nil {
				warnings = append(warnings, fmt.Sprintf("resources.limits.cpu %q: %v; ignored", q, err))
			} else {
				res.CPULimit, set = cores, true
			}
		case "memory":
			if b, err := memBytes(q); err != nil {
				warnings = append(warnings, fmt.Sprintf("resources.limits.memory %q: %v; ignored", q, err))
			} else {
				res.MemoryLimit, set = b, true
			}
		default:
			warnings = append(warnings, fmt.Sprintf("resources.limits.%s is not translated", kind))
		}
	}
	for kind, q := range rr.Requests {
		switch kind {
		case "cpu":
			if cores, err := cpuCores(q); err != nil {
				warnings = append(warnings, fmt.Sprintf("resources.requests.cpu %q: %v; ignored", q, err))
			} else {
				res.ReservedCPU, set = cores, true
			}
		case "memory":
			if b, err := memBytes(q); err != nil {
				warnings = append(warnings, fmt.Sprintf("resources.requests.memory %q: %v; ignored", q, err))
			} else {
				res.ReservedMemory, set = b, true
			}
		default:
			warnings = append(warnings, fmt.Sprintf("resources.requests.%s is not translated", kind))
		}
	}
	if !set {
		return nil, warnings
	}
	return res, warnings
}

// cpuCores parses a Kubernetes CPU quantity (e.g. "500m", "1", "1.5") into a
// fractional core count.
func cpuCores(q string) (float64, error) {
	qty, err := resource.ParseQuantity(q)
	if err != nil {
		return 0, err
	}
	return float64(qty.MilliValue()) / 1000.0, nil
}

// memBytes parses a Kubernetes memory quantity (e.g. "512Mi", "1Gi") into bytes.
func memBytes(q string) (int64, error) {
	qty, err := resource.ParseQuantity(q)
	if err != nil {
		return 0, err
	}
	return qty.Value(), nil
}

// translateProbe maps a Knative exec probe (preferring readiness, then liveness)
// onto a Docker-style Healthcheck. It returns (nil, warning) when the only probes
// present are non-exec (httpGet/tcpSocket/grpc), which cannot be a Docker exec
// command, and (nil, "") when there are no probes at all.
func translateProbe(readiness, liveness *probe) (*api.Healthcheck, string) {
	for _, p := range []*probe{readiness, liveness} {
		if p == nil {
			continue
		}
		if p.Exec != nil && len(p.Exec.Command) > 0 {
			hc := &api.Healthcheck{Test: append([]string{"CMD"}, p.Exec.Command...)}
			if p.PeriodSeconds > 0 {
				hc.Interval = strconv.Itoa(p.PeriodSeconds) + "s"
			}
			if p.TimeoutSeconds > 0 {
				hc.Timeout = strconv.Itoa(p.TimeoutSeconds) + "s"
			}
			if p.InitialDelaySeconds > 0 {
				hc.StartPeriod = strconv.Itoa(p.InitialDelaySeconds) + "s"
			}
			if p.FailureThreshold > 0 {
				hc.Retries = p.FailureThreshold
			}
			return hc, ""
		}
	}
	if readiness != nil || liveness != nil {
		return nil, "non-exec probes (httpGet/tcpSocket/grpc) are not translated; Knative applies its default readiness probe"
	}
	return nil, ""
}

// translateAutoscaling reads the autoscaling.knative.dev/* annotations off the
// revision template into KnativeSpec fields, forwarding every other annotation
// (including unrecognised autoscaling knobs) verbatim so a round-trip preserves
// them.
func translateAutoscaling(annots map[string]string, kn *api.KnativeSpec, warn func(string, ...any)) {
	for k, v := range annots {
		switch strings.TrimPrefix(k, autoscalingPrefix) {
		case "minScale":
			kn.MinScale = parseIntPtr(v, "minScale", warn)
		case "maxScale":
			kn.MaxScale = parseIntPtr(v, "maxScale", warn)
		case "target":
			kn.Target = parseIntPtr(v, "target", warn)
		case "class":
			kn.Class = normalizeClass(v)
		case "metric":
			kn.Metric = v
		default:
			if kn.Annotations == nil {
				kn.Annotations = map[string]string{}
			}
			kn.Annotations[k] = v
		}
	}
}

// parseIntPtr parses a decimal annotation value into an *int, warning and
// yielding nil on garbage.
func parseIntPtr(v, field string, warn func(string, ...any)) *int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		warn("autoscaling %s %q is not an integer; ignored", field, v)
		return nil
	}
	return &n
}

// normalizeClass reduces a Knative class annotation ("kpa.autoscaling.knative.dev"
// / "hpa.autoscaling.knative.dev", or a bare "kpa"/"hpa") to the short word the
// KnativeSpec stores.
func normalizeClass(v string) string {
	switch {
	case strings.HasPrefix(v, "hpa"):
		return "hpa"
	case strings.HasPrefix(v, "kpa"):
		return "kpa"
	default:
		return v
	}
}

// hasExplicitTraffic reports whether the manifest pins traffic to named
// revisions or tags (anything beyond the implicit all-to-latest default).
func hasExplicitTraffic(traffic []trafficTarget) bool {
	for _, t := range traffic {
		if t.RevisionName != "" || t.Tag != "" {
			return true
		}
	}
	return false
}
