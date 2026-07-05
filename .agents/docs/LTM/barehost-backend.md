# Barehost Deploy Backend

## Summary

`pkg/deploy/barehost` is Cornus's daemonless Linux deploy backend. It runs OCI workloads with an OCI runtime, content store, snapshotter, CNI, and Cornus-owned lifecycle supervision rather than Docker or a containerd daemon. It preserves the `deploy.Backend` contract while deliberately keeping the deployment dependency tree free of Moby and BuildKit.

## Key Facts

- Select it with `CORNUS_DEPLOY_BACKEND=bare`; it needs root, an OCI runtime (`runc` by default), CNI plugins, and compatible image/snapshot storage, but no daemon socket.
- Persistent instance records replace daemon metadata. On startup, reconcile adopts desired workloads, restores runtime state after a reboot, and keeps peer DNS/hosts data current.
- CNI networking, volume realization, port forwarding, exec, attach, copy, logs, and cgroup-backed Stats are supported. Guest DNS is served by the Cornus server, not a caretaker.
- `CORNUS_BARE_SHIM=1` opts into a detached `cornus bare-shim` supervisor. The in-process supervisor remains the default until the shim has soaked further.

## Details

### Lifecycle and recovery

Barehost creates rootfs snapshots and OCI bundles directly, runs containers through the selected runtime, and stores desired state, restart counters, network attachments, and mounts on disk. Restart policy is supervised in-process by default. The optional shim is a detached session leader and child subreaper: it owns one container init, exposes a Unix control socket, records its PID/socket in `shim.state`, and can preserve restart supervision through a server-down interval. It waits for the specific init only after `runc create`/`start` have returned, avoiding a race with Go's `os/exec` child reaping.

Host reboot recovery handles the volatile `/run/cornus` state disappearing while records, image content, and bundles survive. `recoverInstance` remounts the rootfs, recreates the pinned netns and CNI attachment, rewrites the OCI config's netns path, persists the new IP, and resynchronizes hosts/DNS. CNI host-local allocation survives reboots, so recovery must tear down the stale attachment before setup to release its old allocation. `server.Run` eagerly initializes barehost when selected so recovery does not wait for the first API request.

Companions for egress and client-local mounts join the application's netns. Teardown must graceful-stop them so a 9P caretaker unmounts before `runc delete`; force-killing it can leave a busy cgroup and leaked stopped container. Shim mode falls back to the graceful direct path whenever no live shim handles a companion or a wedged shim.

### Networking, DNS, mounts, and Stats

Barehost uses CNI bridge networking and a hosts-file store. Guest container DNS is supplied server-side, because the server owns bare networking; caretakers do not provide this responsibility. `BareTarget.AdvertiseHost` must return a routable host address, not `127.0.0.1`, so a companion in the guest netns can reach the server.

Remote companions implement `EgressBackend` and `MountingBackend`. Automated bare scenarios use a registry-hosted agent image because barehost has its own content store. Nested DinD cannot currently prove shared-subtree propagation of sidecar-mounted 9P file contents, although it verifies companion spawning, netns sharing, lifecycle, and mount wiring.

`Stats` reads cgroup pseudo-files directly rather than importing cgroup manager libraries. It discovers the cgroup from `/proc/<init-pid>/cgroup`, handles cgroup v2 and best-effort v1 files, and resolves the init PID per sample. In nested environments controllers may not be delegated, making memory/pids zero while CPU and host-limit fallbacks remain valid.

### Shared extraction and runtime compatibility

The M7 extraction moved daemon-agnostic OCI spec construction, netns liveness, hosts management, volume seeding, CNI, and Stats encoding into `pkg/deploy/internal/hostrun`; see [hostrun-shared-runtime.md](./hostrun-shared-runtime.md). Barehost also supports gVisor `runsc`; direct cgroup Stats and tar-copy remain runtime-agnostic.

## Files

- `pkg/deploy/barehost/` - daemonless backend, shim, reboot recovery, direct cgroup sampler.
- `cmd/cornus/bareshim.go` - hidden shim subcommand.
- `pkg/deploy/internal/hostrun/` - shared runtime machinery.
- `pkg/e2e/`, `e2e/scenarios/`, and `e2e/container/entrypoint.sh` - bare target and scenarios.

## Test Coverage

- Root-free unit tests cover recovery decisions, netns rewriting, restart-code policy, cgroup parsers, and shim control fallbacks.
- The privileged bare E2E subset covers deploy, lifecycle/restart/server-restart/reboot recovery, exec/TTY, logs, port forwarding, CNI/DNS/volumes, Stats, and egress/mount companions. It runs against both in-process and shim supervision.

## Pitfalls

- The shim and server currently perform independent record read-modify-write cycles. A rare stop-during-crash-loop race can resurrect an explicitly stopped workload; coordinate both with a per-record lock before making the shim default.
- Companion reboot recovery is deferred: a companion must be repointed to the rebuilt application netns rather than given its own netns.
- Nested/DinD mount-propagation limits are environmental; do not mistake a spawn-level companion test for a data-content proof.
