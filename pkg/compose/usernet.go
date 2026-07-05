package compose

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/logging"
)

// Plan-time static addressing for Multus-realised user networks (matrix row A').
//
// On kubernetes, a compose network with a Multus driver (`bridge`, `ipvlan`,
// `macvlan`) attaches each member pod to a real secondary interface — but
// CoreDNS only ever publishes a pod's PRIMARY cluster IP, so named traffic
// between members would still ride the primary network. To make names resolve
// to the SECONDARY interface, addressing must be known at plan time:
//
//  1. applyUserNetIPs pins a deterministic IPv4 address per (service, network)
//     into api.NetworkAttachment.IP (CIDR form). The kubernetes Multus provider
//     renders the network's NetworkAttachmentDefinition with the `static` IPAM
//     plugin and carries the address in the pod's network-selection annotation.
//  2. The same table is written into each member's api.DNSSpec.Records, so the
//     caretaker DNS role answers every peer name (and alias) with the peer's
//     user-network address, and the connection rides the user network.
//
// Determinism: the address is a pure function of the deployment name and the
// subnet (hash of the name into the subnet's host range), so re-deploys keep
// their IPs regardless of deploy order or which peers exist. Collisions within
// a project are resolved by deterministic double-hash probing over the members
// in sorted order — rare (1/253 per pair on a /24), and pinnable explicitly via
// the service-level `ipv4_address` field when the reshuffle-on-membership-change
// this implies is unacceptable.
//
// A network is left dynamically addressed (host-local IPAM, no DNS records —
// the pre-A' behaviour) when any member scales past one replica (replicas of a
// single Deployment cannot share one static IP), when the caller opts out with
// `driver_opts: {ipam: host-local}`, or when the subnet cannot be parsed. The
// detached-primary mode (NetworkAttachment.Default) is likewise untouched.

// multusDrivers are the compose network drivers the kubernetes netdriver
// realises as Multus CNI attachments (see netdriver.pipelineFor).
var multusDrivers = map[string]bool{"bridge": true, "ipvlan": true, "macvlan": true}

// MultusDefaultSubnet returns the default IPAM subnet the kubernetes netdriver
// derives for a Multus-realised network with no explicit `driver_opts: subnet`:
// a deterministic /24 inside 10.222.0.0/16 keyed on the network's netLabel.
// It MUST agree with netdriver's subnetFor(netLabelName(name), "") — the
// netdriver package has a test importing this function to keep them in sync.
func MultusDefaultSubnet(networkName string) string {
	sum := sha256.Sum256([]byte(userNetLabel(networkName)))
	return fmt.Sprintf("10.222.%d.0/24", sum[0])
}

// userNetLabel mirrors netdriver.netLabelName: the stable DNS-1123 identifier
// derived from a network's logical name, reused as the NAD name and the
// membership label — and, here, as the key the default subnet derives from.
func userNetLabel(logical string) string {
	sum := sha256.Sum256([]byte(logical))
	base := userNetSanitize(logical)
	if base == "" {
		base = "net"
	}
	if len(base) > 54 {
		base = strings.Trim(base[:54], "-")
	}
	return base + "-" + hex.EncodeToString(sum[:4])
}

// userNetSanitize mirrors netdriver.sanitizeDNS1123.
func userNetSanitize(s string) string {
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

// userNetMember is one service's attachment to a shared network.
type userNetMember struct {
	service string // plan key (compose service name)
	att     *api.NetworkAttachment
	spec    *api.DeploySpec
}

// applyUserNetIPs pins static IPs and derives peer DNS records for every
// Multus-driver network in the plans. See the package comment above for the
// scheme. It runs after applyProxyPolicy: a proxied service keeps its pinned
// IP (peers may still resolve and reach it over the user network) but gets no
// DNS caretaker of its own — the enforcing proxy and the DNS role cannot share
// a pod.
func applyUserNetIPs(plans map[string]ServicePlan) error {
	// Plan-time helper with no request context; log against the process default.
	ctx := context.Background()
	log := logging.FromContext(ctx)
	// Reassemble the network topology from the plans (attachments carry the
	// resolved network resource names). Iterate deterministically throughout.
	svcNames := make([]string, 0, len(plans))
	byName := map[string]ServicePlan{}
	for name, plan := range plans {
		svcNames = append(svcNames, name)
		byName[name] = plan
	}
	sort.Strings(svcNames)

	members := map[string][]userNetMember{} // network resource name -> members
	var netNames []string
	for _, svc := range svcNames {
		plan := byName[svc]
		for i := range plan.Spec.Networks {
			att := &plan.Spec.Networks[i]
			if !multusDrivers[att.Driver] || att.Default {
				// Only Multus-realised overlaid networks take static addressing;
				// an explicit ipv4_address anywhere else has no backend that
				// honours it, so drop it loudly rather than carry a dangling pin
				// into the spec.
				if att.IP != "" {
					log.WarnContext(ctx, "ignoring ipv4_address: only Multus-driver networks (bridge/ipvlan/macvlan on kubernetes) support static addresses",
						slog.Group("compose", "service", svc, "network", att.Name, "driver", att.Driver, "ipv4_address", att.IP))
					att.IP = ""
				}
				continue
			}
			if members[att.Name] == nil {
				netNames = append(netNames, att.Name)
			}
			members[att.Name] = append(members[att.Name], userNetMember{service: svc, att: att, spec: &plan.Spec})
		}
	}
	sort.Strings(netNames)

	// Per-network allocation. ips[net][service] = plain (prefix-less) address.
	ips := map[string]map[string]string{}
	for _, netName := range netNames {
		mems := members[netName]
		table, err := allocateUserNet(netName, mems)
		if err != nil {
			return err
		}
		if table != nil {
			ips[netName] = table
		}
	}

	// Peer DNS records: every member of a statically-addressed network resolves
	// each member's aliases (service name first, container_name, declared) to
	// that member's user-network address. A service on several such networks
	// merges them; when the same alias names different addresses the
	// lexicographically first network wins, deterministically.
	for _, svc := range svcNames {
		plan := byName[svc]
		if plan.Spec.Proxy != nil {
			continue // proxy and DNS caretakers cannot share a pod
		}
		records := map[string]string{}
		for _, att := range plan.Spec.Networks {
			table := ips[att.Name]
			if table == nil {
				continue
			}
			for _, peer := range members[att.Name] {
				ip := table[peer.service]
				for _, alias := range peer.att.Aliases {
					if _, dup := records[alias]; !dup {
						records[alias] = ip
					}
				}
			}
		}
		if len(records) > 0 {
			plan.Spec.DNS = &api.DNSSpec{Records: records, RequireUserNet: true}
			plans[svc] = plan
		}
	}
	return nil
}

// allocateUserNet assigns each member of one network its address, writing the
// CIDR form into the attachment and returning service -> plain address. A nil
// table (no error) means the network stays dynamically addressed.
func allocateUserNet(netName string, mems []userNetMember) (map[string]string, error) {
	// Plan-time helper with no request context; log against the process default.
	ctx := context.Background()
	log := logging.FromContext(ctx)
	opts := mems[0].att.DriverOpts
	if opts["ipam"] == "host-local" {
		// Explicit dynamic-addressing opt-out (the pre-A' behaviour). An
		// ipv4_address pin cannot be honoured under host-local IPAM.
		for _, m := range mems {
			if m.att.IP != "" {
				return nil, fmt.Errorf("network %s: service %s pins ipv4_address %s, but driver_opts ipam host-local requests dynamic addressing", netName, m.service, m.att.IP)
			}
		}
		return nil, nil
	}

	subnetStr := opts["subnet"]
	if subnetStr == "" {
		subnetStr = MultusDefaultSubnet(netName)
	}
	subnet, err := netip.ParsePrefix(subnetStr)
	if err != nil || !subnet.Addr().Is4() || subnet.Bits() > 29 {
		// Not a usable IPv4 subnet (v6, malformed, or too small to reserve
		// network/gateway/broadcast plus hosts): leave the network dynamic
		// unless someone pinned an address that we would then silently drop.
		for _, m := range mems {
			if m.att.IP != "" {
				return nil, fmt.Errorf("network %s: cannot pin ipv4_address %s: subnet %q is not a usable IPv4 subnet", netName, m.att.IP, subnetStr)
			}
		}
		log.WarnContext(ctx, "user network keeps dynamic addressing (subnet not usable for static IPs); named traffic will ride the primary network on kubernetes",
			slog.Group("compose", "network", netName, "subnet", subnetStr))
		return nil, nil
	}
	subnet = subnet.Masked()

	for _, m := range mems {
		if m.spec.Replicas > 1 {
			// Replicas of one Deployment cannot share a single static IP; a
			// mixed static/dynamic NAD is impossible, so the whole network
			// stays dynamic. An explicit pin makes that contradiction an error.
			for _, mm := range mems {
				if mm.att.IP != "" {
					return nil, fmt.Errorf("network %s: service %s pins ipv4_address, but service %s scales to %d replicas; a scaled network cannot be statically addressed", netName, mm.service, m.service, m.spec.Replicas)
				}
			}
			log.WarnContext(ctx, "user network keeps dynamic addressing (a member scales past one replica); named traffic will ride the primary network on kubernetes",
				slog.Group("compose", "network", netName, "service", m.service, "replicas", m.spec.Replicas))
			return nil, nil
		}
	}

	// Explicit ipv4_address pins first: they are hard requirements and take
	// their slot before any derived address probes around them.
	taken := map[netip.Addr]string{} // address -> service holding it
	table := map[string]string{}
	for _, m := range mems {
		if m.att.IP == "" {
			continue
		}
		addr, err := netip.ParseAddr(m.att.IP)
		if err != nil || !addr.Is4() {
			return nil, fmt.Errorf("network %s: service %s: ipv4_address %q is not an IPv4 address", netName, m.service, m.att.IP)
		}
		if !subnet.Contains(addr) {
			return nil, fmt.Errorf("network %s: service %s: ipv4_address %s is outside the network subnet %s", netName, m.service, addr, subnet)
		}
		if holder, dup := taken[addr]; dup {
			return nil, fmt.Errorf("network %s: services %s and %s both pin ipv4_address %s", netName, holder, m.service, addr)
		}
		taken[addr] = m.service
		table[m.service] = addr.String()
		m.att.IP = fmt.Sprintf("%s/%d", addr, subnet.Bits())
	}

	// Derived addresses for the rest, in sorted-member order (mems is built
	// from sorted service names). Keyed on the deployment resource name
	// (spec.Name, "<project>-<service>") so distinct projects sharing an
	// external network tend to distinct addresses.
	for _, m := range mems {
		if m.att.IP != "" {
			continue
		}
		addr, err := staticIPFor(m.spec.Name, subnet, taken)
		if err != nil {
			return nil, fmt.Errorf("network %s: service %s: %w", netName, m.service, err)
		}
		taken[addr] = m.service
		table[m.service] = addr.String()
		m.att.IP = fmt.Sprintf("%s/%d", addr, subnet.Bits())
	}
	return table, nil
}

// staticIPFor deterministically picks an unused host address for name inside
// subnet: the primary candidate is sha256(name) reduced onto the usable host
// range (skipping the network address, the .1 gateway the bridge plugin
// claims, and the broadcast address); collisions probe further salted hashes,
// then fall back to a linear scan so allocation succeeds whenever a free slot
// exists.
func staticIPFor(name string, subnet netip.Prefix, taken map[netip.Addr]string) (netip.Addr, error) {
	size := uint64(1) << (32 - subnet.Bits())
	usable := size - 3 // minus network, gateway (.1), broadcast
	base := binary.BigEndian.Uint32(subnet.Masked().Addr().AsSlice())

	at := func(idx uint64) netip.Addr {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], base+uint32(2+idx))
		return netip.AddrFrom4(b)
	}
	first := uint64(0)
	for k := 0; k < 8; k++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", name, k)))
		idx := binary.BigEndian.Uint64(sum[:8]) % usable
		if k == 0 {
			first = idx
		}
		if _, dup := taken[at(idx)]; !dup {
			return at(idx), nil
		}
	}
	for off := uint64(0); off < usable; off++ {
		idx := (first + off) % usable
		if _, dup := taken[at(idx)]; !dup {
			return at(idx), nil
		}
	}
	return netip.Addr{}, fmt.Errorf("subnet %s has no free host address left for a static IP", subnet)
}
