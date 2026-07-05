# July 2026 Client, Web, And Ingress Consolidation

## Summary

This reference consolidates the July 15-18 client-facing work: Compose concurrency and profiles, SSH connection profiles, the persistent web terminal workspace and SOCKS5 publication, the interactive setup wizard, and ingress access through the SOCKS5 conduit.

## Key Facts

- Compose groups equal build plans, builds distinct groups concurrently, and concurrently reconciles independent services while honoring `depends_on`.
- SSH profiles carry one custom dialer through HTTP and WebSocket transports, supporting native ProxyJump and a Unix-socket system-SSH fallback for ProxyCommand.
- Web terminals persist in the BFF, survive browser reconnects, tile in a split tree, and expose working/idle/blocked state.
- The agent can publish the web BFF and declared ingress names into the SOCKS5 namespace without binding an extra host port.
- Setup answers map deterministically to profiles, support back navigation, and can propose ingress-conduit defaults from server discovery.

## Details

### Compose

`buildServices` resolves all requests, groups them by a `buildRequestKey` that omits `Tag`/`Tags`, and executes unique groups through a bounded errgroup. A group sends its primary tag plus every duplicate service tag in `Tags`. Fancy TTY output uses one task per group; plain output is line-serialized, and JSON stays sequential because events carry no group identifier. `Server.localPushTargets` redirects the primary and every additional tag through the co-located registry.

`up` and `up -d` run services concurrently. `waitForDependencies` already polls live state, so it becomes the topology scheduler when every service gets a goroutine. One shared progress object and grouped hook lines protect terminal output; cancellation fallout is suppressed so it cannot hide the first genuine error. Client-local mounts remain serialized within `clientagent.Project.Apply`.

The Compose loader now preserves every service. `Project.View(profiles)` selects the operational subset, while `compose ps` uses the complete model and therefore reports profile-gated services even when the status command omits the original profile flag.

### SSH profiles

`pkg/sshclient` resolves common ssh_config fields, supports native ProxyJump chains, combines agent and identity-file signers into one callback, and shares fail-closed host-key verification. `ProxyCommand` and unsupported Match semantics use a supervised system-SSH Unix-socket fallback. The `sshd()` E2E builtin validates a real profile-driven Compose path; binary fallback and multi-hop live coverage remain unit-tested only.

### Web workspace and setup

The BFF owns terminal exec connections, a replay ring, and one active subscriber per session. Atomic replay snapshot plus subscriber installation prevents gaps or duplicate output. The Solid client persists a binary split tree and supports drag resize, pane creation, swapping, edge retiling, focus behavior, and prefix-key command palette actions. A headless VT screen classifies terminal sessions as working, idle, or blocked after a quiet-period settle timer.

`cornus web --publish-in-conduit` moves the BFF into the agent and exposes it through an addressless `memlisten` local SOCKS5 registration. Local names override user rules. Non-loopback SOCKS5 binds require explicit opt-in and restrict sensitive direct destinations; the BFF also checks Host and reaps terminal sessions when withdrawn.

The setup wizard offers prior non-secret answers when revisiting a step, uses Esc/Ctrl-D back navigation in rich mode and `<` in plain mode, and separates examples from functional defaults. Its Kubernetes flow can discover ingress capability and write the corresponding SOCKS5 conduit configuration.

### Ingress through SOCKS5

`--ingress-conduit=native|emulate` registers declared ingress hosts at ports 80/443 and requires socks5h DNS. Native mode port-forwards to the real Kubernetes ingress-controller Service, preserving raw TLS SNI and HTTP Host; it needs client kube credentials. Emulate mode is portable, runs an in-process reverse proxy, and terminates TLS with an explicit CA, a detected mkcert root, or a generated fallback CA. Emulate HTTP and TLS scenarios are registered and resolve-checked; live target execution and native-controller E2E remain open.

## Files

- `cmd/cornus/internal/composecli/`, `pkg/sshclient/`, `cmd/cornus/internal/clientconn/`
- `cmd/cornus/internal/webbff/`, `cmd/cornus/internal/clientagent/`, `pkg/socks5/`, `pkg/memlisten/`
- `cmd/cornus/internal/setupwiz/`, `pkg/ingressemu/`, `pkg/ingressnative/`

## Test Coverage

Unit coverage exercises grouping, concurrent reconciliation, profile views, SSH transport, terminal replay/state, setup flows, ingress resolution/proxy/TLS, and SOCKS5 registration withdrawal. E2E coverage includes Compose build grouping, SSH-tunnel profiles, and resolve-checked emulated ingress scenarios.

## Pitfalls

- Do not interleave JSON build events from concurrent groups: consumers cannot associate events with services.
- A normal SOCKS5 URL is session-local; a published web or ingress name must use the held shared agent lifecycle.
- Native ingress cannot use the server's ServiceAccount port-forward permissions.
