# Running an AI agent in a container with client egress routing

## The scenario

Your team runs an autonomous AI agent — a coding agent, a data agent — as a workload on the cluster, but its calls to the LLM API (say the Anthropic API) can only leave through *your* network: the API is reachable via a corporate proxy, a VPN, or a SASE path that exists on the developer's machine, not on the cluster. On top of that, the API key must be brokered in at run time and must never be baked into the image, the deploy spec, or the pod spec. Cornus solves both halves at once: [egress](/guides/egress) routes the agent's outbound API calls back through your machine, and [credentials](/guides/credentials) hands the running agent a secret that only ever lived on your laptop.

## What you'll use

- [Egress](/guides/egress) — the deploy spec `egress:` block (mode + a PAC-style routing script) to send only `api.anthropic.com` through the caller network.
- [Credentials](/guides/credentials) — the deploy spec `credentials:` block to deliver the API key into the agent without it entering the image.
- [`cornus deploy`](/cli/deploy) — a foreground `--server` session, which both relay egress and credential fetches require.
- [Session conduits](/guides/networking#session-conduits-port-forward-vs-socks5) — `--conduit` to choose how you reach the workload's own ports.
- [The deploy spec](/reference/deploy-spec) — the `EgressSpec` and `CredentialSpec` field reference.

## Walkthrough

Why the naive approaches fail: baking the key into the image leaks it to anyone who can pull the image or read the build logs, and passing it as a plain pod-spec env var writes it into the cluster's control plane. And even with a key, a pod on the cluster simply cannot open a socket to the API when the only sanctioned route lives behind your corporate proxy — the connection times out from inside the cluster.

**1. Make the key available on your machine (never on the cluster).** Cornus mints the credential from your own environment at fetch time; here the `env` backend reads it from a caller-side variable:

```sh
export ANTHROPIC_API_KEY=sk-ant-...      # stays on your machine
```

**2. Write the deploy spec.** The `egress:` block routes only `api.anthropic.com` through the client with a PAC script (everything else stays `DIRECT`, i.e. the pod's own network), and the `credentials:` block brokers the key in as the `ANTHROPIC_API_KEY` env var the agent already expects:

```yaml
name: agent
image: localhost:5000/coding-agent@sha256:1c2d...   # digest-pinned; no secrets inside

env:
  AGENT_TASK: "triage the backlog"

egress:
  mode: proxy                 # caretaker forward proxy, relayed back through the server
  default: cluster            # unmatched destinations egress from the pod's own network
  script: |
    function FindProxyForURL(url, host) {
      if (dnsDomainIs(host, "api.anthropic.com")) return "PROXY client";
      return "DIRECT";
    }

credentials:
  sources:
    - name: anthropic
      backend: env                       # mint from a caller-side env var
      config: { var: ANTHROPIC_API_KEY } # non-secret: only the var name travels
      deliver:
        - kind: env
          envVar: ANTHROPIC_API_KEY      # inject into the agent container
```

**3. Deploy it in a foreground session.** Both relay egress (`proxy`/`transparent`) and credential brokering answer over the live deploy-attach connection, so this is a foreground `--server` deploy on the kubernetes backend — not `--detach`:

```sh
cornus deploy -f agent.yaml --server https://cornus.example.com
```

**4. Optionally choose a conduit** for reaching the agent's own ports (a health endpoint, a UI). Per-port forwarding is the default; a single SOCKS5 proxy reaching services by name is the opt-in alternative:

```sh
cornus deploy -f agent.yaml --server https://cornus.example.com --conduit socks5
```

The session stays up while the agent runs. `Ctrl-C` tears the workload down and stops answering egress relays and credential fetches.

## How it works

Two independent mechanisms combine in one spec. **Egress** is a per-destination routing decision. The PAC script is evaluated at the caretaker, re-checked at the server, and re-checked at the client, so all three agree and a compromised pod cannot upgrade its own routing. `api.anthropic.com` maps to `PROXY client`, the `client` route — the caretaker's forward proxy tunnels that connection back through the cornus server to your machine, which dials the API over your corporate/VPN/SASE path. Everything else returns `DIRECT`, which maps to `cluster`: dialed locally from the pod, never relayed. Because `default` is `cluster`, enabling egress never silently diverts in-cluster traffic — you opt only the API destination *out* to the client. The full route table (`client`, `gateway`, `cluster`, `deny`) and the PAC-return mapping are in [egress](/guides/egress) and the [`EgressSpec` reference](/reference/deploy-spec).

**Credential brokering** is orthogonal. Only the backend name and its non-secret `config` (`{ var: ANTHROPIC_API_KEY }`) ever travel to the server; the key itself is produced on your machine at fetch time and delivered by the per-pod caretaker sidecar. The agent then calls `api.anthropic.com` with the key exactly as it normally would — and that outbound call is what the egress policy routes through your network. So the two features meet at the agent's own HTTPS request: credentials put the key in its hand, egress carries the packets home.

The trust model: the key is never in the image (digest-pinned, secret-free), never in the deploy spec, and never in the wire control frames — it is answered per fetch over the live session, and the workload may fetch **only** the credential names its own deploy session declared, keyed on an unguessable session capability checked at both the server relay and the caretaker. Traffic to the API is scoped by policy to a single destination.

## Variations

**Keep the key out of the container entirely.** The `env`-kind delivery above fetches the key once into a Kubernetes Secret (static, lives in etcd). For the strongest posture, use the `anthropic-proxy` endpoint provider instead: the caretaker runs a loopback reverse proxy to the API and injects the auth header itself, so the agent calls the LLM with **no key of its own**. It can even ride your local Claude Code / Codex login:

```yaml
credentials:
  sources:
    - name: anthropic
      backend: claude-code                 # or: anthropic / env (config.var: ANTHROPIC_API_KEY)
      deliver:
        - kind: endpoint
          provider: anthropic-proxy         # sets ANTHROPIC_BASE_URL; injects the header
          # upstream: https://my-gateway    # optional: an Anthropic-compatible gateway
```

**Route by rule instead of PAC.** If you don't have a PAC file, replace `script:` with an ordered `rules:` list (or the `--egress-route` CLI flags), first match wins:

```yaml
egress:
  mode: proxy
  default: cluster
  rules:
    - { pattern: "api.anthropic.com:443", route: client }
```

**Cover an app that ignores proxy env vars.** Switch `mode: proxy` to `mode: transparent` — all app TCP is captured by an nftables redirect and relayed, so the agent needs no proxy-awareness.

**See also:** [egress](/guides/egress) · [credentials](/guides/credentials) · [deploy spec](/reference/deploy-spec) · [`cornus deploy`](/cli/deploy) · [working with remote clusters](/guides/remote-clusters)
