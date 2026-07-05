package netdriver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"cornus/pkg/deploy"
)

// Multus pod annotations: networksAnnotation lists overlaid SECONDARY
// attachments (comma-separated NAD names); defaultNetworkAnnotation replaces
// the pod's primary (cluster-CNI) interface with the named NAD — the DETACHED
// mode. See the plan's detached-mode caveats: off the cluster net, CoreDNS and
// the 9P mount relay may be unreachable.
const (
	networksAnnotation       = "k8s.v1.cni.cncf.io/networks"
	defaultNetworkAnnotation = "v1.multus-cni.io/default-network"
)

// multusProvider realises a network as a real CNI attachment via Multus: one
// shared NetworkAttachmentDefinition per network (its spec.config is the CNI
// JSON for the chosen plugin, parameterized by the attachment's DriverOpts),
// plus the pod annotation that makes Multus wire the interface.
//
// Recognised DriverOpts (all optional unless noted):
//
//	subnet  host-local IPAM subnet (default: a /24 derived from the network
//	        name in 10.222.0.0/16 — deterministic, override on collision)
//	bridge  bridge plugin: the Linux bridge device name (default derived,
//	        <=15 chars)
//	master  ipvlan/macvlan: the parent interface (REQUIRED for those plugins)
//	mode    ipvlan/macvlan mode (plugin default when empty)
//	mtu     interface MTU
//	ipmasq  bridge plugin: "true" to masquerade egress off the bridge
//	ipam    only "host-local" (default) is supported; "dhcp" needs a DHCP
//	        server cornus cannot assume and is rejected
type multusProvider struct {
	plugin string // "bridge", "ipvlan", or "macvlan"
}

func (m multusProvider) Name() string                              { return "multus-" + m.plugin }
func (multusProvider) Requires() []Capability                      { return []Capability{CapMultus} }
func (multusProvider) WorkloadScoped(Attachment) ([]Object, error) { return nil, nil }

func (m multusProvider) NetworkScoped(a Attachment) ([]Object, error) {
	config, err := m.cniConfig(a)
	if err != nil {
		return nil, err
	}
	nad := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "k8s.cni.cncf.io/v1",
		"kind":       "NetworkAttachmentDefinition",
		"metadata": map[string]any{
			"name":   a.NetLabel,
			"labels": map[string]any{deploy.LabelManaged: "true"},
		},
		"spec": map[string]any{"config": config},
	}}
	return []Object{{Unstructured: nad, GVR: nadGVR, Shared: true}}, nil
}

func (m multusProvider) MutatePod(a Attachment, tmpl *corev1.PodTemplateSpec) error {
	if tmpl.Annotations == nil {
		tmpl.Annotations = map[string]string{}
	}
	if a.Net.Default {
		// Detached: this network becomes the pod's primary interface. At most
		// one attachment may claim that. A pinned IP is not honoured here: the
		// default-network annotation is name-only, and detached addressing
		// stays with the NAD's own IPAM. The NAD reference MUST be namespace-
		// qualified: Multus resolves an unqualified default-network annotation
		// in its own configured default namespace (kube-system), not the
		// pod's, and the NAD lives in the workload namespace.
		qualified := a.NetLabel
		if a.Namespace != "" {
			qualified = a.Namespace + "/" + a.NetLabel
		}
		if prev, ok := tmpl.Annotations[defaultNetworkAnnotation]; ok && prev != qualified {
			return fmt.Errorf("multus: networks %s and %s both set default=true; a pod has one primary network", prev, qualified)
		}
		tmpl.Annotations[defaultNetworkAnnotation] = qualified
		return nil
	}
	// Overlaid: accumulate onto the secondary-attachments list, carrying the
	// pinned static address (if any) for Multus to hand the static IPAM plugin.
	merged, err := appendNetworkSelection(tmpl.Annotations[networksAnnotation], networkSelection{Name: a.NetLabel, IPs: pinnedIPs(a)})
	if err != nil {
		return err
	}
	tmpl.Annotations[networksAnnotation] = merged
	return nil
}

// pinnedIPs returns the attachment's pinned addresses for the network-selection
// annotation (empty when the network is dynamically addressed).
func pinnedIPs(a Attachment) []string {
	if a.Net.IP == "" {
		return nil
	}
	return []string{a.Net.IP}
}

// networkSelection is one element of the k8s.v1.cni.cncf.io/networks annotation
// in its JSON form (the form required to carry per-attachment runtime args like
// pinned IPs). Name is the NAD name; IPs are CIDR addresses the `static` IPAM
// plugin assigns (the NAD must declare the `ips` capability — cniConfig does).
type networkSelection struct {
	Name string   `json:"name"`
	IPs  []string `json:"ips,omitempty"`
}

// appendNetworkSelection accumulates one attachment onto the pod's
// network-selection annotation. Multus accepts either a comma-separated name
// list or a JSON array; per-attachment IPs exist only in the JSON form. To keep
// the no-pinned-IP path byte-identical to what it always was, the result stays
// a comma list until some element carries IPs, at which point the whole
// annotation is (re-)rendered as JSON — mixing forms is not allowed.
func appendNetworkSelection(existing string, add networkSelection) (string, error) {
	var sels []networkSelection
	switch {
	case strings.TrimSpace(existing) == "":
	case strings.HasPrefix(strings.TrimSpace(existing), "["):
		if err := json.Unmarshal([]byte(existing), &sels); err != nil {
			return "", fmt.Errorf("multus: cannot extend networks annotation %q: %w", existing, err)
		}
	default:
		for _, name := range strings.Split(existing, ",") {
			sels = append(sels, networkSelection{Name: strings.TrimSpace(name)})
		}
	}
	sels = append(sels, add)

	plain := true
	for _, s := range sels {
		if len(s.IPs) > 0 {
			plain = false
			break
		}
	}
	if plain {
		names := make([]string, len(sels))
		for i, s := range sels {
			names[i] = s.Name
		}
		return strings.Join(names, ","), nil
	}
	b, err := json.Marshal(sels)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cniConfig renders the NAD's CNI JSON for the plugin.
func (m multusProvider) cniConfig(a Attachment) (string, error) {
	opts := a.Net.DriverOpts
	if ipam := opts["ipam"]; ipam != "" && ipam != "host-local" {
		return "", fmt.Errorf("multus: ipam %q is not supported (only host-local; dhcp needs a DHCP server)", ipam)
	}
	// Subnet source precedence: an explicit `driver_opts.subnet` wins, then the
	// compose `ipam.config[0].subnet` (a.Net.Subnet), then a deterministic derived
	// /24. The compose ipam gateway/ip_range have no host-local IPAM knob the
	// generated conflist can express faithfully and are not wired (dockerhost is
	// the backend that realises them). enable_ipv6/internal/attachable are Docker
	// network concepts with no Multus equivalent and are ignored here.
	subnet := opts["subnet"]
	if subnet == "" {
		subnet = a.Net.Subnet
	}
	conf := map[string]any{
		"cniVersion": "0.3.1",
		"name":       a.NetLabel,
		"type":       m.plugin,
		"ipam": map[string]any{
			"type":   "host-local",
			"subnet": subnetFor(a.NetLabel, subnet),
		},
	}
	if a.Net.IP != "" && !a.Net.Default {
		// Pinned plan-time addressing (compose user networks, matrix row A'):
		// the `static` IPAM plugin assigns the addresses Multus forwards from
		// the pod's network-selection annotation, which requires the config to
		// declare the `ips` capability. The NAD is shared per network and the
		// compose planner pins EVERY member (all-or-none per network); a member
		// deployed without a pinned IP against a static NAD would fail its CNI
		// ADD.
		conf["ipam"] = map[string]any{"type": "static"}
		conf["capabilities"] = map[string]any{"ips": true}
	}
	if v := opts["mtu"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return "", fmt.Errorf("multus: bad mtu %q: %w", v, err)
		}
		conf["mtu"] = n
	}
	switch m.plugin {
	case "bridge":
		conf["bridge"] = bridgeNameFor(a.NetLabel, opts["bridge"])
		conf["isGateway"] = true
		if opts["ipmasq"] == "true" {
			conf["ipMasq"] = true
		}
	case "ipvlan", "macvlan":
		master := opts["master"]
		if master == "" {
			return "", fmt.Errorf("multus: %s needs driver_opts master (the parent interface)", m.plugin)
		}
		conf["master"] = master
		if mode := opts["mode"]; mode != "" {
			conf["mode"] = mode
		}
	}
	b, err := json.Marshal(conf)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// subnetFor picks the host-local subnet: the explicit driver_opts value, or a
// deterministic /24 inside 10.222.0.0/16 derived from the network identity so
// distinct networks land on distinct segments without configuration. Two
// networks CAN collide on the derived octet (1/256); override via
// driver_opts subnet when they do.
func subnetFor(netLabel, explicit string) string {
	if explicit != "" {
		return explicit
	}
	sum := sha256.Sum256([]byte(netLabel))
	return fmt.Sprintf("10.222.%d.0/24", sum[0])
}

// bridgeNameFor picks the Linux bridge device name: explicit, or "chim" plus a
// hash — Linux interface names cap at 15 characters.
func bridgeNameFor(netLabel, explicit string) string {
	if explicit != "" {
		return explicit
	}
	sum := sha256.Sum256([]byte(netLabel))
	return "chim" + hex.EncodeToString(sum[:4])
}
