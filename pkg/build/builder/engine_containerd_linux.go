//go:build linux

package builder

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/worker/base"
	ctdworker "github.com/moby/buildkit/worker/containerd"
)

// containerdDialTimeout bounds the containerd client's blocking dial so a
// wedged (existing but unresponsive) daemon fails engine construction quickly
// instead of hanging on the client's 10s default.
const containerdDialTimeout = 5 * time.Second

// containerdWorkerOpt builds a BuildKit worker backed by an external
// containerd daemon: execution, snapshots, content, leases, and the image
// store are all delegated to containerd, so tagged builds are automatically
// recorded in containerd's image store. Worker state lives under
// <Root>/containerd-<snapshotter>/, disjoint from the runc worker's
// <Root>/runc-<snapshotter>/. The lazy build-context plumbing (snapshotter
// registry, oci-layout seeding) does not apply here; New and Build/Solve
// reject lazy builds on this worker.
func containerdWorkerOpt(cfg Config) (base.WorkerOpt, error) {
	address := cfg.Containerd.Address
	if err := probeContainerdSocket(address); err != nil {
		return base.WorkerOpt{}, fmt.Errorf("builder: containerd worker at %q: %w (is containerd running? set %s)", address, err, containerdAddressEnv)
	}
	wopt, err := ctdworker.NewWorkerOpt(ctdworker.WorkerOptions{
		Root:            cfg.Root,
		Address:         address,
		SnapshotterName: cfg.Containerd.Snapshotter,
		Namespace:       cfg.Containerd.Namespace,
		Rootless:        cfg.Rootless,
		NetworkOpt:      netproviders.Opt{Mode: "auto"},
	}, containerd.WithTimeout(containerdDialTimeout))
	if err != nil {
		return base.WorkerOpt{}, fmt.Errorf("builder: containerd worker at %q: %w (is containerd running? set %s)", address, err, containerdAddressEnv)
	}
	return wopt, nil
}

// probeContainerdSocket fails fast when the containerd unix socket is missing
// or not accepting connections. containerd.New dials with grpc.WithBlock, so a
// dead socket path would otherwise burn the full dial timeout before erroring;
// the probe turns that into an immediate, clear failure. Non-unix addresses
// (anything with an explicit non-unix scheme) skip the probe and rely on the
// client's bounded dial.
func probeContainerdSocket(address string) error {
	path := strings.TrimPrefix(address, "unix://")
	if strings.Contains(path, "://") {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}
