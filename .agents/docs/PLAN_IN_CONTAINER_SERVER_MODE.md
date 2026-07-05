# In-container server mode for the dockerhost and containerd backends

Status: consolidated into `ARCHITECTURE.md` and `.agents/docs/JOURNAL.md` on
2026-07-26. Retained as the historical implementation plan; its phase labels and
status below are superseded by the canonical architecture and the journal's
"Consolidated in-container server-mode plan" entry.

## The problem

`README.md` advertises running the server as a container against the host's Docker
daemon, but nothing supported that topology. Every path cornus hands a container
runtime — a bind source, a volume backing dir, a pinned netns, a log file, the
log-shim binary — is resolved by that runtime in the HOST's mount namespace. Run
the server on the host and the two agree, which is why nothing needed this before.
Run it in a container and they diverge **silently**: the runtime binds a path that
means something else there, or nothing at all, the workload starts, and the
expected data is simply absent with nothing in any log.

Concretely, before this work:

- **dockerhost** worked for plain deploys and silently degraded for client-local
  mounts (an empty directory in the workload, no error).
- **containerd** was broken outright: the log URI hands containerd's host-side
  shim `os.Executable()`, a path that does not exist on the host.
- No detection, no translation, no preflight; the only acknowledgement was a prose
  warning in `ARCHITECTURE.md` about binding `<DataDir>/mounts` `rshared`.

## The model

**The server container is itself the caretaker for every workload on that host.**
It gets the host coupling it needs (runtime socket, an `rshared` bind of its data
dir, later host networking) and keeps doing every host-side operation in-process,
exactly as when run as a host process. No companion or sidecar is started for this
scenario.

So an in-container server stays on the **co-located** side of `useSidecarMounts`
(`pkg/server/deploy_attach.go`): `Remote()` stays false, the unified per-replica
companion is not used, and only the paths handed to the runtime are translated.
`CORNUS_DOCKER_REMOTE` / `CORNUS_CONTAINERD_REMOTE` are untouched and never
auto-enabled — they remain the answer for a genuinely *remote* daemon, and would
run a companion per instance to solve a problem a co-located host is not having.

Decisions taken: `--network host` for containerd is **deferred** (preflight warns,
never fails), and containerd's path translation is in scope while its netns/CNI
work is not.

## What landed (phases 1-3)

**`pkg/hostenv`** — detection and translation. `Detect` returns an `Env` and a
`Mapper`; `Mapper.ToHost` translates a path into the host's spelling and reports
`ok=false` when it is not host-visible at all, so callers fail loudly instead of
binding nothing. Mapping sources, operator override first:

1. `CORNUS_HOST_PATH_MAP=/var/lib/cornus=/srv/cornus,...` — authoritative, and the
   only mechanism when the runtime cannot be asked.
2. Docker self-inspection — resolve our own container id from `/proc`, inspect it,
   and build the map from `Mounts[]`.

The load-bearing subtlety is `Env.Translating`. Being in a container does **not**
imply divergence: a cornus containerized *alongside* its runtime (the E2E runner's
dockerd-in-the-same-container shape) shares that runtime's mount namespace and must
keep behaving exactly as before. Divergence is claimed only on evidence — an
explicit map, or a runtime that confirms it created *this* container. Absent both,
`Detect` returns the identity mapper. Candidate container ids are confirmed against
our own mount table before being trusted, because a wrong id yields a confident,
wholly incorrect map.

**`pkg/hostcheck`** — the operator-facing verdict, calibrated to what actually
breaks rather than to how unusual the configuration looks:

- `StatusFail` (server refuses to start) only where deploys would *silently* do the
  wrong thing. Today that is exactly one case: containerd with an unmappable data
  dir, since it hands the runtime a path under it for every deploy.
- `StatusWarn` for a capability that is merely unavailable and reports its own
  absence at the point of use (client-local mounts on dockerhost, propagation that
  is not `shared`, containerd without host networking).
- `bare` and `kubernetes` skip the path checks entirely: bare execs its OCI runtime
  as cornus's own child (same mount namespace), kubernetes hands the server's paths
  to nothing.

**Wiring** — `pkg/server/hostpreflight.go` detects once in `New` (fatal on an
unusable environment), logs a summary plus every warning with its remedy at serve
time, and exposes capability flags on `/.cornus/v1/info`. That endpoint is
auth-exempt, so it carries flags only — no container id, no paths.
`cornus daemon preflight` runs the identical detection and checks, and exits
non-zero on a configuration `cornus serve` would refuse, so an image smoke test can
gate on it.

**dockerhost translation** — `pkg/server`'s `hostVisibleMountSources` rewrites the
mountpoints the server just minted (those under `<DataDir>/mounts`) into their host
spelling before `Apply`, and hard-errors when one is not host-visible. A user's own
bind source is a host path by definition and passes through untouched.
`mountBindPrefixes` then permits BOTH spellings in the backend's default-deny
policy — without it the server's own carve-out rejects its own mounts the moment
translation is in effect. A failed `ForwardPort` dial now explains itself when the
server has no route to the workload's network.

## Outstanding

- **Phase 4, containerd**: the staged content-addressed log-shim binary (the
  blocker), plus the mapper through `hostrun`'s `VolumeStore`/`HostsStore`/
  `OCIBindMount`, `logPath`, and the companion scratch dirs
  (`caretakerMountsDir`/`caretakerAgentDir`, reached by any egress deploy, not just
  remote mode). CNI plugin presence preflight.
- **Phase 5**: `cornus setup` container-install scenarios, a guide page,
  `CORNUS_HOST_PATH_MAP` in the env-var reference, README and `ARCHITECTURE.md`
  updates, ja/zh.
- **Phase 6**: E2E targets that start the server AS a container against the
  containerized runner's existing dind/containerd.
- **Deferred**: `--network host` as a hard containerd requirement and the netns/CNI
  work behind it; a co-located host-mount fallback for containerd (it has none, so
  client-local mounts there stay companion-only); `barehost`/`incushost`.
- **Dropped as speculative**: an auto-derived in-container default for the
  advertise/registry host. Under this model no companion dials back, so
  `CORNUS_ADVERTISE_URL` barely matters here; what the daemon needs to pull images
  is `CORNUS_ADVERTISE_REGISTRY`, already an explicit knob. Guessing a published
  port and bridge gateway would add a failure mode, not remove one.
