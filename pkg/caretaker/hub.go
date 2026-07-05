package caretaker

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"golang.org/x/sync/errgroup"

	"cornus/pkg/hub"
	"cornus/pkg/wire"
)

// HubRole is the caretaker's workload-to-workload overlay membership, carried over
// the pod's unified caretaker connection (see runCaretakerConn): the caretaker
// registers the services this pod hosts (Register), serves ingress for delivered
// ones, and for each service this pod needs to reach opens loopback listeners
// (Reach) that forward accepted connections to the hub. Egress interception mirrors
// the cooperative proxy role — the app's DNS points the peer name at Reach.Listen (a
// loopback address), so an ordinary dial funnels into the hub. Server is the cornus
// hub URL. See .agents/docs/ARCHITECTURE.md ("Workload-to-workload hub").
type HubRole struct {
	Server   string       `json:"server"`
	Identity string       `json:"identity,omitempty"` // this pod's identity, for hub policy
	Register []HubService `json:"register,omitempty"`
	Reach    []HubPeer    `json:"reach,omitempty"`
	// ReachDynamic, when set, opts this spoke into dynamic import discovery: the
	// caretaker asks the hub to push catalog updates over the control connection
	// and binds a loopback listener at hub.SyntheticIP(name) on the given ports
	// for EVERY cataloged service (excluding the ones this spoke registers itself
	// and the ones already statically reached), adding listeners as services
	// appear and closing them as they vanish. Each bind also publishes
	// name -> synthetic IP into the pod's dynamic DNS overlay (DynamicDNS), so a
	// caretaker that also runs the dns role resolves discovered names
	// transparently; each unbind withdraws the record. See runDynamicReach.
	ReachDynamic *HubDynamicReach `json:"reachDynamic,omitempty"`
}

// HubDynamicReach configures dynamic import discovery (see HubRole.ReachDynamic).
// Each discovered service gets its own synthetic loopback IP, so one shared port
// set is bound per service. Protocol is "tcp" (default, empty) or "udp".
type HubDynamicReach struct {
	Ports    []int  `json:"ports"`
	Protocol string `json:"protocol,omitempty"`
}

// wantsDynamicReach reports whether this role subscribes to catalog updates —
// only when a dynamic reach with at least one port is configured, so spokes with
// purely static interest stay entirely off the push path (the hub sends nothing).
func (r *HubRole) wantsDynamicReach() bool {
	return r.ReachDynamic != nil && len(r.ReachDynamic.Ports) > 0
}

// HubService is one service this pod hosts. Set Addr for dial-direct (the hub
// dials Addr itself — it must be hub-reachable), or Target for delivery (the hub
// opens an ingress stream to this spoke, which dials Target locally and splices —
// so Target need not be reachable from the hub). Exactly one of Addr/Target is set.
// Protocol is "tcp" (default, empty) or "udp".
type HubService struct {
	Name     string `json:"name"`
	Addr     string `json:"addr,omitempty"`
	Target   string `json:"target,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// HubPeer is one service this pod reaches through the hub: the caretaker binds
// Listen on each of Ports and forwards accepted connections to the hub as a data
// stream to service Name. Protocol is "tcp" (default, empty) or "udp"; a UDP peer
// binds a datagram listener and frames each datagram over the hub.
type HubPeer struct {
	Name     string `json:"name"`
	Listen   string `json:"listen"`
	Ports    []int  `json:"ports,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// hubTarget is a delivered service's local dial target: the address the spoke
// dials when the hub delivers an inbound flow for it, plus its protocol ("tcp" or
// "udp").
type hubTarget struct {
	addr  string
	proto string
}

// udpFlowIdle is how long a per-source UDP reach flow may sit idle before the GC
// closes it. UDP is connectionless, so there is no FIN to reclaim the hub stream;
// the idle timer is the substitute.
var udpFlowIdle = 60 * time.Second

// startReachListeners binds each peer's loopback (Listen, Ports) and forwards
// accepted connections to the hub as a 'D' data stream to that service name. TCP
// peers listen with net.Listen and splice per connection; UDP peers bind a
// net.ListenUDP socket and run a per-source flow model (startUDPReach). The
// listeners are registered on the caller's errgroup so they share the connection's
// lifetime; ctx cancellation closes them. Called from runCaretakerConn.
func startReachListeners(ctx context.Context, g *errgroup.Group, sess *yamux.Session, peers []HubPeer) error {
	for _, peer := range peers {
		peer := peer
		for _, port := range peer.Ports {
			port := port
			listen := net.JoinHostPort(peer.Listen, strconv.Itoa(port))
			if peer.Protocol == "udp" {
				if err := startUDPReach(ctx, g, sess, peer.Name, listen); err != nil {
					return err
				}
				continue
			}
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("hub: listen %s: %w", listen, err)
			}
			g.Go(func() error { <-ctx.Done(); ln.Close(); return nil })
			g.Go(func() error {
				for {
					c, err := ln.Accept()
					if err != nil {
						if ctx.Err() != nil {
							return nil
						}
						return err
					}
					go forwardToHub(sess, c, peer.Name)
				}
			})
		}
	}
	return nil
}

// forwardToHub relays one accepted TCP connection to the named service via the hub.
func forwardToHub(sess *yamux.Session, c net.Conn, name string) {
	defer c.Close()
	up, err := hub.OpenTo(sess, name)
	if err != nil {
		return
	}
	defer up.Close()
	spliceBidir(c, up)
}

// udpFlow is one client's live path through the hub: the framed 'D' stream opened
// for its source address, and when it was last active (for idle GC). UDP has no
// connection, so the caretaker keys flows by the client's source address and reuses
// the same hub stream for every datagram from that source, routing replies back to it.
type udpFlow struct {
	stream net.Conn
	last   time.Time
}

// startUDPReach binds a UDP socket on listen and runs a per-source flow model so a
// connectionless UDP client can reach a hub service and get replies routed back.
// On each inbound datagram it finds or creates the flow for the source address
// (opening a 'D' hub stream and a reader goroutine that frames replies back to that
// source), then frames the datagram onto the flow's stream. An idle GC closes flows
// that go quiet (there is no UDP close), and ctx cancellation tears everything down.
func startUDPReach(ctx context.Context, g *errgroup.Group, sess *yamux.Session, name, listen string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return fmt.Errorf("hub: resolve udp %s: %w", listen, err)
	}
	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("hub: listen udp %s: %w", listen, err)
	}
	runUDPReach(ctx, g, sess, name, pc)
	return nil
}

// runUDPReach is the per-source flow loop over an already-bound UDP socket (split
// out so tests can drive a known ephemeral port). See startUDPReach.
func runUDPReach(ctx context.Context, g *errgroup.Group, sess *yamux.Session, name string, pc *net.UDPConn) {
	idle := udpFlowIdle // capture once so no goroutine reads the global concurrently
	var mu sync.Mutex
	flows := map[string]*udpFlow{}
	closeAll := func() {
		mu.Lock()
		for k, f := range flows {
			f.stream.Close()
			delete(flows, k)
		}
		mu.Unlock()
	}

	g.Go(func() error { <-ctx.Done(); pc.Close(); closeAll(); return nil })

	// Idle GC: reclaim flows whose source has gone quiet.
	g.Go(func() error {
		t := time.NewTicker(idle)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case now := <-t.C:
				mu.Lock()
				for k, f := range flows {
					if now.Sub(f.last) > idle {
						f.stream.Close()
						delete(flows, k)
					}
				}
				mu.Unlock()
			}
		}
	})

	g.Go(func() error {
		buf := make([]byte, wire.MaxDatagram)
		for {
			n, src, err := pc.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			key := src.String()
			mu.Lock()
			f := flows[key]
			if f == nil {
				stream, oerr := hub.OpenTo(sess, name)
				if oerr != nil {
					mu.Unlock()
					continue // transient; the client will retry (UDP)
				}
				f = &udpFlow{stream: stream, last: time.Now()}
				flows[key] = f
				src := src
				// Reply reader: frame each datagram the hub returns back to this source.
				go func() {
					for {
						dgram, rerr := wire.ReadDatagram(stream)
						if rerr != nil {
							mu.Lock()
							if flows[key] == f {
								delete(flows, key)
							}
							mu.Unlock()
							stream.Close()
							return
						}
						if _, werr := pc.WriteToUDP(dgram, src); werr != nil {
							return
						}
						// Refresh liveness on delivered replies too, so the idle GC
						// only reclaims flows quiet in BOTH directions — a server-push
						// / asymmetric flow whose client has stopped sending must not
						// be torn down while it is actively delivering data.
						mu.Lock()
						if flows[key] == f {
							f.last = time.Now()
						}
						mu.Unlock()
					}
				}()
			}
			f.last = time.Now()
			stream := f.stream
			mu.Unlock()
			payload := make([]byte, n)
			copy(payload, buf[:n])
			if werr := wire.WriteDatagram(stream, payload); werr != nil {
				mu.Lock()
				if flows[key] == f {
					delete(flows, key)
				}
				mu.Unlock()
				stream.Close()
			}
		}
	})
}

// serveIngress accepts ingress-delivery streams the hub opens to this spoke for
// its delivered services, dials the matching local target, and splices (or, for a
// UDP target, datagram-bridges) — so a service this pod hosts is reachable through
// the hub even when the hub cannot dial it directly. Runs until ctx is done (the
// session closes).
func serveIngress(ctx context.Context, sess *yamux.Session, targets map[string]hubTarget) error {
	for {
		tag, stream, err := wire.AcceptTagged(sess)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if tag != wire.TagDeliver {
			stream.Close()
			continue
		}
		go deliverIngress(stream, targets)
	}
}

// deliverIngress reads the service name off one ingress stream, dials the local
// target registered for it, and splices. For a UDP target it opens a connected UDP
// socket and datagram-bridges the framed stream to it instead. An unknown name
// closes the stream.
func deliverIngress(stream net.Conn, targets map[string]hubTarget) {
	defer stream.Close()
	name, err := wire.ReadLine(stream)
	if err != nil {
		return
	}
	tgt, ok := targets[name]
	if !ok {
		return
	}
	if tgt.proto == "udp" {
		up, err := net.Dial("udp", tgt.addr)
		if err != nil {
			return
		}
		defer up.Close()
		wire.BridgeDatagram(stream, up)
		return
	}
	up, err := net.Dial("tcp", tgt.addr)
	if err != nil {
		return
	}
	defer up.Close()
	spliceBidir(stream, up)
}
