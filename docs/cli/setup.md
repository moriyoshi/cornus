# cornus setup

An interactive wizard that creates and verifies a connection profile (a
"context") for reaching a cornus server, then prints scenario-tailored setup
guidance. It is a guided front-end over [`cornus config set-context`](/cli/config)
— it introduces no new profile semantics.

## Synopsis

```sh
cornus setup
cornus setup --scenario local   # skip the opening picker
```

## Description

`cornus config set-context` is a flat wall of flags spanning several unrelated
deployment topologies. `cornus setup` instead asks which topology you are
configuring and then asks only that topology's questions, materializes the
context (reusing the same client config file), optionally tests the connection,
and ends with a next-steps checklist plus the equivalent `set-context` command.

On a real terminal the wizard renders rich dialogs, and the setup guide below is
styled — bold headings, numbered steps, highlighted commands. On a pipe, in CI,
under `--output plain`, or with `NO_COLOR` set it falls back to plain line
prompts and unstyled text, with no escape sequences at all (see
[Non-interactive use](#non-interactive-use)). It refuses `--output json`, since
prompts would corrupt NDJSON — use `cornus config set-context` for scripting.

### Navigation

At any question you can go back or bail out:

- **Go back one step** — press `Esc` ⎋ or `Ctrl-D` in the rich dialogs, or type
  `<` and `Enter` ⏎ at a plain prompt. Backing out of the first question returns
  to the scenario picker; changing an earlier answer re-asks only what depends on
  it. Nothing is written until every question is answered, so going back is always
  safe.
- **Cancel the wizard** — press `Ctrl-C` ⌃C. Before the profile is saved this
  leaves the config untouched; the save is a single atomic step at the end.

## Scenarios

The first question picks one of:

- **Local server** — a `cornus serve` on this machine (plain HTTP loopback).
- **Remote Docker host (SSH)** — reach a docker host over an SSH tunnel.
- **Remote containerd host (SSH)** — reach a containerd host over an SSH tunnel.
- **Remote daemonless host (SSH)** — reach a host whose server drives an OCI
  runtime (`runc`/`crun`/`youki`) itself, with no daemon at all. See the
  [`bare` backend](/reference/deploy-backends#bare).
- **Remote Incus host (SSH)** — reach a host whose server deploys workloads as
  [Incus](https://linuxcontainers.org/incus/) application containers. See the
  [`incus` backend](/reference/deploy-backends#incus).
- **Kubernetes (auto port-forward)** — an in-cluster install, reached by an
  automatic port-forward. The wizard auto-detects the cornus Service and port,
  falling back to a manual entry when it cannot.
- **Kubernetes (direct URL)** — an in-cluster install reached by an ingress URL.
- **Other server URL** — a server at an already-known URL.
- **Docker host (server in a container)** — run the server itself as a container
  on this docker host. The profile is an ordinary loopback one; what the scenario
  adds is the exact `docker run` command, since the bind mounts are the whole
  difficulty and getting one wrong fails silently. See
  [Running the server in a container](/guides/server-in-a-container).

The four **SSH** scenarios ask exactly the same questions and produce the same
kind of profile: the tunnel is backend-agnostic, and it is the server on the far
end that differs. Which one you pick decides the setup guide you are shown and
the `CORNUS_DEPLOY_BACKEND` the generated systemd unit sets.

**Remote Docker host (SSH)** additionally asks whether the server will run as a
**container** on that host, so you need install no binary there. Answering yes
switches the guide to an `ssh HOST 'docker run …'` shape, asks for the host data
directory, and offers no systemd unit — there is no binary for one to start, and
`--restart unless-stopped` is what survives a reboot. The tunnel and the saved
profile are identical either way; only the far end differs. See
[as a container on that host](/guides/server-setup#ssh-container).

Each scenario asks only what it needs (endpoint or SSH/kube target, TLS, auth,
and an optional registry-host override). Advanced transport options (mTLS,
`via-server`, the general conduit/SOCKS5 mode) are left to
[`cornus config set-context --help`](/cli/config).

### Presets

`--scenario NAME` skips the opening picker and starts directly in that
scenario's questions. The names, in picker order, are `local`, `ssh-docker`,
`ssh-containerd`, `ssh-bare`, `ssh-incus`, `kube-port-forward`, `kube-url`,
`url`, and `docker-container`; an unknown name is rejected with the full list.
Nothing else changes — except that, with no picker to return to, backing out of
the first question cancels the wizard.

## Setting up the server

The wizard configures a *client profile*; it never installs or starts a server.
So its second question, asked before anything else, is whether one exists yet:

> Is the cornus server already set up?

Answer **no** and the wizard immediately prints a setup guide for the scenario
you picked, then carries on with the questions. It opens with a one-line synopsis
(the single command that arrangement comes down to) and continues with the
numbered detail: the prerequisites, the `cornus daemon preflight` command that
checks them, and the `cornus serve` invocation. Answer **yes** and no guide is
printed.

The guide comes *before* the questions because the questions are **about** the
setup — the server URL, the published port, the host data directory all describe
the server the guide has just told you how to run. For anything not yet asked the
guide shows the wizard's own defaults, which are exactly what it proposes next,
so the two agree unless you deliberately depart from both.

It is printed once. The closing checklist carries only what to do next: repeating
the setup steps there would explain how to build a server whose parameters that
same checklist has already committed to disk.

The answer also suppresses the three steps that cannot succeed against a server
that is not listening: the ingress probe for the Kubernetes scenarios (which
would otherwise wait out two timeouts), the SSH-key enrollment, and the
[connection test](#verification). Each is replaced by the command to run once the
server is up.

For the **Local server** scenario only, a "no" is followed by one more question:

> Which container runtime will this server drive?

Docker, containerd, bare, Incus, or Kubernetes. Nothing about the answer reaches
the saved profile — the deploy backend is the server's business, selected there
with `CORNUS_DEPLOY_BACKEND`. It exists so the guide can name that backend's real
prerequisites, which differ sharply: `bare` needs root plus an OCI runtime and
CNI plugins, `incus` needs incusd 6.3+ with `skopeo` and `umoci` installed on the
daemon host, and `kubernetes` needs none of that — only a cluster your
`KUBECONFIG` reaches. The other scenarios do not ask, because their own names
already say which runtime is involved. See
[deploy backends](/reference/deploy-backends).

**Kubernetes is offered here even though the reference calls it "server /
in-cluster only".** That restriction applies to a *serverless*
[`cornus deploy`](/cli/deploy), which falls back to `dockerhost` with a warning —
but `cornus serve` **is** a server, and the backend falls back from in-cluster
config to the ordinary `KUBECONFIG` / `~/.kube/config` rules. So a `cornus serve`
on your machine deploying into [k3s](https://k3s.io/), kind, minikube, or a
remote cluster is a supported setup, and a different one from the two
**Kubernetes** *scenarios* above, which configure a client reaching a cornus
that runs **inside** the cluster.

Its guide names the trap that topology has: the cluster's nodes pull built
images from your server's registry themselves, so a loopback address is useless
to them — set `CORNUS_ADVERTISE_REGISTRY` to something they can reach (see
[building images](/guides/building-images)). It then names the alternative,
because Cornus is primarily meant to run **inside** the cluster, where the
registry is a service endpoint the nodes reach by construction and the trap does
not arise. The recommended way to get there is Helm:

```sh
helm install cornus oci://ghcr.io/moriyoshi/charts/cornus
```

The chart is versioned and its image tag tracks the chart version, so one
command gives you a server and a manifest that match — no pinning to get right.
A raw manifest works too, but must be pinned to a release tag rather than a
branch; see [installation](/introduction/installation) and the
[quick start](/introduction/quick-start), which walks the whole path on a
single-node k3s cluster.

### SSH destination

For the **SSH** scenarios the destination question offers the `Host` aliases
declared in your `~/.ssh/config`, each annotated with what it resolves to
(`ops@10.0.0.5:2222`). Wildcard patterns such as `Host *` are not offered: they
configure a class of hosts rather than naming a connectable one. Typing a
destination stays available as the last choice, since the host may simply not be
in the config — and when there is no readable config, or none that declares a
usable alias, the question is the plain free-text prompt.

For the two **Kubernetes** scenarios the wizard also probes the server's
advertised ingress (`/.cornus/v1/info`) and offers to reach a workload's ingress
host through the SOCKS5 conduit, proposing a sensible default: **native** (tunnel
to the discovered ingress controller) when the server advertises one, **emulate**
(a client-side reverse proxy with a generated cert) when it only exposes an ingress
domain, otherwise **off**. Your choice is written to the profile's
`conduit.ingress` block and selects the socks5 conduit. See
[Ingress](/guides/ingress).

For SSH-host and direct-URL scenarios, authentication is a three-way choice:
**SSH key**, **Static token**, or **None**. SSH-key enrollment happens only after
the profile is saved, so cancelling or failing that step never loses the profile.
For an SSH-host scenario the wizard first tries to run
`cornus auth enrollment-code` on the remote host through the configured SSH
transport; otherwise it asks for a code obtained on the server host.

## Verification

After saving, the wizard offers to test the connection: it resolves the profile
exactly as a real command would (including any port-forward) and calls the
server's `/.cornus/v1/info` endpoint, classifying the result (reachable, auth
required, connection refused, TLS problem, timeout, …) with a remediation hint.
Verification never fails the command — the profile stays saved either way. It is
not offered at all when you said the server is not set up yet, since it would be
a guaranteed failure.

## Artifacts

Setup artifacts are offered after the last question and **before** the profile is
saved, so they arrive while you are still setting the server up rather than after
the fact. They are the guide's commands in file form and are built from your
answers, which is why they come after the questions and the guide does not. For
the SSH scenarios the wizard offers to write a `cornus.service` systemd unit for
the remote host, and for the Kubernetes scenarios a `cornus-values.yaml` helm
values snippet. A **local daemonless** server is offered the same unit, because
on `bare` cornus is the workload supervisor: a server started from a shell stops
applying every workload's restart policy the moment it exits. The other local
backends delegate supervision to their daemon and are offered nothing. Each is ask-before-write ({write to a file, print to stdout,
skip}) and guards an existing file with an overwrite confirmation.

The container-install scenario deliberately bundles **nothing**. That arrangement
needs only Docker, so its guide prints the `docker run` command with your answers
filled in — the host data directory and the port — rather than a Compose file you
would need Compose to use, or a shell script you would have to read before
trusting.

The systemd unit carries the scenario's `CORNUS_DEPLOY_BACKEND` and, as
comments, that backend's prerequisites — root and `/opt/cni/bin` for
`containerd` and `bare`, the runtime override for `bare`, the socket and project
overrides plus the `skopeo`/`umoci` requirement for `incus`. Those comments are
there because none of those prerequisites fails at unit start: a unit missing
them looks healthy right up until a deploy fails for reasons several layers
away.

## Non-interactive use

Non-TTY stdin runs the plain line prompts against scripted input rather than
erroring, so the wizard can be driven from a heredoc:

```sh
printf '1\n\n\n\n\n\n' | cornus --output plain setup                 # local scenario, all defaults
printf '\n\n\n\n\n' | cornus --output plain setup --scenario local   # same, without the picker answer
```

Every prompt prints its default, and EOF aborts **without saving** — a truncated
or wrong script aborts rather than materializing a silently-wrong profile. For
real automation, prefer the deterministic
[`cornus config set-context`](/cli/config) directly.

## Relation to `config set-context`

The wizard writes the same client config file as `cornus config`, and its
guidance prints the exact `cornus config set-context …` command equivalent to the
profile it built (with the bearer token redacted). Anything the wizard can do,
`set-context` can do non-interactively; the wizard just supplies a guided path and
the server-side setup steps.

**See also:** [cornus config](/cli/config),
[connection config](/reference/connection-config),
[working with remote clusters](/guides/remote-clusters).
