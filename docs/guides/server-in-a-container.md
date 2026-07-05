# Running the server in a container

Run the cornus server itself as a container on the **docker or containerd host it
manages** — the host-backend counterpart of the in-cluster Helm install on
Kubernetes. You get cornus without installing anything on the host but a
container, and workloads still land on the host's own runtime.

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
dial a workload's container IP. A server on its own bridge network has no route to
one on a user-defined network, and the dial times out with an explanation of why.
Two ways out:

- add `--network host` to the `docker run` above, so the server shares the host's
  view of every docker network; or
- set `CORNUS_DOCKER_REMOTE=1` (plus `CORNUS_AGENT_IMAGE` and
  `CORNUS_ADVERTISE_URL`) to reach workloads through a per-instance companion
  instead. This runs an extra container per replica, so prefer host networking
  unless you need the companion for other reasons.

## containerd

::: warning
Containerized cornus on a **containerd** host is not finished. Path translation
works, but the backend's CNI networking still builds workload networks inside
whatever network namespace cornus runs in, so the server must share the host's —
and the container image does not yet ship the CNI plugin binaries. Until both are
resolved, run the containerd backend [directly on the host](/guides/remote-docker-hosts).
:::

The requirements it will have, which the preflight already reports:

- `-v /run/containerd/containerd.sock:/run/containerd/containerd.sock`
- `-v /srv/cornus:/var/lib/cornus:rshared` — mandatory here, not optional. The
  containerd backend puts a path under the data dir into **every** deploy (volume
  backings, the managed `/etc/hosts`, the log file), so a data dir the runtime
  cannot see makes the server refuse to start rather than produce empty workloads.
- `--network host`, for the CNI plumbing.
- The CNI plugins (`bridge`, `portmap`, `host-local`, `loopback`) inside the
  container, under `/opt/cni/bin` or `CORNUS_CNI_BIN_DIR`.

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

When cornus cannot ask the runtime what your container's mounts are — a
non-docker runtime, or a container the daemon will not report on — declare the
correspondence explicitly:

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
