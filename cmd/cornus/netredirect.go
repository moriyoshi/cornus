package main

import (
	"fmt"

	"cornus/pkg/netredirect"
)

// NetRedirectCmd installs the nftables NAT rules that transparently capture the
// pod's app-container outbound TCP into the caretaker's enforcing proxy. It runs
// as an init container with NET_ADMIN before the app starts. The caretaker's own
// forwarded upstream dials are left alone so they are not re-redirected — by uid
// (ExemptUID, the proxy-only case where the caretaker runs as a dedicated uid) or
// by firewall mark (ExemptMark, the proxy-with-mounts case where the caretaker
// runs as root and marks its sockets instead). Loopback is always exempt. At
// least one of ExemptUID / ExemptMark must be set. Programmed via the nftables
// netlink API directly — no iptables/nft CLI, so the sidecar image needs no
// packages. Idempotent.
type NetRedirectCmd struct {
	ToPort     int `kong:"name='to-port',required,help='Proxy listen port to redirect app egress to.'"`
	ExemptUID  int `kong:"name='exempt-uid',help='UID whose traffic is NOT redirected (the caretaker, proxy-only case).'"`
	ExemptMark int `kong:"name='exempt-mark',help='SO_MARK whose traffic is NOT redirected (the caretaker, proxy+mounts case).'"`
}

// Run programs the redirect (platform-specific; nftables via netlink on linux).
func (c *NetRedirectCmd) Run(cli *CLI) error {
	if c.ExemptUID == 0 && c.ExemptMark == 0 {
		return fmt.Errorf("net-redirect: one of --exempt-uid / --exempt-mark is required")
	}
	return netredirect.Setup(c.ToPort, c.ExemptUID, c.ExemptMark)
}
