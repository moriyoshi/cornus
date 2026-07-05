# Working with remote clusters

Task-oriented recipes for driving a remote cornus server from your own machine,
while your files, secrets, and kube access stay local. For the full field set
see [connection config](/reference/connection-config) and [remote workflows](/topics/remote-workflows).

To build a cluster profile interactively (auto-detecting the in-cluster Service,
choosing auth, and generating a helm values snippet), run the
[`cornus setup`](/cli/setup) wizard.

## Point a one-off command at a remote server

Target a server for a single command without creating a profile.

```sh
cornus deploy -f app.yaml --server https://cornus.example.com
CORNUS_SERVER=https://cornus.example.com CORNUS_TOKEN="$TOKEN" cornus exec -it web -- sh
```

- `--server` takes precedence over `CORNUS_SERVER`, which takes precedence over the selected profile. The endpoint accepts `http(s)://` or `ws(s)://`.
- The bearer token is read from `CORNUS_TOKEN` (or the profile); it is never a command flag.

**See also:** [remote workflows](/topics/remote-workflows), [cornus deploy](/cli/deploy)

## Create a connection profile for a remote server

Store a server URL, token, and TLS material once so commands need nothing on the command line.

```sh
cornus config set-context prod \
  --server https://cornus.example.com \
  --token "$(cat ci-token.jwt)" \
  --tls-ca-cert ./ca.pem
cornus config use-context prod
cornus deploy -f app.yaml
```

- `set-context` replaces the named context by default; pass `--merge` to edit in place and keep unset fields.
- Layering order is `--from-file` (base), then flags, then `--from-file-override` (top).

**See also:** [cornus config](/cli/config), [connection config](/reference/connection-config)

## Auto port-forward into an in-cluster server via a profile

Reach an in-cluster cornus with no ingress by naming its Service instead of a URL; the CLI opens the port-forward around each command.

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000
cornus config use-context cluster
cornus compose ps     # transparently port-forwards to svc/cornus, then talks to it
```

- Leave `--server` unset: an empty `server` with a `port-forward` block dials the in-cluster Service.
- `--pf-kube-context` selects a kubeconfig context; `--pf-service` skips Service auto-detection.

**See also:** [remote workflows](/topics/remote-workflows), [cornus config](/cli/config)

## Mint short-lived credentials from your own kube access

Derive the bearer token from a cluster ServiceAccount via the Kubernetes TokenRequest API instead of storing a static one.

```sh
cornus config set-context cluster \
  --pf-namespace cornus --pf-service cornus --pf-remote-port 5000 \
  --kube-auth-service-account cornus-client --kube-auth-audience cornus
cornus config use-context cluster
cornus compose ps     # mints a cluster token AND port-forwards -- no static token
```

- `--kube-auth-audience` must match the server's `CORNUS_JWT_AUDIENCE`.
- `--kube-auth-namespace` / `--kube-auth-kube-context` default to the `--pf-*` values; `--kube-auth-expiration-seconds` defaults to `3600`.

**See also:** [connection config](/reference/connection-config), [auth and TLS](/topics/auth-and-tls)

## Switch, view, and delete profiles

Manage the set of connection profiles, kubeconfig-style.

```sh
cornus config get-contexts          # list profiles (current marked *)
cornus config use-context staging   # make staging the default
cornus config current-context       # print the current context name
cornus config view                  # print the file (tokens redacted)
cornus config delete-context old    # remove a profile
```

- `view --show-tokens` prints bearer tokens; `view --export --context prod` emits one bare Context object that round-trips into `set-context --from-file`.
- `delete-context` clears the current-context pointer if it named the deleted context.

**See also:** [cornus config](/cli/config)

## Set a default namespace for a profile

Record the namespace of the cornus install so cluster detection and kube-auth default to it.

```sh
cornus config set-context staging -n cornus-system
```

- `-n`/`--namespace` auto-detects the Service and port unless `--pf-service` or `--no-detect` is set; add `--no-detect` to store the namespace without contacting the cluster.

**See also:** [cornus config](/cli/config), [connection config](/reference/connection-config)

## Route client-to-workload traffic through the server

Force logs and port-forward to go through the cornus server proxy instead of reaching pods directly with your kubeconfig (cluster profiles only).

```sh
cornus config set-context cluster --merge --via-server
cornus port-forward --via-server web 8080:80    # per-command override
```

- Precedence is the per-command `--via-server` / `--no-via-server` flag, then `CORNUS_VIA_SERVER` (`1`/`0`), then the profile field.
- It changes transport only; a `kube-auth` profile still mints its cluster token.

**See also:** [remote workflows](/topics/remote-workflows), [cornus port-forward](/cli/port-forward)

## Tail logs and exec against a remote deployment

Stream a workload's logs and run commands inside it over the resolved server or profile.

```sh
cornus compose logs --follow --tail 100 web
cornus exec -it web -- sh
```

- For a cluster profile, logs and exec go straight to the pod with your kubeconfig, falling back to the server proxy; `--via-server` forces the server-routed path.
- Everything after `--` in `exec` is passed to the command verbatim; `-t` downgrades to a plain stream when stdin is not a terminal.

**See also:** [cornus exec](/cli/exec), [cornus compose](/cli/compose)
