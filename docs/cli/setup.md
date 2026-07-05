# cornus setup

An interactive wizard that creates and verifies a connection profile (a
"context") for reaching a cornus server, then prints scenario-tailored setup
guidance. It is a guided front-end over [`cornus config set-context`](/cli/config)
— it introduces no new profile semantics.

## Synopsis

```sh
cornus setup
```

## Description

`cornus config set-context` is a flat wall of flags spanning several unrelated
deployment topologies. `cornus setup` instead asks which topology you are
configuring and then asks only that topology's questions, materializes the
context (reusing the same client config file), optionally tests the connection,
and ends with a next-steps checklist plus the equivalent `set-context` command.

On a real terminal the wizard renders rich dialogs; on a pipe, in CI, or under
`--output plain` it falls back to plain line prompts (see
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
- **Kubernetes (auto port-forward)** — an in-cluster install, reached by an
  automatic port-forward. The wizard auto-detects the cornus Service and port,
  falling back to a manual entry when it cannot.
- **Kubernetes (direct URL)** — an in-cluster install reached by an ingress URL.
- **Other server URL** — a server at an already-known URL.

Each scenario asks only what it needs (endpoint or SSH/kube target, TLS, auth,
and an optional registry-host override). Advanced transport options (mTLS,
`via-server`, the general conduit/SOCKS5 mode) are left to
[`cornus config set-context --help`](/cli/config).

For the two **Kubernetes** scenarios the wizard also probes the server's
advertised ingress (`/.cornus/v1/info`) and offers to reach a workload's ingress
host through the SOCKS5 conduit, proposing a sensible default: **native** (tunnel
to the discovered ingress controller) when the server advertises one, **emulate**
(a client-side reverse proxy with a generated cert) when it only exposes an ingress
domain, otherwise **off**. Your choice is written to the profile's
`conduit.ingress` block and selects the socks5 conduit. See
[Public ingress](/topics/ingress).

## Verification

After saving, the wizard offers to test the connection: it resolves the profile
exactly as a real command would (including any port-forward) and calls the
server's `/.cornus/v1/info` endpoint, classifying the result (reachable, auth
required, connection refused, TLS problem, timeout, …) with a remediation hint.
Verification never fails the command — the profile stays saved either way.

## Artifacts

For the SSH scenarios the wizard offers to write a `cornus.service` systemd unit
for the remote host; for the Kubernetes scenarios it offers a `cornus-values.yaml`
helm values snippet. Each is ask-before-write ({write to a file, print to stdout,
skip}) and guards an existing file with an overwrite confirmation.

## Non-interactive use

Non-TTY stdin runs the plain line prompts against scripted input rather than
erroring, so the wizard can be driven from a heredoc:

```sh
printf '1\n\n\n\n\n' | cornus --output plain setup   # local scenario, all defaults
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
