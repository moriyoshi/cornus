package caretaker

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/hashicorp/yamux"

	"cornus/pkg/egresspolicy"
	"cornus/pkg/logging"
	"cornus/pkg/netredirect"
)

// runEgressTransparent serves the transparent egress mode: the app container's
// outbound TCP is captured by an nftables REDIRECT (installed by the net-redirect
// init container) into this listener, and the pre-DNAT destination is recovered via
// SO_ORIGINAL_DST. Each connection is then routed by the same policy as proxy mode —
// relayed to the client/gateway, dialed directly for the cluster route, or dropped
// on deny — with no app-side proxy configuration required. mark, when non-zero, is
// stamped on the caretaker's own dials so they escape the redirect (the mounts+egress
// case where the caretaker runs as root and cannot be exempted by uid).
func runEgressTransparent(ctx context.Context, sess *yamux.Session, role EgressRole, mark int) error {
	pol, err := role.policy()
	if err != nil {
		return fmt.Errorf("egress: policy: %w", err)
	}
	log := logging.FromContext(ctx)
	// Host companion: program the nftables redirect ourselves (we run with NET_ADMIN
	// in the shared netns). On Kubernetes a dedicated init container did it already,
	// so SetupRedirect stays false. Exempt the caretaker's own marked sockets so its
	// relay/direct dials are not re-redirected.
	if role.SetupRedirect {
		if err := netredirect.Setup(role.ListenPort, 0, mark); err != nil {
			return fmt.Errorf("egress: program transparent redirect: %w", err)
		}
		log.InfoContext(ctx, "caretaker egress: transparent redirect programmed", "port", role.ListenPort, "mark", mark)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", role.ListenPort))
	if err != nil {
		return fmt.Errorf("egress: transparent listen :%d: %w", role.ListenPort, err)
	}
	log.InfoContext(ctx, "caretaker egress proxy listening", "port", role.ListenPort, "mode", "transparent")
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		tcp, ok := c.(*net.TCPConn)
		if !ok {
			c.Close()
			continue
		}
		go handleTransparent(ctx, sess, role.Session, tcp, pol, mark)
	}
}

// handleTransparent recovers a redirected connection's original destination and
// relays it by route; a denied destination is simply dropped (the app sees a reset).
func handleTransparent(ctx context.Context, sess *yamux.Session, session string, c *net.TCPConn, pol egresspolicy.Policy, mark int) {
	defer c.Close()
	ctx = logging.WithAttrs(ctx, slog.Group("egress", slog.Any("remote", c.RemoteAddr())))
	log := logging.FromContext(ctx)
	dst, err := originalDst(c)
	if err != nil {
		log.WarnContext(ctx, "SO_ORIGINAL_DST failed", "error", err)
		return
	}
	ctx = logging.WithAttrs(ctx, slog.Group("egress", slog.String("dest", dst)))
	log = logging.FromContext(ctx)
	log.DebugContext(ctx, "transparent capture")
	up, err := routeUpstream(ctx, sess, session, dst, pol, mark, "transparent")
	if err != nil {
		logEgressConn(ctx, err)
		return
	}
	defer up.Close()
	ab, ba := spliceBidir(c, up)
	recordEgressBytes(metrics(), ab, ba)
	log.DebugContext(ctx, "transparent tunnel closed", "sent", ab, "recv", ba)
}
