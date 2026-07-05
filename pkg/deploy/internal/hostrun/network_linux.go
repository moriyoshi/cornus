//go:build linux

package hostrun

// CNI networking: every network is a generated CNI bridge+portmap conflist
// (nerdctl-style), each instance gets its own pinned netns joined to its
// networks, and published ports ride portmap. There is no embedded DNS — service
// names and aliases resolve between instances via the hosts-file sync
// (hosts_linux.go). Shared by both host backends; the datadir subdir + error
// prefix are per-backend, and each backend keeps its own thin teardown at the
// call boundary (barehost passes the netns/networks/ports from its records,
// containerd decodes them from container labels).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/containerd/containerd/pkg/netns"
	gocni "github.com/containerd/go-cni"

	"cornus/pkg/api"
)

// DefaultNetwork is the implicit network instances join when the spec declares
// none. It always allocates subnet index 0 and its conflist is never reaped.
const DefaultNetwork = "default"

// netnsDir is where instance network namespaces are bind-mounted (tmpfs-backed;
// pins survive a cornus restart but not a host reboot — the backends' reboot
// recovery rebuilds them).
const netnsDir = "/run/cornus/netns"

// CNIManager realizes user-defined networks as generated CNI bridge+portmap
// conflists, allocating a /24 per network and a pinned netns per instance.
type CNIManager struct {
	mu        sync.Mutex
	confDir   string
	state     string
	errPrefix string
}

// NewCNIManager builds a manager rooted at <dataDir>/<subdir>/cni; errPrefix
// ("bare"/"containerd") heads its errors.
func NewCNIManager(dataDir, subdir, errPrefix string) *CNIManager {
	base := filepath.Join(dataDir, subdir, "cni")
	return &CNIManager{
		confDir:   filepath.Join(base, "conf"),
		state:     filepath.Join(base, "subnets.json"),
		errPrefix: errPrefix,
	}
}

// ConfDir is the directory holding the generated conflists (enumerated by the
// backends' network GC to find user networks no instance references).
func (m *CNIManager) ConfDir() string { return m.confDir }

// pluginDirs resolves the CNI plugin binary search path: CORNUS_CNI_BIN_DIR, then
// the standard CNI_PATH list, then /opt/cni/bin.
func pluginDirs() []string {
	if d := os.Getenv("CORNUS_CNI_BIN_DIR"); d != "" {
		return []string{d}
	}
	if p := os.Getenv("CNI_PATH"); p != "" {
		return filepath.SplitList(p)
	}
	return []string{"/opt/cni/bin"}
}

// subnetBase is the /16-shaped prefix networks carve /24s out of.
func subnetBase() string {
	if b := os.Getenv("CORNUS_CNI_SUBNET_BASE"); b != "" {
		return b
	}
	return "10.4"
}

func findPlugin(dirs []string, name string) bool {
	for _, d := range dirs {
		if st, err := os.Stat(filepath.Join(d, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// checkPlugins fails with an actionable error when required CNI plugin binaries
// are missing.
func (m *CNIManager) checkPlugins(dirs []string) error {
	var missing []string
	for _, p := range []string{"bridge", "portmap", "host-local", "loopback"} {
		if !findPlugin(dirs, p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: missing CNI plugins %s under %s (install the CNI plugins package, or point CORNUS_CNI_BIN_DIR/CNI_PATH at them)",
			m.errPrefix, strings.Join(missing, ", "), strings.Join(dirs, ":"))
	}
	return nil
}

// subnetState is the persisted allocator: logical network name -> /24 index.
type subnetState struct {
	Networks map[string]int `json:"networks"`
}

func (m *CNIManager) loadState() (subnetState, error) {
	st := subnetState{Networks: map[string]int{}}
	data, err := os.ReadFile(m.state)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", m.state, err)
	}
	if st.Networks == nil {
		st.Networks = map[string]int{}
	}
	return st, nil
}

func (m *CNIManager) saveState(st subnetState) error {
	if err := os.MkdirAll(filepath.Dir(m.state), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := m.state + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.state)
}

// allocateSubnet returns the network's persistent /24 index (default = 0). Caller
// holds m.mu.
func (m *CNIManager) allocateSubnet(name string) (int, error) {
	st, err := m.loadState()
	if err != nil {
		return 0, err
	}
	if idx, ok := st.Networks[name]; ok {
		return idx, nil
	}
	idx := 0
	if name != DefaultNetwork {
		used := map[int]bool{0: true}
		for _, i := range st.Networks {
			used[i] = true
		}
		idx = 1
		for used[idx] {
			idx++
		}
		if idx > 255 {
			return 0, fmt.Errorf("%s: no free /24 left under %s.0.0/16", m.errPrefix, subnetBase())
		}
	}
	st.Networks[name] = idx
	return idx, m.saveState(st)
}

// releaseSubnet frees a network's allocator entry. Caller holds m.mu.
func (m *CNIManager) releaseSubnet(name string) error {
	st, err := m.loadState()
	if err != nil {
		return err
	}
	if _, ok := st.Networks[name]; !ok {
		return nil
	}
	delete(st.Networks, name)
	return m.saveState(st)
}

// bridgeIfName derives a <=15-byte bridge interface name for a network.
func bridgeIfName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "crns" + hex.EncodeToString(sum[:])[:8]
}

// GatewayFor returns a network's CNI bridge gateway IP (the first address of its
// /24, which host-local assigns to the isGateway bridge). Errors if the network
// has no subnet allocated yet. Used by the barehost DNS resolver (bind address)
// and resolv.conf generation (the container's nameserver).
func (m *CNIManager) GatewayFor(network string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.loadState()
	if err != nil {
		return "", err
	}
	idx, ok := st.Networks[network]
	if !ok {
		return "", fmt.Errorf("%s: no subnet allocated for network %q", m.errPrefix, network)
	}
	return fmt.Sprintf("%s.%d.1", subnetBase(), idx), nil
}

func (m *CNIManager) conflistPath(name string) string {
	return filepath.Join(m.confDir, "cornus-"+name+".conflist")
}

type cniConflist struct {
	CNIVersion string    `json:"cniVersion"`
	Name       string    `json:"name"`
	Plugins    []cniConf `json:"plugins"`
}

type cniConf struct {
	Type         string          `json:"type"`
	Bridge       string          `json:"bridge,omitempty"`
	IsGateway    bool            `json:"isGateway,omitempty"`
	IPMasq       bool            `json:"ipMasq,omitempty"`
	HairpinMode  bool            `json:"hairpinMode,omitempty"`
	IPAM         *cniIPAM        `json:"ipam,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
}

type cniIPAM struct {
	Type   string              `json:"type"`
	Ranges [][]cniRange        `json:"ranges"`
	Routes []map[string]string `json:"routes"`
}

type cniRange struct {
	Subnet string `json:"subnet"`
}

// conflist renders a network's CNI config: a bridge with host-local IPAM on
// subnet, plus portmap; the firewall plugin is appended when its binary exists.
func conflist(name, subnet string, withFirewall bool) ([]byte, error) {
	plugins := []cniConf{
		{
			Type:        "bridge",
			Bridge:      bridgeIfName(name),
			IsGateway:   true,
			IPMasq:      true,
			HairpinMode: true,
			IPAM: &cniIPAM{
				Type:   "host-local",
				Ranges: [][]cniRange{{{Subnet: subnet}}},
				Routes: []map[string]string{{"dst": "0.0.0.0/0"}},
			},
		},
		{
			Type:         "portmap",
			Capabilities: map[string]bool{"portMappings": true},
		},
	}
	if withFirewall {
		plugins = append(plugins, cniConf{Type: "firewall"})
	}
	return json.MarshalIndent(cniConflist{
		CNIVersion: "1.0.0",
		Name:       "cornus-" + name,
		Plugins:    plugins,
	}, "", "  ")
}

// EnsureNetworks materializes conflists for the given logical networks
// (create-if-absent, idempotent).
func (m *CNIManager) EnsureNetworks(names []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.confDir, 0o755); err != nil {
		return err
	}
	dirs := pluginDirs()
	for _, name := range names {
		path := m.conflistPath(name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		idx, err := m.allocateSubnet(name)
		if err != nil {
			return err
		}
		subnet := fmt.Sprintf("%s.%d.0/24", subnetBase(), idx)
		data, err := conflist(name, subnet, findPlugin(dirs, "firewall"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RemoveNetwork deletes a managed network's conflist and frees its subnet. The
// default network is left alone (recreated on demand, keeps index 0).
func (m *CNIManager) RemoveNetwork(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == DefaultNetwork {
		return nil
	}
	if err := os.Remove(m.conflistPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.releaseSubnet(name)
}

// InstanceNetworks returns the logical networks an instance joins: the spec's
// user networks, or the implicit default network.
func InstanceNetworks(spec api.DeploySpec) []string {
	if names := NetworkNames(spec); len(names) > 0 {
		return names
	}
	return []string{DefaultNetwork}
}

// UnsupportedNetworkFeatures lists network spec features the CNI bridge backing
// cannot honor, for a per-deploy warning (the same auto-allocated /24 bridge
// limits on both host backends).
func UnsupportedNetworkFeatures(spec api.DeploySpec) []string {
	var driver, driverOpts, ipam, isolation, labels, endpoint bool
	for _, n := range spec.Networks {
		driver = driver || (n.Driver != "" && n.Driver != "bridge")
		driverOpts = driverOpts || len(n.DriverOpts) > 0
		ipam = ipam || n.Subnet != "" || n.Gateway != "" || n.IPRange != ""
		isolation = isolation || n.Internal || n.Attachable || n.EnableIPv6
		labels = labels || len(n.Labels) > 0
		endpoint = endpoint || n.IPv6 != "" || n.MAC != "" || n.Priority != 0
	}
	var out []string
	if driver {
		out = append(out, "driver")
	}
	if driverOpts {
		out = append(out, "driverOpts")
	}
	if ipam {
		out = append(out, "ipam")
	}
	if isolation {
		out = append(out, "internal/attachable/enableIPv6")
	}
	if labels {
		out = append(out, "labels")
	}
	if endpoint {
		out = append(out, "ipv6Address/macAddress/priority")
	}
	return out
}

// load builds a go-cni instance scoped to the given networks (plus loopback at
// position 0, so networks[i] attaches as eth(i+1)).
func (m *CNIManager) load(networks []string) (gocni.CNI, error) {
	cni, err := gocni.New(gocni.WithPluginDir(pluginDirs()), gocni.WithMinNetworkCount(len(networks)+1))
	if err != nil {
		return nil, err
	}
	opts := []gocni.Opt{gocni.WithLoNetwork}
	for _, n := range networks {
		opts = append(opts, gocni.WithConfListFile(m.conflistPath(n)))
	}
	if err := cni.Load(opts...); err != nil {
		return nil, fmt.Errorf("%s: load CNI networks %v: %w", m.errPrefix, networks, err)
	}
	return cni, nil
}

// toCNIPorts converts published port mappings (Host set) to portmap args.
func toCNIPorts(ports []api.PortMapping) []gocni.PortMapping {
	var out []gocni.PortMapping
	for _, p := range ports {
		if p.Host == 0 {
			continue
		}
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		out = append(out, gocni.PortMapping{
			HostPort:      int32(p.Host),
			ContainerPort: int32(p.Container),
			Protocol:      proto,
			HostIP:        p.HostIP,
		})
	}
	return out
}

// Attachment describes an instance's realized network attachment.
type Attachment struct {
	Netns string
	IP    string
	IPs   map[string]string
}

// Setup creates a netns for the instance and attaches it to its networks,
// publishing ports via portmap. On error nothing is left behind.
func (m *CNIManager) Setup(ctx context.Context, id string, networks []string, ports []api.PortMapping) (Attachment, error) {
	if err := m.checkPlugins(pluginDirs()); err != nil {
		return Attachment{}, err
	}
	cni, err := m.load(networks)
	if err != nil {
		return Attachment{}, err
	}
	ns, err := netns.NewNetNS(netnsDir)
	if err != nil {
		return Attachment{}, fmt.Errorf("%s: create netns: %w", m.errPrefix, err)
	}
	res, err := cni.Setup(ctx, id, ns.GetPath(), gocni.WithCapabilityPortMap(toCNIPorts(ports)))
	if err != nil {
		// go-cni does not unwind partially-attached networks; tear down best-effort
		// before removing the netns (mirrors Teardown).
		_ = cni.Remove(ctx, id, ns.GetPath(), gocni.WithCapabilityPortMap(toCNIPorts(ports)))
		_ = ns.Remove()
		return Attachment{}, fmt.Errorf("%s: CNI setup for %s: %w", m.errPrefix, id, err)
	}
	att := Attachment{Netns: ns.GetPath(), IPs: map[string]string{}}
	for i, nw := range networks {
		if cfg, ok := res.Interfaces[fmt.Sprintf("eth%d", i+1)]; ok && len(cfg.IPConfigs) > 0 {
			att.IPs[nw] = cfg.IPConfigs[0].IP.String()
		}
	}
	if len(networks) > 0 {
		att.IP = att.IPs[networks[0]]
	}
	if att.IP == "" {
		att.IP = primaryIP(res)
	}
	return att, nil
}

// primaryIP extracts the first sandbox interface IP (eth0 by convention).
func primaryIP(res *gocni.Result) string {
	if cfg, ok := res.Interfaces["eth0"]; ok && len(cfg.IPConfigs) > 0 {
		return cfg.IPConfigs[0].IP.String()
	}
	names := make([]string, 0, len(res.Interfaces))
	for name := range res.Interfaces {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if cfg := res.Interfaces[name]; cfg.Sandbox != "" && len(cfg.IPConfigs) > 0 {
			return cfg.IPConfigs[0].IP.String()
		}
	}
	return ""
}

// Teardown detaches an instance from its networks (releasing portmap rules) and
// removes its netns, from the fields the caller recorded at create time.
// Best-effort.
func (m *CNIManager) Teardown(ctx context.Context, id, netnsPath string, networks []string, ports []api.PortMapping) {
	if netnsPath == "" {
		return
	}
	if len(networks) == 0 {
		networks = []string{DefaultNetwork}
	}
	if cni, err := m.load(networks); err == nil {
		_ = cni.Remove(ctx, id, netnsPath, gocni.WithCapabilityPortMap(toCNIPorts(ports)))
	}
	_ = netns.LoadNetNS(netnsPath).Remove()
}
