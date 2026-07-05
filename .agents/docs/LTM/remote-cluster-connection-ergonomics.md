# Remote-Cluster Connection Ergonomics

## Summary

Makes the cornus CLI usable against a remote in-cluster cornus server without hand-run
`kubectl port-forward` or hand-provisioned tokens. A kubeconfig-style connection profile stores
the endpoint, TLS material, an automatic port-forward to the in-cluster Service, and a way to
mint a short-lived cornus credential from the developer's existing kube access. A single
kong-bound resolver threads all of this into every command (`deploy`/`exec`/`port-forward`/
`compose`/`daemon`/`build`), so a remote deploy is just `cornus deploy -f app.yaml` under the
right context.

## Key Facts

- **Profiles** live in `pkg/clientconfig` — a kubeconfig-style file (`File`/`Context`/`TLS`/
  `PortForward`/`KubeAuth`), `Load`/`Save` (0600 file under a 0700 dir), `Resolve(name)`. The
  `cornus config` command manages it (get-contexts / current-context / use-context / set-context /
  delete-context / view).
- **One resolver, all commands.** `cmd/cornus/internal/clientconn` exposes
  `Resolver{ConfigFile,Context}` -> `Conn{Endpoint,Token,TLS,Cleanup}`. Endpoint precedence:
  explicit flag > context server > auto port-forward. Token precedence: `CORNUS_TOKEN` env >
  kube-auth mint > static profile token. Bound into kong with `ctx.Run(&cli, resolver)`.
- **Auto port-forward** (`pkg/svcforward`): a profile that names an in-cluster Service opens the
  `kubectl port-forward` equivalent to it for the command's lifetime, over embedded client-go
  SPDY, and points the client at `http://127.0.0.1:<local>`.
- **Credential sharing** (`pkg/kubeauth`): a profile can mint an audience-scoped ServiceAccount
  token from the developer's kube access via the TokenRequest API; the in-cluster cornus validates
  it through its existing JWKS/audience verify path with NO server code change.
- **Client TLS everywhere**: `client.WithTLSConfig` applies a `*tls.Config` (custom CA / mTLS /
  insecure) to BOTH the REST transport AND every WebSocket dial, so build and deploy-attach honor
  a profile's TLS too.
- **Server side is unchanged** for auth: kube-auth reuses `CORNUS_JWT_JWKS_URL`/`_JWKS_FILE` +
  `_AUDIENCE` (+ optional `_ISSUER`). Only client-side code and docs were added.
- Global flags: `--context` (`CORNUS_CONTEXT`) and `--config-file` (`CORNUS_CONFIG`).
- **Service auto-detection.** `cornus config set-context <name> --namespace <ns>` (`-n`) can
  discover the client-facing cornus Service in a namespace (`pkg/svcforward.Discover`) and store its
  name + port, so a port-forward profile no longer needs `--pf-service`/`--pf-remote-port`.
- **First-context default prompt.** Creating the very first context offers (TTY-gated) to set it as
  the current context, so a fresh user's plain `cornus deploy` works without a separate
  `use-context`.
- **Pull-ref host is decoupled from the client endpoint.** The deploy `spec.Image` / build push ref
  host comes from the server's `GET /.cornus/v1/info` (`RegistryHost`/`RegistryScheme`), not the
  scheme-stripped control-plane endpoint, so an in-pod build pushes to the co-located registry while
  the node pulls from an advertised host. Client override: `--registry` / `CORNUS_REGISTRY` /
  `clientconfig.Context.RegistryHost`.
- **Opt-in `via-server`.** A tri-state toggle (per-command flag > `CORNUS_VIA_SERVER` env > profile
  field) forces workload logs / port-forward streams through the server instead of direct-to-pod.

## Details

### Connection profiles (`pkg/clientconfig`)

A kubeconfig-style client config with per-context blocks `File`/`Context`/`TLS`/`PortForward`/
`KubeAuth`. `Load`/`Save` write a 0600 file under a 0700 dir; `Resolve(name)` selects a context.
`DefaultPath()` is cross-platform: it honors an explicitly set `$XDG_CONFIG_HOME` on every OS and
otherwise uses `os.UserConfigDir()` (`~/.config` on Linux, `~/Library/Application Support` on
macOS, `%AppData%` on Windows). `(*TLS).Config()` builds a `*tls.Config` from a CA bundle, an mTLS
client key/cert pair, or an insecure-skip-verify flag.

The `cornus config` command is the management surface: `get-contexts`, `current-context`,
`use-context`, `set-context`, `delete-context`, and `view` (token-redacting). `set-context` has
merge-on-edit semantics: it only overwrites fields whose flags are provided (an omitted flag
leaves the stored value as-is, so it doubles as an edit command); `--insecure-skip-verify` only
ENABLES insecure mode and cannot clear it via a bare bool.

#### Service auto-detection for `set-context` (`pkg/svcforward.Discover`)

`cornus config set-context <name> --namespace <ns>` (`-n`; `--pf-namespace` is a working alias)
contacts the cluster ONCE to find the client-facing cornus Service and stores its name + port into
the profile's `port-forward` block, so the user no longer has to know the exact Service and port
per install method. Connect-time port-forwarding is unchanged — `clientconn.Resolve` +
`svcforward.Start` already consume the stored block — so this is purely a config-time convenience.

`Discover` loads the clientset via `kubeclient.Load` then delegates to the unit-testable
`discover(ctx, clientset, ns)`. Detection contract: identify a cornus install by Service labels,
trying `app.kubernetes.io/name=cornus` (Helm chart, `deploy/helm/cornus/templates/_helpers.tpl`)
then `app=cornus` (raw `deploy/k8s/cornus.yaml`); exclude the headless hub Service
(`spec.clusterIP == "None"`); port preference is the port named `http`, else port 5000, else the
sole port. It is EAGER (runs at set-context, not at connect) and FAILS LOUD: zero matches or more
than one client-facing match is a hard error listing the candidates and directing the user to
`--pf-service` / `--pf-remote-port`. `--no-detect` stores just the namespace offline (no cluster
contact); an explicit `--pf-service` or `--server` also skips detection. `get-contexts` renders a
service-less block as `(port-forward ns/<namespace>)` instead of `(port-forward svc/:0)`.

#### First-context default prompt (`cmd/cornus/config.go`)

When the config has no contexts yet (`firstContext := len(f.Contexts) == 0`) and no current context
is set, `set-context` prompts `Set context %q as the default (current) context? [Y/n]` (empty =
yes), sets `CurrentContext`, and prints "...saved and set as the current context". Only the FIRST
context is offered — a second context never steals the default even if confirmed. The prompt is the
CLI's first interactive input and is strictly TTY-gated: it lives in an injectable
`confirmSetDefaultContext` var that returns false WITHOUT prompting when stdin is not a terminal
(`term.IsTerminal`), so scripts and CI stay deterministic and silent (consistent with the exec `-t`
TTY gating). The var seam also lets tests simulate an interactive "yes" without a PTY.

#### Registry host for cluster pulls, decoupled from the client endpoint

`cornus compose up` used to derive the image ref (`commands.go:59`) as
`client.Host() + "/" + resource + ":latest"` — the scheme-stripped control-plane endpoint — and
that one string served as the build push target, the deploy `spec.Image` (pulled by the node's
containerd), AND the returned name. That only works on the single-node quick start where node ==
host and a port-forward collapses every vantage onto one loopback. On a real cluster the node pulls
in the host netns with node DNS (not CoreDNS, not the client's machine), so a port-forward's
`127.0.0.1:<ephemeral>` (baked in by `clientconn.go:134`) is unpullable, and one host rarely serves
both the in-pod build push and the node pull. See [[registry-and-storage]] and
[[kubernetes-backend]].

The fix is a server-advertised registry host with a client override, in three phases:

- **Phase 0 (client).** `clientconfig.Context.RegistryHost` + `--registry` / `CORNUS_REGISTRY`,
  carried via `clientconn.Conn.RegistryHost` (NEVER rewritten by the port-forward).
  `runtime.registryHostFor(ctx)` resolves precedence override > server `/.cornus/v1/info` >
  `client.Host()` (memoized) and feeds `commands.go:59`.
- **Phase 1 (server advertise).** Auth-exempt `GET /.cornus/v1/info` returns
  `api.ServerInfo{RegistryHost, RegistryScheme}`. Source: `CORNUS_ADVERTISE_REGISTRY` env (mirrors
  `CORNUS_ADVERTISE_URL`), else the optional `deploy.RegistryAdvertiser` the kubernetes backend
  implements by introspecting its OWN Service (reuses the `clusterDNSIP` Service-read pattern +
  svcforward's `app.kubernetes.io/name=cornus` / `app=cornus` selectors). Only NodePort
  (`localhost:<nodePort>`) and LoadBalancer auto-advertise; ClusterIP returns empty on purpose.
  `Server.advertisedRegistry` short-circuits to empty for a non-kubernetes `CORNUS_DEPLOY_BACKEND`
  before calling `getBackend()`, so the build hot path never constructs a backend just to learn it
  does not advertise.
- **Phase 2 (server push-redirect).** `Server.localPushTarget` rewrites a build push whose target
  host equals the advertised host to the co-located registry over loopback (`127.0.0.1:<port>` from
  `HTTPAddr`), because the in-pod build engine cannot reach a NodePort's `localhost:<nodePort>`; the
  repo path is preserved so push and pull hit the same content. Applied at `build.go` and
  `build_attach.go`.

**Exposure matrix.** Helm `registry.exposure` (nodePort | clusterIP | hostPort | hostNetwork |
ingress, default **nodePort**) drives the Service type / hostPort / hostNetwork /
`CORNUS_ADVERTISE_REGISTRY` env, plus a `registry.nodeCIDR` NetworkPolicy ipBlock allow for the
pod-terminating modes. The raw manifest `deploy/k8s/cornus.yaml` Service is **NodePort `30500`**
(matching the chart default): the node pulls through kube-proxy's node-port binding (reachable on
the node's own loopback via `route_localnet`), a real service endpoint — not an ad-hoc forward —
and the same node port serves the CLI control plane, so the quick start uses NO port-forward at all.
The plain-Docker variant keeps `-p 5000:5000` (direct publish).

Durable design reasoning:

- **Core invariant.** An image's identity is its REPOSITORY PATH (`<project>-<service>`); the HOST
  is a per-vantage rendezvous detail. Push and pull hit the same co-located registry backend
  addressed differently, so keep the repo path fixed and let the host vary (push -> loopback,
  pull -> advertised).
- **Push vs pull vantage asymmetry.** The build engine pushes from INSIDE the pod; the node pulls
  from the HOST netns. A single tag host rarely satisfies both — a NodePort's `localhost:<nodePort>`
  is reachable from the node but unbound on the pod's loopback — which is exactly why the server-side
  push-redirect exists.
- **NetworkPolicy is orthogonal to ClusterIP-vs-NodePort.** Both DNAT to the same registry pod, so
  a default-deny ingress drops node-origin pull traffic either way; the source is a node IP (host
  netns), matchable only by an `ipBlock`, never a `podSelector`. Only a host-level listener
  (hostPort / hostNetwork) is policy-immune.
- **Node containerd uses host DNS, not CoreDNS.** So a `*.svc` name does not resolve at pull time
  even though the ClusterIP it would return is reachable via kube-proxy from the host netns. Hence
  ClusterIP-advertise carries the IP, not the DNS name, and is opt-in (needs
  `CORNUS_ADVERTISE_REGISTRY`), while NodePort/LB auto-advertise.
- **Why ClusterIP is not auto-advertised.** It is the default Service type and carries no signal of
  intent; auto-advertising it would silently rewrite the quick start's ref to `<clusterIP>:5000`,
  which the node does not trust. Auto-advertise is restricted to the deliberate exposure types.

#### Server-routed workload streams (`via-server`)

The direct-to-pod path for workload logs / port-forward always won for a cluster profile, with no
way to force the server-routed path. A `via-server` toggle is honored in precedence order
**per-command flag > `CORNUS_VIA_SERVER` env > profile field**; each layer is tri-state (a `*bool`
/ unset value defers to the next), so a higher layer can force EITHER direction.

- `clientconfig.Context.ViaServer *bool` (`via-server` JSON, omitempty).
- `clientconn`: `Conn.ProfileViaServer` (from the profile), `Conn.ViaServer(cliOverride *bool)`
  applying precedence via `viaServerEnabled(cli, env, profile)` + `parseBoolish` (1/true/yes/on,
  0/false/no/off). `Conn.Dialer(viaServer bool)` returns the plain proxy when
  `KubeCluster == nil || viaServer`. `KubeCluster` stays raw cluster coords; the toggle is applied
  at use sites, NOT by nulling `KubeCluster` in `Resolve` — so `mintKubeToken` (KubeAuth) is
  untouched: transport routes through the server while the cluster token is still minted.
- CLI flags are kong `*bool` + `negatable` -> tri-state (absent = nil, `--via-server` = true,
  `--no-via-server` / `--via-server=false` = false): `PortForwardCmd`, `DeployCmd`, and the
  `composecli.Cmd` group (inherited by logs/up). `config set-context --via-server` persists the
  profile field only when the flag is given.
- `composecli.load` computes `direct := cn.KubeCluster != nil && !viaServer` and sets
  `kubeLogs`/`forwardDialer`/`kubeCluster` from it, so the detached `up -d` supervisor also follows
  the toggle (nil kubeCluster => spawnDaemon omits the kube flags => proxy).

### Client TLS through REST and every WebSocket dial (`pkg/client` + `pkg/wire`)

`client.WithTLSConfig(*tls.Config)` stores the config on `Client` and applies it to BOTH the REST
`http.Client` transport AND every WebSocket dial. The REST side trivially takes a transport, but
exec/attach/portforward/build/deploy all ride WebSockets via `pkg/wire`, which had no TLS-config
entry point (only `DialTLS` for the hub). New TLS-aware dialers were added rather than changing
existing signatures: `wire.DialConnControlHeaderTLS` and `wire.DialControlHeaderTLS` (the internal
`dialConn` now takes a `*tls.Config`), plus a trailing `*tls.Config` threaded through
`buildwire.Serve` and `deploywire.Serve` so build and deploy-attach honor a profile's custom CA /
mTLS. Existing callers pass a trailing `nil`.

### The resolver (`cmd/cornus/internal/clientconn`)

`Resolver{ConfigFile,Context}` produces `Conn{Endpoint,Token,TLS,Cleanup}` via `Resolve`/
`Require`. It centralizes two precedence chains:

- **Endpoint**: explicit flag > context server > automatic port-forward (a port-forward-only
  profile triggers `svcforward` and the endpoint becomes the local forwarded address).
- **Token**: `CORNUS_TOKEN` env > kube-auth mint > static profile token.

The resolver lives in an INTERNAL package rather than in package main because `cornus compose` is a
separate package (`cmd/cornus/internal/composecli`) that cannot import `main` (import cycle). One
`*clientconn.Resolver` is built after `kong.Parse` and passed to `ctx.Run(&cli, resolver)`; kong
injects it by type into any command's `Run` method — including nested compose subcommands (e.g.
`func (c *UpCmd) Run(cli *Cmd, r *clientconn.Resolver) error`), the same way kong already injects
the parent `*Cmd`.

`Conn` exposes explicit `Endpoint`, `Token`, `*tls.Config`, and `Cleanup` fields (not opaque
`[]client.Option`) because `build` bypasses `*client.Client` — it calls `buildwire.Serve` with a
raw bearer header + `*tls.Config` — so it needs the resolved values directly. `Conn.Client()`
builds the client options from those fields, so both `client.New` callers and `build` read one
source of truth for the token.

### Automatic port-forward to the in-cluster Service (`pkg/svcforward` + `pkg/kubeclient`)

`svcforward.Start(ctx, Options{KubeContext,Namespace,Service,RemotePort}) (*Forwarder, error)`
loads the developer's kubeconfig (`clientcmd` deferred loader + `ConfigOverrides{CurrentContext}`),
resolves the Service to a READY backing pod and numeric target port, and forwards a local
ephemeral port to it. `Forwarder.LocalAddr` is `127.0.0.1:<port>`; `Forwarder.Close()` stops it.

Resolution (`resolveEndpoint`) uses the Service's ENDPOINTS, not a raw pod list: Endpoints already
reflect pod readiness (ready `Subsets[].Addresses` vs `NotReadyAddresses`) and resolve named
target ports to numbers. The Service itself is fetched only to map the requested service port to a
port NAME (or to accept the sole port when `RemotePort==0`); that name selects the Endpoints port
whose number is the pod target port.

Forwarding reuses the same SPDY dialer pattern as `kubernetes.go:ForwardPort`
(`spdy.RoundTripperFor` -> `spdy.NewDialer` on the `pods/portforward` subresource) but hands it to
the higher-level `portforward.NewOnAddresses(dialer, ["127.0.0.1"], ["0:<target>"], stop, ready,
...)` so client-go owns the local listener and the accept loop. `Start` blocks on the ready
channel (or a forwarder-exited error, or ctx timeout) and reads the assigned local port from
`pf.GetPorts()`. In `resolveConn`, the port-forward-only branch calls `svcforward.Start` with a
30s readiness timeout, sets `Endpoint = http://<localAddr>`, and `Cleanup = fwd.Close`; every
command already `defer cn.Cleanup()`, so the tunnel is torn down on exit.

`pkg/kubeclient.Load(kubeContext, namespace) (clientset, restConfig, ns, err)` centralizes
kubeconfig loading + namespace resolution (explicit > context namespace > "default");
`svcforward` and `kubeauth` both build on it.

### Kube-auth: sharing kube credentials via TokenRequest (`pkg/kubeauth`)

`Token(ctx, Options{KubeContext,Namespace,ServiceAccount,Audience,ExpirationSeconds})` mints a
short-lived, audience-scoped ServiceAccount token via the Kubernetes TokenRequest API
(`clientset.CoreV1().ServiceAccounts(ns).CreateToken(...)`, `authenticationv1.TokenRequest`,
default 3600s; split into a testable `mint()` that takes a clientset). The in-cluster cornus
validates it against the cluster's OIDC JWKS using the EXISTING
`CORNUS_JWT_JWKS_URL`/`_JWKS_FILE` + `_AUDIENCE` (+ optional `_ISSUER`) verify path — NO server
code change.

`clientconfig.Context` gained a `kube-auth` block (`KubeAuth{KubeContext,Namespace,ServiceAccount,
Audience,ExpirationSeconds}`) whose kube-context/namespace default to the `port-forward` block's
when empty, so a pf profile only needs `--kube-auth-service-account` + `--kube-auth-audience`.
`config set-context` gained `--kube-auth-*` flags with the same merge-on-edit semantics as the
`--pf-*` flags. Minting has a 30s timeout that does not outlive the call.

Server-side deployment requirement (documentation, no code): for a minted token to be accepted the
in-cluster cornus must trust cluster-issued SA tokens — `CORNUS_JWT_JWKS_URL` = the cluster JWKS
(its OIDC discovery `.../openid/v1/jwks`, or a projected-SA JWKS file via `CORNUS_JWT_JWKS_FILE`),
`CORNUS_JWT_AUDIENCE` = the audience the profile requests (e.g. `cornus`), `CORNUS_JWT_ISSUER` =
the SA issuer. The caller's kube identity also needs RBAC to `create` on the
`serviceaccounts/token` subresource for the named ServiceAccount.

### Uniform adoption across commands

`deploy`/`exec`/`port-forward` (native) and `compose`/`daemon`/`build` all route through the
resolver. `compose` and `daemon docker` lost their hardcoded `http://localhost:5000` default — it
became a FALLBACK so an unset `-H`/`--host` lets the profile win; every subcommand `Run` (and
`runAction`) `defer`s `cn.Cleanup()`. `build`'s remote path (an explicit `--builder` OR a profile
server routes remote, mirroring deploy) now uses `resolveBuilderURL(cn.Endpoint)` +
`bearerHeader(cn.Token)` and passes `cn.TLS` to `buildwire.Serve` (previously always nil, so a
remote build now honors the profile's CA/mTLS). `cornus hub` also routes through the resolver
(profiles, token precedence, pf-only profiles; an explicit `--server` wins and the flag is no
longer required), and a profile's client-TLS material flows into the hub WS dial via
`caretaker.Config.TLSClientConfig` (see [hub-network-overlay.md](./hub-network-overlay.md)).

### E2E harness support and scenarios

The `cornus()` Starlark builtin gained two kwargs:

- `env={...}` — appended last so it wins over `h.target.ServeEnv()`; points `CORNUS_CONFIG` at a
  throwaway file so driving `cornus config` never clobbers the developer's real
  `~/.config/cornus/config.yaml`. Backward compatible with existing no-kwarg calls.
- `expect_fail=True` — asserts the command must exit non-zero and returns its output instead of
  aborting (mirrors `build(expect_fail=...)`); needed for the kube-auth negative control.

Scenarios:

- `connection-profile.star` (+ fixture `connection-profile-app.yaml`, a port-free `name: profile`
  nginx project). Target-agnostic (docker + kube; skips local). It stores a `config set-context` +
  `use-context` under a throwaway `CORNUS_CONFIG`, asserts `get-contexts` shows the name/endpoint/
  current `*` marker, and drives `compose up`/`ps`/`down` PURELY through the profile (argv carries
  no `-H`), with a server-side `wait(name="profile-web", running=1)` cross-check.
- `incluster-portforward.star` (+ `incluster-cornus.yaml`) and `incluster-kubeauth.star`
  (+ `incluster-cornus-auth.yaml`) — kube-only, self-skip off kube. Both deploy cornus INTO kind
  as a Deployment + ClusterIP Service (image `cornus:e2e`) and drive the CLI through a port-forward
  profile. `incluster-portforward` proves CLI -> svcforward -> in-cluster cornus (a `kubectl get
  deploy` cross-check confirms the in-cluster cornus created the workload). `incluster-kubeauth`
  publishes the live cluster JWKS into a ConfigMap the cornus pod mounts, then a profile that BOTH
  port-forwards AND mints an SA token authenticates; a port-forward-only negative control is
  `expect_fail`ed, proving the minted token is what satisfies auth.

## Files

- `pkg/clientconfig/` — kubeconfig-style profile (`File`/`Context`/`TLS`/`PortForward`/`KubeAuth`),
  `Load`/`Save`/`Resolve`, `DefaultPath`, `(*TLS).Config`.
- `pkg/client/` + `pkg/wire/` — `client.WithTLSConfig`; TLS-aware dialers
  `DialConnControlHeaderTLS`/`DialControlHeaderTLS`; `*tls.Config` on `buildwire.Serve`/
  `deploywire.Serve`.
- `cmd/cornus/internal/clientconn/` — `Resolver`, `Conn`, `Resolve`/`Require`, mint + port-forward
  wiring.
- `cmd/cornus/conn.go` — thin `*CLI` wrappers over the resolver.
- `cmd/cornus/config.go` — the `cornus config` command.
- `pkg/svcforward/` — Service -> ready pod/targetPort resolution (`resolveEndpoint`), SPDY
  `portforward.NewOnAddresses`; `Discover`/`discover` client-facing Service detection for
  `set-context`.
- `cmd/cornus/config.go` — `confirmSetDefaultContext` var, first-context default prompt, `--namespace`
  detection, `--via-server` persistence.
- `pkg/server/` — `GET /.cornus/v1/info` (`api.ServerInfo{RegistryHost,RegistryScheme}`),
  `parseAdvertiseRegistry`, `advertisedRegistry` (non-k8s short-circuit), `localPushTarget` push
  redirect (`build.go` / `build_attach.go`).
- `pkg/deploy/kubernetes/` — `RegistryAdvertiser` Service introspection (NodePort/LoadBalancer only).
- `cmd/cornus/internal/composecli/` — `runtime.registryHostFor` ref-host precedence + memoization
  (`commands.go:59`); `load` `via-server` -> direct/proxy selection.
- `clientconfig.Context` — `RegistryHost`, `ViaServer *bool`; `clientconn.Conn.RegistryHost`,
  `ProfileViaServer`, `ViaServer`, `Dialer`.
- `deploy/helm/cornus/` (`registry.exposure`, `registry.nodeCIDR`) and `deploy/k8s/cornus.yaml`
  (NodePort `30500`).
- `pkg/kubeclient/` — `Load(kubeContext, namespace)` kubeconfig + namespace resolution.
- `pkg/kubeauth/` — `Token`/`mint` TokenRequest minting.
- `pkg/e2e/harness.go` — `cornus()` `env=` / `expect_fail=` kwargs.
- `e2e/scenarios/connection-profile.star` (+ `connection-profile-app.yaml`),
  `incluster-portforward.star` (+ `incluster-cornus.yaml`), `incluster-kubeauth.star`
  (+ `incluster-cornus-auth.yaml`); all registered in the Makefile `SCENARIOS`.

## Test Coverage

- Unit: `pkg/clientconfig` (XDG/cross-platform path, round-trip perms, `Resolve`, `TLS.Config`
  branches incl. a generated self-signed pair), `pkg/client` `TestClientTLSConfig`
  (httptest.NewTLSServer: fails untrusted, succeeds with CA), `pkg/svcforward` (Endpoints ->
  pod/port resolution via fake clientset: single unnamed port incl. `RemotePort==0` and a bad
  port, named multi-port, ambiguous multi-port error, no-ready-pods, missing-service), `pkg/kubeauth`
  (fake clientset + TokenRequest reactor asserting audience/expiration; default expiration;
  empty-token error; option validation with no kubeconfig), `pkg/kubeclient` (temp kubeconfig:
  current-context / override / explicit namespace / unknown-context error — hermetic, never dials),
  `cmd/cornus` `TestResolveConn*` (endpoint precedence, profile token + `CORNUS_TOKEN` override,
  pf-only profile fails fast with `KUBECONFIG` at a missing file) and `TestConfig*` /
  `TestConfigSetContextKubeAuth`.
- Service detection: `discover` via `fake.NewSimpleClientset` (helm/manifest labels, headless-hub
  exclusion, zero/ambiguous/multi-port cases); the command-layer test exercises only the offline
  `--no-detect` path so `go test ./...` never needs a cluster.
- First-context prompt: `TestConfigSetContextFirstContextDefaultPrompt` covers non-TTY (no default),
  injected-yes-on-first (default set), and second-context (default unchanged).
- Registry host: `pkg/deploy/kubernetes/advertise_test.go` (table over Service types via
  `NewWithClient` + fake clientset), `pkg/server/server_info_test.go` (`parseAdvertiseRegistry`, env
  advertise, `localPushTarget` redirect/external/digest,
  `TestAdvertisedRegistryNonK8sBackendSkipsIntrospection`),
  `cmd/cornus/internal/composecli/registry_test.go` (precedence + memoization via an httptest
  `/.cornus/v1/info`).
- `via-server`: `clientconn` `TestViaServerEnabled` (full precedence matrix) + `TestParseBoolish`;
  kong tri-state and CLI persistence smoke-verified.
- E2E (registry): `registry-advertise.star` (target-agnostic) boots `serve(env={CORNUS_ADVERTISE_REGISTRY})`,
  asserts `/.cornus/v1/info` echoes host+scheme, then `build_upload` to an unreachable tag host and asserts
  `BUILD OK` + the tag lands in the co-located registry (proving the push-redirect).
- E2E (via-server): `incluster-portforward.star` A/B-tests workload port-forward and `compose logs`
  through BOTH transports — direct (default) and `CORNUS_VIA_SERVER=1` — relying on pods/log +
  pods/portforward RBAC in `incluster-cornus.yaml`. The harness `port_forward` builtin gained an
  optional `env` kwarg (omits `--server`, threads env) to drive a cluster profile.
- Gate: gofmt / `go build ./...` / `go vet ./...` / `go test ./...` clean; static container build
  (`CGO_ENABLED=0 -tags netgo,osusergo`) OK.
- E2E: `connection-profile.star` PASSED live on docker AND kube. `incluster-portforward.star` +
  `incluster-kubeauth.star` PASSED LIVE on a real kind cluster (svcforward end to end + the
  kube-auth JWKS chain, with an unauthenticated-rejection negative control). No source changes were
  needed for the live kube run.

## Pitfalls

- **Two distinct credentials, bridged by TokenRequest.** The kube API credential authenticates the
  port-forward SETUP to the API server; the cornus credential authenticates THROUGH the tunnel.
  They are NOT interchangeable (different verifiers/audiences). The bridge is a TokenRequest-minted,
  audience-scoped SA token cornus validates via the cluster JWKS — reusing kube access with zero
  server-side code.
- **A port-forwarded Service is plain HTTP.** The forward tunnels raw bytes to the ClusterIP;
  in-cluster cornus behind ClusterIP speaks HTTP (TLS terminates at the cluster edge, which a
  forward bypasses, and the server cert would never match `127.0.0.1`). `resolveConn` therefore
  sets `http://<local>` unconditionally for the port-forward path; the profile token still rides
  over it.
- **kong resolves bindings at RUN time, not compile time.** A clean `go build` does NOT prove a
  `*clientconn.Resolver` is injectable into a command's `Run`; a missing binding only errors when
  the command runs. Smoke-run every resolver-wired command.
- **kong forbids a global flag that duplicates a subcommand flag.** A global `--config` panicked at
  parse time because `CaretakerCmd` already declares `--config`; it was renamed to `--config-file`.
  The `config` COMMAND name did NOT collide — flags and commands are separate namespaces.
- **Resolve via Endpoints, not a pod list.** Endpoints already encode pod readiness and resolve
  named target ports to numbers — the same data kube-proxy routes on. Listing pods by selector and
  reading pod conditions just duplicates it.
- **svcforward's ctx must not own the forward.** The 30s timeout is for kubeconfig/Get + readiness
  only; after `Start` returns, the forward runs on its own `stopCh`. This lets the caller pass a
  short readiness deadline without killing a healthy long-lived tunnel.
- **E2E hermeticity needs the `cornus()` `env=` kwarg.** Driving `cornus config` without it would
  clobber the developer's real `~/.config/cornus/config.yaml`; point `CORNUS_CONFIG` at a throwaway
  file. `expect_fail=` enables the kube-auth negative control.
- **Preflight is target-scoped for kube tools.** `scenarioNeeds` token-scans only for `build(` /
  `ssh_agent(` / `lazy_9p` (not `kubectl(`), and `CapKind`/`CapKubectl` are TARGET needs — so a
  kube-only scenario that self-skips off kube adds NO capability demand to docker/local runs, and is
  safe in the shared `SCENARIOS` list.
- **In-cluster E2E needs the `cornus:e2e` image in kind.** cornus runs in-cluster from the
  app/sidecar image the containerized flow's `prepare_kube` builds + `kind load`s
  (`e2e/container/appimage.Dockerfile` = debian-slim + static cornus, `ENTRYPOINT ["cornus"]`); a
  host-run `make e2e-kube` must replicate that load or the in-cluster Deployment ImagePullBackOffs.
- **The pull-ref host must not be the client endpoint.** A port-forward endpoint is
  `127.0.0.1:<ephemeral>`, unpullable by the node; deriving `spec.Image` from `client.Host()` only
  works on the single-node quick start. `registryHostFor` therefore prefers the server's `/.cornus/v1/info`
  advertise, and `Conn.RegistryHost` is never rewritten by the port-forward.
- **Service auto-detection is only unit-tested.** `discover` is covered by a fake clientset, but the
  auto-advertise-from-Service path (NodePort/LoadBalancer) and a full in-cluster deploy pulling with
  NO port-forward are NOT E2E-covered — the harness's `kube` target runs cornus as a HOST process
  (`serve --addr`, `CORNUS_DEPLOY_BACKEND=kubernetes` over KUBECONFIG), so it has no self Service to
  introspect and `/.cornus/v1/info` returns empty (existing compose/build scenarios are unaffected; ref
  host falls back to `client.Host()`). Closing this gap needs an in-cluster-server E2E target.
- **kube-auth JWKS wiring that avoids fragility.** Publish the live cluster JWKS
  (`kubectl get --raw /openid/v1/jwks`) into a mounted ConfigMap (`CORNUS_JWT_JWKS_FILE`) and leave
  `CORNUS_JWT_ISSUER` unset (skip the iss check), so the scenario never parses the OIDC discovery
  document or reaches the API server's JWKS over TLS from inside the pod. Audience scoping +
  signature are what get verified.

## Related

- The kubernetes SPDY `pods/portforward` mechanism is shared with the user-facing
  `cornus port-forward` deploy feature in [port-forwarding.md](./port-forwarding.md).
- Auth / JWKS verification details live in [auth-and-security.md](./auth-and-security.md).
- The containerized kind runner and the `cornus()` harness kwargs relate to
  [e2e-harness-and-coverage.md](./e2e-harness-and-coverage.md).

## Still Open

- Phase 3 — OAuth2 device authorization grant (`cornus login` against an advertised external OIDC
  IdP; deferred by decision). The resulting JWT would validate through the existing JWKS path.

(Formerly open, since done: resolver adoption in `cornus hub` — see above; the Helm
`auth.jwt.*` values that render the server's `CORNUS_JWT_*` env — see
[auth-and-security.md](./auth-and-security.md).)
