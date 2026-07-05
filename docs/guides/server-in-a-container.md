# Running the server in a container

Run the cornus server itself as a container on the **host runtime it manages** —
the host-backend counterpart of the in-cluster Helm install on Kubernetes. You get
cornus without installing anything on the host but a container, and workloads still
land on the host's own runtime.

How well that works depends on the backend, and the differences are not cosmetic.
Docker and containerd are both self-configuring: each asks the runtime it is about
to deploy through which container it is running in, and derives its own host paths
from the answer. Bare has no daemon to ask and needs no paths translated anyway, and
incus hands the runtime no path of its own — for those two the question is only
whether the server can reach its workloads. Each has its own section below.

This is not the same as a [remote docker/containerd host](/guides/remote-docker-hosts),
where the server runs on the far side of an SSH tunnel and you reach it from your
machine. Here the server is co-located with the runtime; it simply happens to be
containerized.

## Why the binds matter

Every path cornus hands the container runtime is resolved by that runtime in the
**host's** filesystem, never in cornus's. A server running directly on the host
never notices, because the two are the same. A containerized one must be told how
they correspond, or it hands over paths that mean nothing on the other side.

The failure is silent. Given a path it cannot find, the runtime creates it and
starts the workload anyway: your mount is an empty directory, no command failed,
and nothing appears in any log. So cornus refuses to guess — it detects the
correspondence where it can, and otherwise says so up front.

## Docker

```sh
docker run -d --name cornus \
  --privileged \
  -p 5000:5000 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest
```

- **The socket bind** is what makes this the host's docker rather than none at all.
- **`:rshared`** on the data dir is what lets a mount cornus makes inside the
  container reach the host. Without it, [client-local mounts](/guides/deploying-workloads#mount-a-client-local-directory-into-a-remote-workload-local-mount-streamed-over-9p)
  (`--local-mount`) are rejected with an explanation; everything else still works.
- **`--privileged`** is needed to build images in-process and to perform the
  kernel 9P mount for `--local-mount`. A server that only deploys pre-built images can
  drop it.

Cornus discovers the `/srv/cornus` ↔ `/var/lib/cornus` correspondence by asking
the daemon about its own container, so no extra configuration is required.

### Reaching workloads

`cornus port-forward`, `cornus tunnel`, and published-port access from the server
dial a workload's container IP. Docker drops traffic between two different bridge
networks, so a containerized server would have no route to a workload that
declares `networks:` — and none back, either.

Cornus handles this itself: when it deploys onto a user-defined network, it
attaches **its own container** to that network first, and detaches again when the
deployment (and the network) is torn down. Nothing to configure. Joining a
user-defined network does not disturb the server's own default route, so its
outbound connectivity is unchanged.

Because the server is then a member of the workload's network, Docker's embedded
DNS resolves it there by container name — so `CORNUS_ADVERTISE_URL` may name the
server's container (`ws://cornus:5000`) for caretaker companions and workload
telemetry to dial back on.

Two consequences worth knowing:

- **The server holds one address in each such network's pool.** If you size a
  network's `ipam.config` subnet or `ip_range` exactly to your replica count, it
  will now be one short, and the extra replica fails to start with the daemon's
  "no available IPv4 addresses on this network's address pools". Widen it by one.
  Cornus adds that explanation to the error when it is the extra tenant.
- **Replacing the server container drops its attachments.** Docker endpoints
  belong to a container, so they survive `docker restart` but not the `docker rm`
  + `docker run` of an upgrade. Cornus re-attaches on demand the next time it
  needs to reach a workload, so this is self-healing; you may see one
  `attached this cornus server's own container to a running workload's network`
  log line per network after an upgrade.

One case is left to you: a workload with **no** `networks:` at all lands on the
default bridge, and cornus will not attach itself to that one automatically —
joining the default bridge *does* move a container's default route, and silently
re-homing the server's own egress is worse than the failure it would fix. If your
server container is not on the default bridge either, `port-forward` reports the
missing route and names the two remedies:

- add `--network host` to the `docker run` above, so the server shares the host's
  view of every docker network; or
- set `CORNUS_DOCKER_REMOTE=1` (plus `CORNUS_AGENT_IMAGE` and
  `CORNUS_ADVERTISE_URL`) to reach workloads through a per-instance companion
  instead. This runs an extra container per replica, so prefer host networking
  unless you need the companion for other reasons.

## containerd

```sh
ctr run -d --net-host --privileged \
  --mount type=bind,src=/run/containerd/containerd.sock,dst=/run/containerd/containerd.sock,options=rbind:rw \
  --mount type=bind,src=/srv/cornus,dst=/var/lib/cornus,options=rbind:rw \
  --mount type=bind,src=/run/cornus,dst=/run/cornus,options=rbind:rw \
  --env CORNUS_DATA=/var/lib/cornus \
  --env CORNUS_DEPLOY_BACKEND=containerd \
  ghcr.io/moriyoshi/cornus:latest cornus \
  cornus serve --addr :5000
```

Started with `ctr` (or `nerdctl`) rather than `docker`, and that is not a style
choice. Cornus discovers its own mounts and network mode by asking the containerd it
is about to deploy through which container it is running in — so it can only find
itself if **that same containerd created the server's container**. Start the server
with docker on a containerd host and it lands in docker's own containerd, on a
different socket, where the lookup finds nothing; cornus then says so and you supply
the mapping with `CORNUS_HOST_PATH_MAP` instead.

Given the socket bind, no path configuration is needed: the data directory's host
spelling is discovered, exactly as on docker.

Each of the four binds above answers for itself, and two of them fail silently
without the preflight:

- **The containerd socket** is what makes self-inspection possible at all. Without
  it cornus cannot learn which container it is, assumes its paths already agree with
  containerd's, and hands over its own container-private spellings. Containerd
  creates each one fresh and empty on the host: volumes with no data, no managed
  `/etc/hosts`, and a log shim that is never staged — so `cornus logs` returns
  nothing for a perfectly healthy container.
- **`/srv/cornus:/var/lib/cornus`** is mandatory here, not optional. This backend
  puts a path under the data dir into **every** deploy (volume backings, the managed
  `/etc/hosts`, the log file), so a data dir the runtime cannot see makes the server
  refuse to start rather than produce empty workloads.
- **`/run/cornus`** is where cornus pins each instance's network namespace and hands
  the path to containerd, whose shim reopens it in the *host's* mount namespace.
  `/run` is container-private, so without this bind every deploy fails — and it
  fails late, after the image pull and after the previous healthy deployment has
  already been torn down. The preflight reports it as `netns-host-visible` and
  refuses to start.
- **`--net-host`** puts the CNI bridge and the port-publishing NAT rules on the host
  instead of inside the server's container. Without it a deploy **succeeds** and
  reports its ports published while the host sees nothing on them, so the preflight
  refuses to start rather than let that happen. If you want it anyway — the server's
  own `port-forward` and `tunnel` still reach workloads either way — set
  `CORNUS_HOST_NETWORK=0` to acknowledge it and serve with a warning.

The published image ships the CNI plugins (`bridge`, `portmap`, `host-local`,
`loopback`) at `/opt/cni/bin` and the `iptables` they shell out to, so there is
nothing to install. `CORNUS_CNI_BIN_DIR` points elsewhere if you would rather
supply your own.

## bare

`bare` shares containerd's CNI networking — cornus forks the same plugins, so the
bridge and the port-publishing NAT rules land in cornus's own network namespace —
but nothing else about the containerd section applies to it. It is daemonless, so
there is no socket to bind, and its OCI runtime is cornus's own child process, so it
shares cornus's mount namespace and no path can diverge: neither the data dir nor
the netns directory needs a bind.

That leaves one consequence, the published port. Run the server container with
`--network host` and it behaves as it does on the host. Without it, `ports:` is
realized inside the server's container and the host sees nothing there, while the
server's own `port-forward` and `tunnel` keep working. Cornus cannot detect which
you have — there is no daemon to ask — so it warns that it cannot tell, and
`CORNUS_HOST_NETWORK=1` or `=0` settles it either way.

## incus

Incus instances are networked by incusd, on incusd's own bridge in the host's
network namespace. Where the server sits decides whether it can reach them, and
there are two supported answers.

**As an incus instance** on the same daemon is the one with no routing problem at
all, and the one that needs no second container runtime: the server sits on
incusd's bridge alongside its workloads, so it reaches them with neither host
networking nor a companion. Cornus recognizes this itself — it identifies its own
instance and the preflight reports `workload-routing` as OK, naming it.

The one thing to get right is how the daemon socket is exposed. Use a **proxy**
device, not a disk device: a disk device is id-shifted to `nobody` inside an
unprivileged instance and cornus cannot connect to it. And publish it at a path
whose parent directory already exists in the image — the listener is created when
the instance starts, and `/var/lib/incus` is absent from most images, which fails
with `bind: no such file or directory`:

```sh
incus config device add cornus incusd proxy \
  listen=unix:/tmp/incus-daemon.sock \
  connect=unix:/var/lib/incus/unix.socket \
  bind=instance mode=0660
incus config set cornus environment.CORNUS_INCUS_SOCKET=/tmp/incus-daemon.sock
```

`connect` is the socket on the host; `listen` is where it appears inside the
instance, which is what `CORNUS_INCUS_SOCKET` then points cornus at.

Nothing else is bound: this backend hands incusd no path of its own, so there is
nothing to translate and no data directory to make host-visible.

**Beside incusd**, in a container on the same host, works too and needs only the
daemon socket plus host networking. Any container runtime will do — the flags below
are the same for `podman run`:

```sh
docker run -d --name cornus --network host \
  -p 5000:5000 \
  -v /var/lib/incus/unix.socket:/var/lib/incus/unix.socket \
  -e CORNUS_DEPLOY_BACKEND=incus \
  ghcr.io/moriyoshi/cornus:latest
```

`--network host` is what gives it a route to the incus bridge. Without it the
server has no route and cannot acquire one — the docker self-attach above has no
analogue, because a cornus container is not an incus instance — so `port-forward`,
`tunnel` and caretaker dial-backs cannot reach a workload. The preflight says so as
`workload-routing`, and a dial that fails names the same cause rather than leaving
you with a bare timeout. Deploys themselves are unaffected, and a `ports:` mapping
still publishes on the host, because incus realizes it with a proxy device that
listens in the daemon's namespace rather than cornus's.

If neither suits, `CORNUS_INCUS_REMOTE=1` (plus `CORNUS_AGENT_IMAGE` and
`CORNUS_ADVERTISE_URL`) reaches every instance through a per-instance companion
instead. That runs an extra instance per replica, so prefer one of the two above
unless you need the companion anyway.

Without any of them, the dial fails and cornus names this as the cause rather than leaving
you with a bare timeout.

## Checking before you commit

Run the preflight inside the image you intend to serve from, with the same mounts
and environment. It performs exactly the checks `cornus serve` does at startup and
exits non-zero on a configuration the server would refuse:

```sh
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/cornus:/var/lib/cornus:rshared \
  ghcr.io/moriyoshi/cornus:latest daemon preflight
```

```
cornus runs in a container (a1b2c3d4e5f6) on a docker host; translating its paths for the runtime
  [ok  ] data-dir-host-visible: data dir /var/lib/cornus is /srv/cornus on the host
  [warn] client-local-mounts: client-local mounts unavailable: ...
           remedy: run with CAP_SYS_ADMIN (or --privileged) and the 9p kernel module loaded
```

`--output json` gives the same result as one object, for a CI gate.

The running server reports the same conclusions: a summary line and one warning
per problem at startup, and the capability flags a client reads from
`GET /.cornus/v1/info`, so `cornus setup`'s verification can tell you that a
server will not realize `--local-mount` before you rely on it.

## Declaring the mapping yourself

When cornus cannot ask the runtime what your container's mounts are — a runtime
with no self-inspection (`bare`), or a container the daemon will not report on, such
as a server that docker started on a containerd host — declare the correspondence
explicitly:

```sh
-e CORNUS_HOST_PATH_MAP=/var/lib/cornus=/srv/cornus
```

Multiple pairs are comma-separated, and an explicit entry always beats a detected
one, so this is also how you correct a wrong guess. A malformed value is a startup
error rather than a silently ignored setting.

## What is not translated

A bind mount **you** write in a deploy spec or Compose file is a host path
already — the daemon is what opens it — so it is passed through untouched, exactly
as on a non-containerized server. The same goes for
`CORNUS_ALLOW_BIND_SOURCES`: write those prefixes as the host sees them.

Only the paths cornus itself provisions under its data directory are translated.

## A note on Docker-in-Docker

A cornus containerized **alongside** the daemon it drives — both in the same
container, as in a Docker-in-Docker test harness — shares that daemon's mount
namespace, so its paths already agree and nothing is translated. Cornus
distinguishes this from the case above by whether the runtime confirms it created
the container cornus is running in, so the two shapes do not need different
configuration.
