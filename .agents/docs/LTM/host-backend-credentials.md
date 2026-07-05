# Host-Backend Credentials: What Landed, and Why the Rest Is Blocked

Status: **ALL THREE delivery kinds now work on the host backends with no caretaker
(2026-08-07)**: env on dockerhost/podman, containerd and bare; file on
dockerhost/podman and bare; endpoint on all three. incus does none.

The "endpoint is the one kind that genuinely needs the server to be a caretaker"
conclusion this note used to carry was WRONG, and the correction is the most
useful thing here — see "Endpoint: why the blockers dissolved" below.

## The bug this started from

A `cornus compose up` against an in-container dockerhost server failed with:

```
deploy shell: shell: client-local mounts and client-sourced credentials on the
dockerhost backend require CORNUS_ADVERTISE_URL (the cornus URL the caretaker dials back on)
```

The message named the wrong cause twice over. Supplying `CORNUS_ADVERTISE_URL`
moved the failure twenty lines further, into the backend's blanket
`len(creds) > 0` rejection. And the deploy needed no caretaker at all.

Two defects stacked:

1. **The dispatch never asked whether a companion was needed.**
   `pkg/server/deploy_attach.go` routed on `hasCreds || hasEgress`, with no test
   of what the credentials actually required. Compare `useSidecarMounts` in the
   same file, which *does* ask the equivalent question for mounts and answers it
   correctly — "a cornus containerized on the daemon's OWN host is not the remote
   case". That reasoning existed for mounts and was absent for credentials.
2. **Host-backend credentials were unimplemented**, so even the corrected route
   had nowhere to land.

## The distinction that resolves it

Not "caretaker vs no caretaker" but **session-scoped vs netns-scoped**.
`caretaker.Config.Instance`'s own doc already said it: *"Roles that are
session-scoped (Mounts, Credentials) don't need it."*

| Delivery | Needs a process beside the workload? | Where it is realized |
| --- | --- | --- |
| `env` | no | resolved once at deploy time, merged into `spec.Env` |
| `file` | no | server renders and binds; refreshed by a symlink swap |
| `endpoint` | no | server binds a listener INSIDE the workload's netns and serves it |

`file` looks like it needs one and does not, and that is the whole insight. It
decomposes into *produce the bytes*, which needs the deploy-attach session and so
belongs to the server, and *make them visible in the workload*, which needs a path
the runtime resolves — which the server already has. On kubernetes one caretaker
does both because it is the only thing inside the pod; here the halves are already
in the right places.

`deploy.NeedsCaretaker(d, serverFiles)` is the single predicate, deliberately: the
server's split of a source's deliveries, the server's companion decision, and each
backend's rejection were separate `Kind ==` comparisons, the shape that lets one
drift and silently disagree. It takes a capability rather than being a constant
because the answer for `file` genuinely differs by backend.

**env needs no co-location either**, which is the simplification that made phase 1
small. The value never becomes a host path, so there is no mapper, no
`hostpolicy`, and no predicate problem — it works on a remote daemon too.

## Five claims that subagent verification refuted

Recorded because each one was plausible, and four of the five would have shipped
a defect rather than a compile error.

1. **`hostpolicy` default-denies a bind of a server-owned credential directory —
   which constrains ONE DESIGN, not the feature.** All four host backends run
   `Policy.Validate` over `spec.Mounts`, and the only carve-out is
   `mountBindPrefixes` (`pkg/server/hostpreflight.go`) = `MountsDir` alone.
   Widening it would be a security regression: `AllowBindPrefixes` is prefix-based
   with no per-deploy scoping, so any caller of the deploy API could bind another
   deployment's credential directory as an ordinary mount source.

   **But `Policy.Validate` inspects only `spec.Mounts`**
   (`pkg/deploy/hostpolicy/policy.go:65`), so a delivery that creates no mount
   never meets the policy at all. This was recorded as a blocker on *file
   delivery* when it is a blocker on *delivering files by bind-mounting a
   server-owned directory*. The distinction matters: it is the difference between
   "this needs a security decision" and "pick a different mechanism". See the
   `CopyTo` row below for the mechanism that avoids it.
2. **No sound co-location predicate exists.** `hostcheck.UsesHostMountFastPath`
   excludes containerd deliberately and also gates the genuinely 9P-specific
   `propagationCheck`, so it cannot be widened. And `!Remote()` is not a
   co-location test — `useSidecarMounts`'s own doc says a remote daemon "cannot be
   detected" — so `DOCKER_HOST=tcp://` and `CORNUS_PODMAN_SOCKET=ssh://`
   (`podman_ssh.go`) both pass it while non-co-located.
3. **A unix socket cannot be advertised.** `Endpoint.Serve` taking a
   `net.Listener` is real and nothing in `generic` assumes TCP — but `Serve` is a
   third of the contract. `Env(name, addr)` and `WellKnownAddr()` are `host:port`
   by documented contract and every provider renders `"http://" + addr`. There is
   nowhere to put a socket path. Separately, `githubproxy` sets
   `RewriteUpstreamURLs: true`, activating `authproxy.go`'s
   `"http://" + ln.Addr().String()` — which on a unix listener renders the socket
   PATH into `Location`/`Link` headers. Silently unroutable; no error, no compile
   break.
4. **The nftables DNAT sketch was unsound**, in four independent ways:
   `netredirect.Setup` deletes the whole `cornus` table on every call, so a rule
   added by another path is wiped on the next egress converge; the existing
   redirect is a catch-all last rule with no daddr condition, so an appended DNAT
   never fires; the table only exists when transparent egress is on, so a
   credentials-only workload has none; and `Setup` takes no netns parameter.
   Netns *repair* after a reboot also mints a fresh namespace, discarding any nft
   state. And well-known addresses **already work** by address-binding
   (`ensureLocalAddr` adds 169.254.169.254 to `lo` via `vishvananda/netlink`), so
   DNAT would replace a working mechanism rather than fill a void.
5. **`State.Pid` is not decoded** anywhere in `pkg/deploy/dockerhost` — the
   inspect decode is an anonymous struct with three fields. The only `Pid` in the
   package is *exec* inspect, a transient process.

One correction to a claim made in the other direction: **`nftables.WithNetNSFd`
does not avoid `setns`.** `mdlayher/socket` spawns a goroutine, calls
`runtime.LockOSThread`, `setns`, creates the socket and restores. The caller does
not manage threads; `CAP_SYS_ADMIN` remains the gate. Two sharp edges if this is
ever used: an fd of `0` is silently treated as "no netns" and programs the HOST
ruleset, and without `AsLasting()` the Conn re-dials per `Flush()`, so the fd must
outlive the whole `*Conn`.

## The mechanism that was tried and discarded: `CopyTo`

Recorded because it is the obvious answer and it is worth knowing why it lost.

Every backend implements `CopyTo(ctx, name, path, r, opts)` — a path-free way to
put bytes inside a workload through the runtime's own file-transfer mechanism. It
sidesteps claim 1 completely: no host path, so no `hostpolicy`, no `hostenv`
mapper, no co-location predicate. It was built and then removed.

What it does not sidestep is **when** it can run:

| Backend | Mechanism | Works on a *created* container? |
| ------- | --------- | ------------------------------- |
| `dockerhost` / `podman` | Docker archive PUT (`containerArchivePut`) | **yes** |
| `containerd` | `procRoot` = `/proc/<pid>/root` | no — needs a running task |
| `bare` | same | no — needs a running task |

A credential must exist before the application's first process runs, or the read
races the write. Only dockerhost has a create→start seam, so the copy design was
dockerhost-only by construction, and it needed an explicit re-PUT for every
refresh. The bind design covers bare as well, and refresh is a symlink rename.

`copyUIDGID=1` on the archive PUT makes Docker honour the tar header's uid/gid —
worth remembering if this is ever revisited for a remote daemon, which is the one
topology the bind cannot serve.

## The `netredirect` limits are refactorable, not constraints

Recorded because they were once cited as reasons a link-local DNAT could not be
added, and they are not. All four are properties of a package that has only ever
had one caller:

- `Setup` deletes the whole `cornus` table on every call (`redirect_linux.go:35-38`)
  — a consequence of being the sole writer, not a requirement.
- The final rule matches `meta l4proto tcp` with no destination condition and
  issues a `Redir` (`:105-110`), so anything appended after it never fires.
  Insertion order is ours to choose.
- The table exists only when transparent egress is on, because egress is the only
  caller.
- `Setup` takes no netns parameter — a parameter.

What a refactor would *not* change: workload-side rules still cannot authenticate a
workload to the server, and `CAP_SYS_ADMIN` plus a per-backend netns handle remain
required for any cross-namespace use.

## Why nft in a workload's netns cannot authenticate

Recorded so the idea is not re-derived. It is a natural one — cornus already
programs nftables into workload namespaces (`pkg/netredirect`, via netlink with no
CLI dependency) — but it solves the wrong direction.

Rules in workload W's netns only ever evaluate W's own packets. A hostile peer's
connection never traverses W's netns, so W's rules are not on the path. Applying
them to *every* workload cornus creates does contain a co-tenant, but that fails
against any container cornus did not create (a plain `docker run --network
myproject_default`), against `privileged: true` (which returns `NET_ADMIN`), and it
scales as workloads x credentials.

For the server to *learn* something it must be in the packet, and nothing nft can
set is both visible and unforgeable:

- **`meta mark`** — the instinctive answer, and cornus uses `SO_MARK` for its own
  egress exemption — is kernel metadata inside one namespace. It is never
  serialized onto the wire.
- source IP, source port, DSCP, TTL are visible but a peer picks its own.
- conntrack state and labels are local to the tracking namespace.

This is why Kubernetes enforces NetworkPolicy at the CNI/node layer rather than in
pods, and why meshes prove identity with mTLS rather than netfilter. The house
answer already exists here: `CORNUS_CARETAKER_TOKEN` is a scoped bearer token and
mount session ids are unguessable capabilities. Ranking for any future TCP-served
credential: **kernel-enforced possession (a bind-mounted unix socket) > capability
token in the payload > source IP**. A DNAT is address translation, not identity —
it leaves the source address intact, so it composes with a peer check rather than
replacing one.

## What remains, and what each is blocked on

- **file**: DONE for dockerhost/podman and bare. The design that landed is neither
  the bind-of-a-server-dir this note first analysed nor the archive-PUT copy that
  briefly replaced it — it is the bind, placed under `MountsDir`, which is what
  dissolves the objections rather than working around them. `Policy.Validate`'s
  allow-list already covers that prefix (`mountBindPrefixes`), and
  `hostVisibleMountSources` already translates sources under it, so the policy
  carve-out and the mapper generalization both become unnecessary; the session id
  in the directory name is the same unguessable capability the 9P mountpoints
  beside it rely on. Refresh uses Kubernetes' atomic-writer shape — a versioned
  directory and one `..data` symlink swapped by rename — because a bind pins an
  inode, so rename-over would leave the workload reading the dead one forever.
  Two residual caveats, documented rather than fixed: the bind covers the
  credential's DIRECTORY, so anything the image had there is hidden (Kubernetes
  makes the same trade); and remote mode declines the capability outright, because
  Docker creates a missing bind source rather than refusing and the workload would
  come up with an empty directory where its credential should be.
- **containerd**: still refuses file deliveries. Not for the `procRoot` reason —
  that belonged to the archive-PUT design — but because it is absent from
  `hostcheck.UsesHostMountFastPath`, which gates the server path that does the
  translation. Wiring it is now small and worth doing.
- **endpoint**: RESOLVED 2026-08-07 — (3) never had to be solved. See below.
- **Refresh machinery**, if file ever lands: extract `credFetcher` / `credExpiry` /
  `parseTTL` re-parameterized on a `fetch func(ctx) (credential.Credential, error)`
  so both paths share TTL semantics, plus a supervisor — the caretaker's
  `runCredential` runs under `supervisor.Restart` and the server has no equivalent.
- **`fetchCredentialValue` does not call `sess.AllowsCredential(name)`**, unlike
  both relay paths. Harmless for a one-shot deploy-time fetch; not harmless once it
  becomes a long-lived enforcement point.
- **`incushost`** is a fourth host backend, is not an `AttachingBackend` at all, and
  rejects credentials separately. It needs its own decision.
- **Multi-replica cornus on a host backend** would break a server-side realization:
  the cross-replica credential relay (`relayCredentialRemote`) is gated on
  `hubDistributed()`, false for a local hub. Not a supported topology today; stated
  rather than discovered later.

## Related documents

- [in-container-server-mode.md](./in-container-server-mode.md) — the topology this
  bug was reported from, and the D3/selfnet routing work it depends on.
- [caretaker-transport-and-hub-synthesis.md](./caretaker-transport-and-hub-synthesis.md)
  — the relay the companion dials, and what `CORNUS_ADVERTISE_URL` addresses.
- [client-local-mounts-deploy.md](./client-local-mounts-deploy.md) — the co-located
  9P fast path the new credential path was modelled on.
- [deploy-backend-contract.md](./deploy-backend-contract.md) — `AttachingBackend`
  and the optional-interface surface.

## Endpoint: why the blockers dissolved (2026-08-07)

This note recorded three blockers on endpoint delivery. All three dissolved on
re-verification, and they dissolved for ONE reason, which is worth stating before
the individual answers.

**On kubernetes the endpoint binds `127.0.0.1:<port>` inside the POD's netns**
(`kubernetes.go:3990`). The caretaker can serve it because it shares the pod's
network namespace, and the workload is authorized to reach it by nothing more than
sharing that namespace. **The netns boundary IS the authorization model.** There
was never a network-level trust question to design — which was the blocker I had
been calling structural, and the one that made the other two look load-bearing.

1. **"`Endpoint.Env` is `host:port` by contract"** — true, and irrelevant. Binding
   in the workload's own namespace makes `127.0.0.1:<port>` exactly right. The
   unix-socket problem existed only because I had assumed the server must serve
   from its OWN namespace and hand the workload some other kind of address.
2. **"`serveCredEndpoint` hardcodes `net.Listen("tcp", ...)`"** — caretaker code.
   It says nothing about what a server-side path can do.
3. **"needs an authorization model"** — the namespace is the capability, as above.
   A listener bound there is reachable by that workload and nothing else on the
   host, so the guarantee is the caretaker's rather than a weaker substitute.

**Generalization worth keeping.** Three times in this line of work I recorded an
implementation artifact as a design verdict: netredirect's four "limits" (all
artifacts of one caller), the `setns` claim, and these. The tell is the same each
time — writing "this cannot be done" when what was established is "the one existing
caller does not do this". Ask which is being claimed before recording it here.

### What IS real: an ordering constraint, splitting the OPPOSITE way from file

`hostrun.CNIManager.Setup` calls `netns.NewNetNS` — cornus creates and pins the
namespace itself, attaches CNI, THEN creates the container with that path
(`containerdhost/lifecycle_linux.go:232` vs `:296`). So containerd and bare have a
bind point before the app's first process. dockerhost does not: Docker owns the
namespace and creates it at container start, which is why its companions join with
`NetworkMode: container:<app>` and need the target already running.

Note the inversion against file delivery, where dockerhost and bare can and
containerd cannot. "Host backend" is not a capability; ask per capability.

The startup race that follows on dockerhost was ACCEPTED (user's call) rather than
engineered around, because a connection refused is retryable and every IMDS/SDK
client already retries — where a file is opened once at startup with no second
chance. That distinction is what let all three backends share one code path.

### Shape

- `pkg/netnsbind` — `Listen(nsPath, network, addr)` and `EnsureLocalAddr(nsPath, ip)`
  via `ns.WithNetNSPath`. A socket belongs to the namespace it was CREATED in, so
  the namespace is entered only for the bind; `Accept` runs normally afterwards.
  The caretaker's `ensureLocalAddr` is now a wrapper over the same code.
- `deploy.CredentialEndpointBinder` — `BindsCredentialEndpoints()` plus
  `InstanceNetns(ctx, name, replica)`. The latter must RE-RESOLVE on every call:
  a restarted dockerhost container has a new pid, and a namespace rebuilt by
  reboot recovery is a different object at the same path.
- `deploy.ServerDelivers` replaced the lone `serverFiles bool`. Two adjacent bools
  at a call site is where a swap compiles and silently misroutes a delivery, and
  the two capabilities genuinely differ per backend.
- Server assigns addresses BEFORE Apply (env is fixed into the create request) and
  binds AFTER (dockerhost has no namespace until start).

### dockerhost needs guards the other two do not

Its only handle is `/proc/<pid>/ns/net`, and the pid comes from the daemon. So it
is a pid in the DAEMON's namespace — meaningless in a containerized server's
`/proc` — and reusable if the container exited between inspect and open. Both
failures have the same shape, a valid-looking path naming the WRONG namespace, and
the same consequence: a live credential endpoint bound somewhere it was never meant
to be. `InstanceNetns` therefore checks the namespace differs from the server's own
(`os.SameFile` on the two procfs links) before returning it. That does not prove it
is the right container's namespace; it removes the outcome that matters.

### The bug this work found in the file path

`applyWithHostAttachments` re-derived the capability as a hardcoded `false` while
the dispatch had routed the deploy there using the backend's REAL capability, so a
`file` delivery — correctly materialized and mounted a few lines above — was
re-classified as caretaker-bound and the deploy died on `internal: file credential
delivery reached the co-located path, which serves none`. Every host file delivery
would have failed. It shipped green because **no test called
`applyWithHostAttachments` at all** — the pieces (layout, refresh, routing
predicate) were each tested and the composition between them never was.

Fixed structurally: the capability is computed once in the dispatch and passed in,
and the split+guard is extracted as `realizeCoLocatedCredentials` so a test can
reach it without HTTP plumbing. The lesson generalizes past this function: when a
change adds a capability that several steps must agree about, single-source it
rather than testing that they agree.

### Testing a netns bind without root

`go test ./...` must not need root, so the namespace tests skip unprivileged. This
host cannot grant it either (no passwordless sudo; unprivileged userns blocked by
AppArmor). Run them in a privileged container against a binary compiled AS THE
USER, so nothing root-owned lands in the Go cache:

```sh
go test -c -o ./.agents-workspace/tmp/netnsbind.test ./pkg/netnsbind/
docker run --rm --privileged -v "$PWD/.agents-workspace/tmp:/t:ro" ubuntu:24.04 /t/netnsbind.test -test.v
```

The confinement test's two halves catch DIFFERENT faults, which is why both stay:
the positive half ("the workload can reach it") catches a bind in the wrong
namespace; the negative half ("the host cannot") catches the plausible alternative
implementation — bind on the host and bridge it in with a DNAT rule — which passes
the positive half and would publish the endpoint to every process on the machine.

### A listener in a destroyed netns does not fail — it goes quiet

The single most surprising thing in this work, found by the E2E restart arm rather
than by reasoning.

A rebind loop that waits for `Serve` to return will never rebind after a workload
restart. **The listener holds a reference to its network namespace**, so destroying
the workload does not free the namespace, the socket stays open, and `Accept`
blocks forever on a namespace nothing can reach. `Serve` has no reason to return.
Meanwhile the restarted workload — in a brand-new namespace — gets connection
refused. It presents as a healthy server serving nobody.

The fix is to watch for replacement rather than wait for failure, and to compare
the namespace's IDENTITY rather than its path. On dockerhost the handle is
`/proc/<pid>/ns/net`; a REUSED pid re-creates that exact path pointing at a
different namespace, so an existence check reports healthy while the endpoint
serves a credential into somewhere it does not belong. `os.SameFile` against the
handle captured at bind time is the check.

Generalization: any code that binds a resource inside a namespace it does not own
needs an explicit liveness signal. The absence of an error is not one.

## Capability gates: ask the question you mean (2026-08-07)

Three separate defects in this line of work were the same mistake — a predicate
answering a question next to the one being asked.

- **`hostcheck.UsesHostMountFastPath` gating credential FILES.** It answers "does
  this backend realize client-local 9P mounts by having the server mount and the
  runtime bind the mountpoint". A credential directory involves no 9P. Using it
  excluded containerd and incus from a capability they had. The right question is
  `CredentialBinder` — which each backend answers about itself.
- **`!Remote()` as a co-location test.** `Remote()` reports the MODE an operator
  selected. `DOCKER_HOST=tcp://` and `CORNUS_PODMAN_SOCKET=ssh://` reach another
  machine with no mode set at all. `runtimeendpoint.Endpoint.NonLocal()` is the
  question actually meant, and both credential capabilities now require it.
- **"is this backend co-located" instead of "can this process do X".** Entering a
  workload's netns needs CAP_SYS_ADMIN, which a co-located but UNPRIVILEGED server
  does not have. Probe the operation (`netnsbind.CanEnter`, following
  `builderctr.CanMount`); privilege is not the same question as uid, and neither
  is the same question as co-location.

The general form: when a capability check is a proxy for the real question, it is
right until someone adds a second caller. Name what is actually being asked.

## Test the topology the bug lives in

Every credential E2E ran in the containerized runner, which is co-located with an
IDENTITY path mapper. Two bugs hid there and neither could have been caught by it:

- a mount the server never translated — invisible, because the untranslated and
  translated paths are the same string under an identity mapper;
- an endpoint the server had no privilege to bind — invisible, because the runner
  is root.

`make e2e-docker` (cornus as an unprivileged HOST process) found the second on its
first run. The first needed a unit test with a deliberately non-identity mapper.
A green containerized suite says nothing about either axis, so for anything
touching path translation or privilege, test out-of-container too.

Related trap, same session: a fake backend named `"fake"` reports
`UsesHostMountFastPath` TRUE, because `normalizeBackend` maps anything
unrecognized to the dockerhost default. A test using it passed with the fix
neutralized. When a predicate normalizes its input, a fake's NAME is load-bearing.

## Two engines, one result type (2026-08-07)

`containerInspectResult` is filled by TWO implementations — the Docker engine's
and `podmanEngine`'s — from different JSON shapes. Adding a field to one and not
the other produces NO error: both compile, both decode, and the zero value is
read downstream as a legitimate state. `State.Pid` went missing on the podman
side and surfaced as "instance is not running yet", forever, on a container that
was running and exec-able.

Guard: `TestBothEnginesDecodeEveryInspectField` drives the real engine over a fake
libpod endpoint and reflects over every field of the result type, so a field added
to one side fails until the other has it. Note it must call the ENGINE — a first
version rebuilt the mapping inline and passed with the fix neutralized.

## Rootless runtimes cannot read a server-owned credential file

Rootless podman and incus fail the same way for the same reason, and neither is a
wiring gap:

- rootless podman: the daemon runs as an ordinary user and cannot even traverse
  `<MountsDir>/creds-<session>/<n>` (`statfs: permission denied`, returned as a
  podman 500);
- incus: a host disk device is idmap-shifted, so the file arrives owned by
  `nobody`.

Loosening the DIRECTORY does not fix either: the 0600 file inside is owned by the
server's uid and unreadable by the user the container's root maps to. Both are
refused by name via `BindsCredentialDir`. ENDPOINT delivery works on both — a
listener is not a file, and it carries no ownership at all.

This is why `BindsCredentialDir`/`BindsCredentialEndpoints` take a context: two
backends have to ask the runtime a question to answer their own capability.

## Measured: what incus does to a host bind (2026-08-08)

The idmap claim that made incus refuse credential FILES was recorded from the
incusd-socket case. Measured directly for an ordinary DIRECTORY bind, against the
live incusd in the E2E runner, with a cornus server-host bind mount:

| configuration | ownership inside | write |
| --- | --- | --- |
| default | `65534:65534` (nobody) | permission denied |
| device `shift=true` | `65534:65534` — unchanged | permission denied |
| instance `raw.idmap "both 0 0"` | `0:0` | **succeeds** |

Host side throughout: `-rw-r--r-- 0 0`, container running as `uid=0(root)`.

Three things follow.

**The credential-file refusal on incus is confirmed by measurement**, not inferred
from the socket case. A 0600 root-owned file would arrive owned by nobody and be
unreadable. Even 0644 is only readable, never writable.

**`shift=true` is not the remedy** on this incus/kernel combination, so anything
built on it would fail exactly where it was supposed to help.

**`raw.idmap "both 0 0"` works but is the WRONG LEVER**, and the correction is
worth more than the original finding.

"nobody" is not an owner, it is the kernel's OVERFLOW uid — shown when a file's
owner is not mapped into the instance's user namespace at all. The instance maps
host 1000000 -> ns 0 over a range of 1e9 (`volatile.idmap.current`), and host uid
0 is outside it. That is also why the instance's root cannot write despite
CAP_DAC_OVERRIDE: a userns root holds capabilities only over uids INSIDE its map,
so an unmapped owner is beyond its authority entirely. `chmod 0666` would not
help either.

raw.idmap "fixes" this by dragging host root INTO the map, which is why it costs
isolation. The actual requirement is only that the owner be inside the map.
Measured: `chown 1000000:1000000` on the host directory, no raw.idmap, no config
change at all — the file reads as `0:0` INSIDE and writes succeed. The files then
belong to an unprivileged host uid that only this instance's namespace maps, so
nothing gains host privilege.

| remedy | works | cost |
| --- | --- | --- |
| `raw.idmap "both 0 0"` | yes | instance root becomes HOST root |
| chown into the mapped range | yes | none |

**Still unmeasured, and it is what a real implementation turns on.** The above
used a plain host directory. A client-local mount is a 9P mount, where ownership
inside comes from what cornus's 9P SERVER reports, not from chowning the
mountpoint — and `Mount9P` (pkg/deploywire/backing_linux.go) passes no uid
mapping at all today (`trans=unix,version=9p2000.L,msize=1048576`). Making a
9P-served file land inside the instance's range needs either `dfltuid=`/`dfltgid=`
mount options, an access-mode change, or a shift in the mount server. None of
those has been measured against incus.

So client-local mounts on incus are possible WITHOUT the isolation trade the
earlier version of this note recorded. What stands between here and them is a uid
mapping in the 9P layer, which is a smaller and much less dangerous problem than
raw.idmap.

## Measured: 9P mount options cannot carry an id mapping (2026-08-08)

Two layers could map ids for a client-local 9P mount: the MOUNT OPTIONS
(`dfltuid`/`dfltgid` on the 9p mount) or the 9P PLUMBING (cornus's server
translating the ids it reports). The first is one string in
`pkg/deploywire/backing_linux.go`; the second is real work. So the first was
measured before choosing.

**It is inert.** Mounting with `dfltuid=100999,dfltgid=100999` under
`version=9p2000.L`:

- the options ARE accepted and visible on the mount
  (`rw,relatime,dfltuid=100999,dfltgid=100999,access=client,msize=1048576,trans=unix`);
- the file still reads as `0:0` inside the container, unchanged from the baseline
  without them.

That is by protocol, not by accident: `dfltuid`/`dfltgid` are documented as a
fallback for when the SERVER supplies no ids, and a 9p2000.L getattr always
supplies them. `access=<uid>` is not an alternative either — it changes which
uid the client performs operations AS, not the ownership the mount presents.

So there is no choice to make: id mapping for 9P mounts has to happen in
cornus's own 9P server, in the ids it reports. `deploy.IDMapper` supplies the
map; what remains is applying it at that layer.

The decision is recorded at the mount site as well, because the missing
`dfltuid=` there looks like an oversight and is not.

## Client-local 9P mounts fail on rootless podman BEFORE ownership matters (2026-08-08)

Measured, and it redirects the 9P id-translation work rather than motivating it.

A client-local mount on the rootless podman leg does not produce a workload with
wrongly-owned files. It fails the DEPLOY:

```
create cornus-ninep-0: podman api: 500 Internal Server Error:
  statfs /tmp/.../server/mounts/sess-<id>/m0: permission denied
```

**It is not path permissions.** Every component is already traversable: the data
dir is 0711 (widened for credential files), `mounts/` is 0755, and both
`sess-<id>/` and `m0` are created 0755 by `pkg/deploywire/backing.go`. Widening
them further was tried and changed nothing, so that change was reverted rather
than shipped — a permission widening that buys no measured benefit is a cost with
no return.

**Cause not yet identified.** The two candidates worth testing first:

  - MOUNT NAMESPACE. Rootless podman runs in its own user AND mount namespace, so
    a 9P mount made in the server's namespace may not propagate to it. That is
    what `hostcheck`'s propagation check exists for on the other backends, and it
    would explain a failure at statfs time. The error being EACCES rather than
    ENOENT argues against it, but not decisively.
  - The 9P mount itself. `access=client` has the kernel do permission checks with
    the ids the SERVER reports, and everything on that mount is reported as the
    server's root.

**Consequence for the planned work.** Translating ids in cornus's 9P server was
the next item, on the premise that ownership inside the mount was the problem.
That premise is wrong for this configuration: ownership cannot be the problem
while the mount cannot be statfs'd at all. Identify this failure first — the id
translation may turn out to be necessary but it is certainly not sufficient, and
building it first would leave the deploy failing in exactly the same way.

### A 9P mount is denied to a second uid, and the cause is inside v9fs (2026-08-08)

Followed the `statfs: permission denied` down on a leg where the mount WORKS
(docker, server as root), which separates the 9P mount's own behaviour from
podman's namespaces. Measured on the live mount:

| | value |
| --- | --- |
| mount propagation | `shared` — so propagation is NOT the problem |
| exported directory | mode 755, root-owned |
| 9P mount root (`m0`) | mode 755, root-owned |
| file inside | mode 644 |
| access as root | works (`ls -ln` lists the file) |
| **access as uid 1001** | **Permission denied**, on both statfs and ls |

Every POSIX mode on the path is permissive and access is refused anyway, so the
denial is not a plain file-permission outcome.

**That is as far as the evidence goes, and an earlier version of this note went
further than it should have** — it concluded "the mount admits only the uid that
mounted it". That is consistent with the measurement but not established by it,
and the case that actually matters was never tested. Two reasons:

  - **The test ran in the SAME user namespace.** `setpriv --reuid=1001` changes
    the uid, not the namespace. Rootless podman's daemon and containers run in a
    DIFFERENT userns, so the measurement exercised plain DAC and never the
    namespace dimension at all. A different uid and a different namespace are not
    the same question.
  - **There was no CONTROL.** Nothing established that `setpriv --reuid=1001`
    could read an ordinary 755 root-owned directory in that environment. Without
    it, "denied on the 9P mount" does not distinguish a 9P behaviour from a
    broken probe.

Kernel sources say what the boundaries are (fs/9p/fid.c, fs/9p/v9fs.c):
`v9fs_fid_lookup` selects `current_fsuid()` for ACCESS_SINGLE/USER/CLIENT and
`v9ses->uid` for ACCESS_ANY; only ACCESS_SINGLE returns -EPERM outright, and we
saw EACCES. Worth noting for anyone retrying `access=any`: `v9ses->uid` is left
UNINITIALISED unless `access=<uid>` supplies one, so an `access=any` attempt may
fail for that reason rather than for the reason being tested — which may be
exactly what happened here.

What IS established, and survives the caveats above:

1. **It is not mount-namespace propagation.** The mount is `shared`, and the
   failure reproduces on docker where no separate mount namespace is involved.
2. **It is not the directory chain or the file modes** — every component is
   permissive and was measured.
3. **Translating ids in cornus's 9P server is not obviously the fix.** Whatever
   denies here does so with permissive modes, so ownership is not the whole
   story. That is enough to stop the id-translation work from being built on an
   assumption, which was the point of measuring.

**Both caveats were then tested.** With a CONTROL in the same run:

| probe | result |
| --- | --- |
| uid 1001 reads a plain 755 root-owned directory | **works** — the method is sound |
| uid 1001 reads the 9P mount root (755, root-owned) | **Permission denied** |
| the container itself (docker, no userns) | works, sees 0:0 |

So the denial IS specific to the 9P mount rather than to the probe, and the
same-namespace case is now established. That is enough to explain the rootless
podman deploy failure, whose failing actor is the DAEMON doing statfs as another
uid — not a container in another namespace.

**And it is not cornus's 9P server.** `hugelgupf/p9`'s Tattach handler
(`p9/handlers.go`) ignores the attach UID entirely — it calls
`attacher.Attach()` with no uid argument — so a second attach from another uid is
not being refused by cornus. Nor is it the transport: v9fs multiplexes fids over
the single connection made at mount time, so no new socket is opened.

> **CORRECTED 2026-08-08. The conclusion below is WRONG and is kept only for
> the methodological lesson; the corrected finding is in "It was a 0700 parent
> directory" at the end of this section.** What denied uid 1001 was not v9fs at
> all: `os.MkdirTemp` creates 0700, and the session directory holding the
> mountpoints was never made traversable. Every "denied" cell in the matrix
> below was that directory. The one measurement that IS real is the
> `access=1001` row — ACCESS_SINGLE genuinely excludes other uids. The error was
> generalizing from it "by symmetry" to `access=client`, which is false.

**CONFIRMED: it is v9fs's `access=` mode, and the gating is client-side.**
Mounted with `access=1001`, the DOCKER DAEMON — running as root, and previously
able to use the mount — is locked out:

```
error while creating mount source path '.../m0': mkdir .../m0: file exists
```

(root's stat of the mount is refused, so docker falls back to mkdir and gets
EEXIST). Demonstrating the gate by EXCLUDING the uid that previously worked is
what makes this conclusive rather than suggestive: it is `fs/9p/fid.c`'s
ACCESS_SINGLE path admitting exactly one uid. By symmetry the default
`access=client` admits only the MOUNTING uid, which is the entire failure.

**Every access mode was then measured, and none shares the mount.** The
uninitialised-`v9ses->uid` theory recorded here earlier was WRONG, and the source
says so: `fs/9p/v9fs.c` sets `fid->uid = v9ses->uid` only for ACCESS_SINGLE and
`INVALID_UID` otherwise, while `fid.c`'s ACCESS_ANY path passes `any = 1` so the
lookup ignores uid entirely. ACCESS_ANY should have worked. It does not.

| `access=` | root | uid 1001 |
| --- | --- | --- |
| `client` (cornus's default) | OK | denied |
| `user` | OK | denied |
| `any` | not measured | denied |
| `1001` (SINGLE) | **denied** | not measured |

So the practical answer does not depend on the unexplained part: **no mount
option makes one 9P mount usable by both cornus and a runtime running as another
user.** `access=<uid>` moves the exclusion rather than removing it. Whatever
denies a second uid under client/user/any is still unexplained, and is a kernel
question rather than a cornus one.

### What this means for the planned work

**Id translation in cornus's 9P server cannot fix this.** The gate is in the
v9fs CLIENT and precedes any interaction with the server — the server is never
asked. That closes the question this investigation opened: the "9P plumbing
layer" the id-mapping plan named as the remaining route is the wrong layer.

**There is already a supported shape for this, and it is not a mount option.**
Remote mode (`CORNUS_PODMAN_REMOTE=1`, and its dockerhost/containerd
equivalents) routes mounts through `applyWithSidecarMounts`: a caretaker
companion performs the kernel 9P mount ITSELF, inside the workload's own
namespaces, so the mount is never made by one user and consumed by another. That
is the same mechanism kubernetes always uses, and it sidesteps this entirely.

What remains genuinely unavailable is the CO-LOCATED fast path on a runtime that
runs as another user — the path whose whole premise is that the server can mount
and the runtime can bind the same mountpoint. That premise does not hold there,
and the measurements above say it cannot be made to hold by configuration.
Mounting as the runtime's user instead of as root would reverse the exclusion,
not remove it.

The NAMESPACE dimension remains untested and is a separate question; it only
becomes reachable once a workload can start at all.
`access=any` was tried and the failure persisted, but its presence on the mount
was NOT verified in /proc/mounts, so that one result is weaker than the others
and should be repeated before being relied on. The kernel's 9p documentation
describes `access=` as selecting per-user attaches (`client`, `user`), a single
shared attach (`any`), or a single permitted uid (`<uid>`); the behaviour seen
here is what a mount restricted to its mounting user looks like.

Until that is settled, client-local mounts remain unavailable on any runtime that
reaches them as a different user — rootless podman today, incus if it is ever
wired.

### It was a 0700 parent directory (2026-08-08, supersedes all of the above)

`pkg/deploywire/backing.go` created the session directory with
`os.MkdirTemp(m.baseDir, "sess-")`. **`os.MkdirTemp` always creates 0700**, and
it ignores umask, so the 0022 umask that made every other directory in the chain
0755 never applied to this one. The mountpoints inside it were 0755; their parent
was not traversable by anyone else.

One line — `os.Chmod(sessDir, 0o711)` — and, with **no mount option changed and
`access=client` still the default**:

| | before | after |
| --- | --- | --- |
| uid 1001 reads the 9P mount (docker) | denied | **`-rw-r--r-- 1 0 0 13 f.txt`**, contents read |
| rootless podman deploy | failed at create | **reaches running** |

So the corrected findings are:

- **`access=client` does NOT restrict the mount to the mounting uid.** That claim
  was an inference from the `access=1001` result, never a measurement.
- **`access=<uid>` (ACCESS_SINGLE) really does exclude other uids.** That row was
  measured directly and stands.
- The "ACCESS_ANY should have worked and does not" puzzle **dissolves** — ANY was
  never the thing failing, so there is no kernel anomaly to explain.
- **Id translation in cornus's 9P server is NOT closed off.** The claim that the
  gate is client-side and precedes the server rested entirely on the wrong
  conclusion.

**Why it survived so many rounds of measurement.** The error message names the
MOUNTPOINT — `statfs .../sess-<id>/m0: permission denied` — and `m0` was 0755, so
every probe kept re-confirming that the mountpoint itself was fine. The failing
component was the one the message does not mention. Two habits would each have
caught it on the first round: walking every component of the path rather than
stat-ing the leaf, and noticing that the control probe tested a DIFFERENT path
(`/tmp/ctl`) instead of a directory in the same chain. A control that does not
share the suspect's context controls for nothing.

This is the third time this session that `os.MkdirTemp`'s 0700 has been the
hidden cause (the E2E data root and the credential directory were the others).
**When a path is unreadable by another uid and the modes you checked look right,
check every component, and check `MkdirTemp` call sites first.**

What remains genuinely unfinished on rootless podman is one layer further in: the
deploy now starts, but the workload sees an EMPTY mount (`/data/f.txt: No such
file or directory`) — the mount is not visible in the container's mount
namespace. That is the propagation question, and it is now correctly isolated
from the permission one.

### The propagation layer, solved (2026-08-08)

Measured. The cornus server (root) makes the kernel 9P mount in its own mount
namespace; rootless podman runs containers in a separate one held by its pause
process:

| | server ns | podman pause ns |
| --- | --- | --- |
| mount namespace | `mnt:[4026533998]` | `mnt:[4026534239]` |
| 9p lines in mountinfo | 1 | **0** |

with the 9P mount `private` and `/` itself `private`. A mount under a private
subtree propagates nowhere.

**`mount --make-rshared /` before podman starts** fixes it, and the ordering is
not negotiable: a mount namespace copy joins the peer group of the mounts it was
copied from ONLY if those were shared at copy time. Podman's pause namespace
cannot be made a peer retroactively, so this must precede the podman service.
After it, the pause namespace has the 9p line (`master:1133`), and the container's
`/data` is the 9P mount itself — same device `0:197`, same backing socket — and
reads the client's bytes.

**Why the silent failure looked like success.** Without shared propagation the
container's `/data` is `overlay`: podman binds the underlying directory from its
own root filesystem, which exists and is empty because it is a mountpoint. So the
deploy succeeds, the workload reads nothing, and no component reports an error.
That is why `deploy-mounts-local-podman-rootless.star` asserts the FSTYPE and not
only the bytes.

**What is still open, and it is the layer this file wrongly closed.** The file
arrives owned by `65534:65534` inside the container — the server owns it as host
root, and host uid 0 is not in the container's userns map — so reads work only
because the mode is world-readable, and `touch /data/w` is `Permission denied`.
That is exactly the "Layer B — the 9P plumbing" id translation the id-mapping plan
named, and which the retracted `access=` conclusion above had declared
unreachable. It is reachable; nothing about the v9fs client prevents cornus's 9P
server from translating the ids it reports.

**A deploy-time guard needs a new capability, not a propagation check.** The
obvious gate — refuse when `MountsDir` propagation is not shared — would be
WRONG: the docker leg works with `/` private, because its daemon shares the
server's mount namespace and no propagation step is involved. The fact that
matters is "does this runtime consume mounts from a DIFFERENT mount namespace",
which is true for rootless podman and false for rootful docker. That is an
optional backend capability in the shape of `CredentialBinder`/`IDMapper`, not a
host measurement.
