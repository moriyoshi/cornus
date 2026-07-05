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
- The privileged bare E2E subset covers deploy, lifecycle/restart/server-restart/reboot recovery, exec/TTY, logs, port forwarding, CNI/DNS/volumes, Stats, and egress/mount companions. It runs against the IN-PROCESS supervisor only. (Corrected 2026-07-28: `BareTarget.ServeEnv` never sets `CORNUS_BARE_SHIM` and the CI bare leg does not either, so every green bare run to date has exercised the in-process path. `deploy-reboot-survival.star`'s `pkill -9 -f 'bare-shim'` has always been a no-op. A shim variant leg is cheap — env propagates through `append(os.Environ(), ServeEnv()...)` — it simply has to be run.)

## Pitfalls

- RESOLVED 2026-07-28: the shim and server performed independent record read-modify-write cycles, now serialized by an advisory `flock` on `<recordDir>/record.lock` — a STABLE sibling path, never `record.json`, which is rename-published so a lock on it locks an inode the next write unlinks. flock is per open file description, so the one mechanism covers the in-process supervisor and the cross-process shim alike; no mutex was added. All writes go through `updateRecordAt` (lock -> RE-READ -> mutate -> publish); the re-read is half the fix, since the lock alone still permits writing back a stale copy.
  The earlier characterization was WRONG IN TWO WAYS. The race was not rare and not shim-specific — both failures were reachable in the DEFAULT in-process configuration: (a) `supervisor.onExit` re-read the record, ran a seconds-wide `runc` restart, then wrote back the copy read BEFORE it, clobbering a `Stop` that landed in the window so startup reconcile relaunched an explicitly stopped deployment; and (b) both server-side writers staged through the same `record.json.tmp`, so truncating it after the other renamed it truncated the RECORD — and `listRecords` skips unreadable records, so the instance silently vanished from Status/List while its container kept running.
- Companion reboot recovery is deferred: a companion must be repointed to the rebuilt application netns rather than given its own netns.
- Nested/DinD mount-propagation limits are environmental; do not mistake a spawn-level companion test for a data-content proof.

## Coverage, Record Locking, and Supervision

Daemon-free tests raised package coverage from roughly 39.5% to 68.4% without a
production seam change. Real helper processes exercise shim stop/alive behavior
and true exit-code reporting. `--tail 0` now seeks to EOF while preserving future
follow output; negative tail remains the all-history sentinel.

All record mutations use an advisory flock on the stable
`<recordDir>/record.lock`, re-read the current generation under the lock, mutate
only owned fields, and publish by rename. The lock covers API handlers,
in-process supervision, and the cross-process shim. Holding it across runc,
backoff, or process waits would introduce deadlocks and is forbidden.

Keep the in-process supervisor as the default. The shim lacks a dedicated E2E
leg, between-restart liveness monitoring, stable-run backoff reset, and complete
companion reboot recovery. Local bare setup therefore recommends and can generate
a systemd unit: when Cornus exits, no external daemon remains to enforce workload
restart policy.

The field audit distinguishes bare from containerd where behavior differs:
managed DNS and restart-attempt caps are implemented here. Hostrun emits shared
warnings for unsupported SELinux relabeling and volume sizing/driver metadata.
