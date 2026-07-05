# The cross-backend deploy.Backend contract

## Summary

The three deploy backends (dockerhost, containerdhost, kubernetes) implement one
`deploy.Backend` interface, and a method-by-method consistency audit (three
fresh-context review agents: lifecycle/identity, data plane, spec-field coverage +
config + docs) established what the contract actually guarantees. The audit's
divergences were then fixed or documented, tightening the contract with shared
sentinels and helpers in `pkg/deploy/`.

## Key Facts

- `deploy.ErrNotFound` (`pkg/deploy/deploy.go`): all three backends wrap it from
  Stop/Start/Restart on a missing name; `Delete` stays delete-if-exists;
  `handleDeployAction` maps it to 404.
- `deploy.ParseSince` (`pkg/deploy/since.go`): shared `--since` grammar (docker
  `GetTimestamp`: unix[.nanos] / RFC3339 / durations-ago; `"0"` = epoch), wired into
  all three backends.
- Non-TTY Logs, exec, and attach output MUST be stdcopy-framed on every backend (the
  framing contract is documented on the interface; kubernetes was the violator,
  fixed via `muxWriters`).
- Command/Entrypoint semantics are uniform: `spec.Command` is args to the image
  ENTRYPOINT; `spec.Entrypoint` overrides it (docker semantics on all backends).
- Host-port publishing with `replicas>1` is replica-0-only on every host backend
  (one DNAT target per host port); kubernetes Services are per-deployment anyway.
- `Delete` reaps anonymous volumes on all backends (`docker rm -v` parity, promised
  in `pkg/api/deploy.go`).
- State vocabulary is documented on `Backend.Status`, not normalized: docker 7
  states / containerd 4 / kubernetes only `running|pending`; common subset is
  `running` + the Running bool.
- Shared helpers: `pkg/deploy/hostpolicy` (privilege policy, extracted from
  dockerhost; error text names the backend) and `deploy.Bridge`
  (`pkg/deploy/bridge.go`, the half-close stdio splicer promoted from dockerhost).
- `localBackend()` (`cmd/cornus/commands.go`) honors `CORNUS_DEPLOY_BACKEND` for
  `containerd`; it deliberately falls through to dockerhost for `kubernetes`
  (documented, with a `slog.Warn` on unrecognized values).

## Details

### What the audit found consistent

Ownership labels; `Replicas`/`RestartPolicy` defaults via the shared helpers;
recreate-on-Apply (host backends); the first-instance convention; hostpolicy
privilege gating (kubernetes has an equivalent path); volume ReadOnly + named/anon +
seed-when-empty semantics; UDP port mappings (a suspected drop did not exist); Logs
stdcopy framing; Delete/Status of a nonexistent name (uniform no-op/empty);
managed-network GC; cp PathStat/tar shapes where supported.

### High-impact divergences found (ranked) and their resolutions

1. dockerhost `replicas>1` + published ports was broken: one `createBody` reused per
   replica gave every replica the same `PortBindings` -> "port already allocated"
   (the unit fake never conflicted, pinning the bug). Fixed: host ports publish on
   replica 0 only (containerd parity); replicas 1+ get a `PortBindings`-less copy of
   the create body. The fake now models dockerd's port lifecycle (allocate at start,
   conflict = 500, release on remove) so the old bug fails tests.
2. kubernetes silently ignored `spec.Restart` (pods always `Always`). Fixed with
   `warnUnsupportedRestart`: warns for `no`/`on-failure[:N]`; `unless-stopped`
   counts as honored (Stop scales to zero).
3. kubernetes command-only specs dropped the image ENTRYPOINT (spec Command mapped
   to k8s `Command`, which overrides the entrypoint) — compose `command:` is exactly
   this shape, a silent behavior change docker -> k8s. Fixed: `spec.Command` -> k8s
   `Args` always; k8s `Command` is set only from `spec.Entrypoint`.
4. containerd had no inter-container DNS and no warning about it. Fixed twice over:
   a per-deploy `slog.Warn` for unsupported network knobs, then nerdctl-style
   hosts-file sync closing the DNS gap itself (see the containerd-backend LTM doc);
   `Driver`/`DriverOpts` remain warn-only there.
5. kubernetes non-TTY exec/attach output was not stdcopy-framed (its own Logs was),
   and exec dropped Env/WorkingDir/User/Privileged silently; ExecInspect reported
   Running before start and never a Pid. Fixed: `muxWriters` framing for both;
   `ExecCreate` warns per-field for the unhonorable options (no `sh -c` wrapping —
   containers may lack a shell); ExecInspect lifecycle corrected (Running =
   started && !done; Pid stays 0, documented).
6. Nonexistent-name Stop/Start/Restart split three ways (dockerhost silent success
   via an empty forEachInstance, containerd a bespoke error, kubernetes a raw
   apierror). Fixed with the shared `deploy.ErrNotFound` sentinel: all three wrap it,
   the contract is documented on the Backend interface, `handleDeployAction` maps it
   to 404, and `streamErrStatus` prefers `errors.Is` over substring matching. A
   caller audit found no reliance on the old nil-for-missing behavior.
7. dockerhost `Delete` leaked anonymous volumes (`containerRemove` lacked `v=1`),
   violating the explicit `docker rm -v` parity promise in `pkg/api/deploy.go`.
   Fixed: `v=1` is sent; containerd/kubernetes already reaped them.

### Medium/low divergences and their status

- `--since` parsed three ways (dockerhost unix/RFC3339/durations; containerd erred
  on durations; kubernetes silently ignored non-integers and returned ALL logs).
  Fixed with shared `deploy.ParseSince` in all three backends: garbage `since`
  errors everywhere, durations work on containerd.
- The server flushed 200 before Logs/Stats output, swallowing pre-output errors
  (e.g. kubernetes `docker stats` -> empty 200 instead of its not-supported error).
  Fixed with a lazy-header writer in `pkg/server/deploy.go` and
  `pkg/dockerproxy/containers.go`: 200 + flush happen on the backend's first write;
  pre-output errors return a real status (not-found -> 404, unsupported -> 501,
  invalid since -> 400, else 500). The attach/wait flush-header-early protocol is
  untouched (docker run depends on it).
- containerd StatsJSON lacked `memory_stats.stats`/`networks`/`blkio_stats` (docker
  CLI showed inflated MEM, zero NET/BLOCK I/O). Fixed (see containerd-backend doc).
- State vocabulary divergence: documented on `Backend.Status` instead of adding a
  normalization layer. Known asymmetries that remain by design: stopped shows as
  `1/1 exited` on host backends vs `0/0` on kubernetes (fabricated instance IDs);
  Restart on a stopped deploy resurrects on host backends but not on scaled-to-zero
  kubernetes; cp works on stopped containers only on dockerhost (containerd needs a
  running task, kubernetes is unsupported); a localhost image ref of an unpushed
  image works on containerd (local-store fallback), fails on dockerhost, and on
  kubernetes "localhost" means the node.
- Doc bugs fixed: README "user networks ... on both backends" (three backends;
  containerd limitation stated), `pkg/api/deploy.go` `Replicas` doc (all backends;
  replica-0-only publish) and `Command` doc (uniform args-to-ENTRYPOINT).

### Shared helpers extracted

- `pkg/deploy/hostpolicy` — the privilege policy formerly private to dockerhost
  (privileged/bind-mount gating). Error text names the backend; dockerhost keeps
  type aliases so callers did not churn. Both host backends use it; kubernetes has
  an equivalent path.
- `deploy.Bridge` (`pkg/deploy/bridge.go`) — the half-close stdio splicer (stdin
  EOF -> CloseIO/half-close) promoted from dockerhost with its regression tests;
  used by dockerhost and containerd exec/attach.

## Files

- `/home/moriyoshi/src/cornus/pkg/deploy/deploy.go` — `Backend` interface with the
  documented contract (ErrNotFound semantics, framing, state vocabulary),
  `deploy.ErrNotFound`.
- `/home/moriyoshi/src/cornus/pkg/deploy/since.go` (+ `since_test.go`) —
  `deploy.ParseSince`.
- `/home/moriyoshi/src/cornus/pkg/deploy/bridge.go` (+ `bridge_test.go`) —
  `deploy.Bridge`.
- `/home/moriyoshi/src/cornus/pkg/deploy/hostpolicy/policy.go` (+ `policy_test.go`).
- `/home/moriyoshi/src/cornus/pkg/deploy/dockerhost/dockerhost.go` — replica-0-only
  publishing, `containerRemove` `v=1`, ErrNotFound wrapping.
- `/home/moriyoshi/src/cornus/pkg/deploy/kubernetes/kubernetes.go` —
  `warnUnsupportedRestart`, Command->Args mapping, `muxWriters`, ExecCreate
  warnings, ExecInspect fix, ErrNotFound wrapping.
- `/home/moriyoshi/src/cornus/pkg/server/deploy.go` and
  `/home/moriyoshi/src/cornus/pkg/dockerproxy/containers.go` — lazy-header
  stream-error surfacing, `streamErrStatus`.
- `/home/moriyoshi/src/cornus/pkg/api/deploy.go` — `Replicas`/`Command` doc
  contract; the `docker rm -v` parity promise.
- `/home/moriyoshi/src/cornus/cmd/cornus/commands.go` — `localBackend()` doc comment
  + warn on unrecognized `CORNUS_DEPLOY_BACKEND`.

## Test Coverage

- dockerhost: the unit fake models dockerd's port lifecycle (allocate at start,
  conflict = 500, release on remove), so the multi-replica port bug regresses
  loudly; anonymous-volume reaping and ErrNotFound covered in
  `pkg/deploy/dockerhost/dockerhost_test.go`.
- kubernetes: framing/exec/restart-warning/Args-mapping covered in
  `pkg/deploy/kubernetes/kubernetes_test.go` (see the kubernetes-backend LTM doc).
- Shared: `pkg/deploy/since_test.go`, `pkg/deploy/bridge_test.go`,
  `pkg/deploy/hostpolicy/policy_test.go`.
- Server stream errors: lazy-header behavior covered in `pkg/server` tests; the
  behavioral note is that quiet follow-mode clients now get headers at first output.

## Pitfalls

- Never return nil from Stop/Start/Restart for a missing name or leak a raw backend
  error — wrap `deploy.ErrNotFound` so the server can 404 via `errors.Is`.
- Never hand-parse `--since`; use `deploy.ParseSince` (silently ignoring a bad value
  returns ALL logs, the original kubernetes bug).
- Non-TTY exec/attach output must be stdcopy-framed even when the transport is a
  raw stream — clients demux unconditionally.
- When faking a daemon in unit tests, model the resource lifecycle (the dockerhost
  port fake accepted duplicate `PortBindings`, hiding a real conflict for months).
- A backend that cannot honor a spec/exec field must warn per-field, not silently
  drop it; do not "fix" missing exec Env/WorkingDir with `sh -c` wrapping —
  containers may lack a shell.
- Host-port publishing on multi-replica deploys is replica 0 only; do not copy a
  create body carrying `PortBindings` to other replicas.
- Status states are backend-specific by documented design; only `running` (and the
  Running bool) is portable across backends.
