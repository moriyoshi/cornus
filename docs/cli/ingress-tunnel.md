# cornus ingress-tunnel

Expose a whole project's **ingress** through one public URL, instead of a single
workload port.

## Synopsis

```sh
cornus ingress-tunnel [flags] <deployment>
cornus ingress-tunnel [flags] --project <project>
```

## Description

[`cornus tunnel`](/cli/tunnel) publishes one port of one workload.
`cornus ingress-tunnel` publishes the **ingress**: every hostname and path your
services declare with [`x-cornus-ingress`](/guides/ingress) becomes reachable
through a single URL, with the routing done for you.

That is the difference that matters for a multi-service project. With
`cornus tunnel` a compose project of three services needs three tunnels and three
unrelated URLs; with `cornus ingress-tunnel` it needs one, and requests reach the
right service by host and path exactly as they would in production.

What the tunnel actually fronts depends on the deploy backend:

| Backend | Fronts |
| --- | --- |
| `kubernetes` (with a discoverable ingress controller) | the **real cluster controller**, so the cluster's own routing rules and TLS certificates apply |
| every other backend, and a cluster with no controller | the **server's own ingress routing**, which serves the same declared hosts and paths |

The deployment must already declare an ingress. If it does not, the command
fails and says so rather than publishing a URL that answers nothing.

::: warning Development and preview use
A tunnel in front of the **server's own** ingress routing puts a development
facility on a public address: it terminates no TLS of its own, applies no rate
limiting or access control, and its error pages name the internal workload and
container port they failed to reach. That is fine for sharing work in progress or
receiving a webhook, and wrong for anything durable.

A tunnel in front of a **real cluster ingress controller** carries none of those
caveats — the controller applies whatever routing, TLS and policy the cluster
already enforces. The `fronting:` line the command prints tells you which one you
have.
:::

Credential handling is identical to [`cornus tunnel`](/cli/tunnel): the secret is
injected into the server's already-authenticated endpoint, `--authtoken-file` and
`CORNUS_TUNNEL_AUTHTOKEN` keep it out of argv and shell history, and you can omit
it entirely when the server has a default credential. The tunnel stays up until
Ctrl-C.

## Host handling

A tunnel provider hands out a hostname of its own — `abc123.ngrok.app` — but your
ingress was declared for something else, like `web.myapp.example.com`. Reconciling
those two is what `--host-mode` controls.

| Mode | What arrives at the app | Use when |
| --- | --- | --- |
| `auto` (default) | asks the provider for the ingress hostname; falls back to `alias` when it cannot be granted | almost always |
| `alias` | the **tunnel** hostname; routing resolves through it | the app builds URLs from the `Host` it receives |
| `passthrough` | untouched | the tunnel hostname already *is* an ingress host, or a raw TCP tunnel |
| `rewrite` | the **ingress** hostname | the app keys on its configured hostname |

`alias` is the default outcome, and deliberately so. The app sees the hostname the
browser is actually on, so its redirects, `Domain=` cookies and CORS origins all
point somewhere the visitor can reach. `rewrite` inverts that: the app sees its
configured hostname and any absolute URL it emits points at a name your visitor
cannot resolve. Reach for `rewrite` only when an app refuses to serve a hostname
it does not recognize.

`rewrite` is unavailable in front of a real cluster ingress controller: that
tunnel is a raw byte stream with no HTTP layer to rewrite in. The command tells
you so rather than silently ignoring the flag.

### Getting the hostname you actually declared

Some backends can publish the tunnel under a hostname you choose, which makes
`auto` resolve to `passthrough` — nothing about the request is adjusted at all,
and TLS can be end to end. `ngrok` supports it with a reserved or custom domain
on the account; the `ssh` backend supports it wherever the relay routes by
requested bind host, as [sish](https://github.com/antoniomika/sish) does.
Cloudflare quick tunnels and Tailscale Funnel cannot, so `auto` falls back to
`alias` there.

## Flags

| Flag | Description |
| --- | --- |
| `--project <name>` | Expose every deployment of a compose project behind one URL. Mutually exclusive with the deployment argument. |
| `--host-mode <mode>` | `auto` (default), `passthrough`, `alias`, or `rewrite`. See above. |
| `--host <hostname>` | Which declared ingress hostname the tunnel fronts, when the scope serves more than one. |
| `--proto <http\|tcp>` | `http` (default) or `tcp`. A `tcp` tunnel is a raw byte stream, so client TLS and `Host` reach the ingress untouched — the only way to get end-to-end TLS, and it **requires a real cluster ingress controller** to terminate that TLS. A server routing the ingress itself speaks plain HTTP, so it refuses `tcp` rather than publishing a URL that fails on https. |
| `--authtoken-file <path>` | Read the tunnel credential from a file, keeping it out of argv and shell history. |
| `--authtoken <token>` | The credential directly. Visible via `ps` and often saved to shell history; prefer `--authtoken-file`. |
| `--forward-agent` | Forward the local `ssh-agent` to the server for the `ssh` backend. Like `ssh -A`, only use it against a server you trust. |
| `--server <url>` | Remote cornus server URL. Falls back to the selected connection profile. |

## Examples

Publish a compose project — every service, one URL:

```sh
cornus ingress-tunnel --project myapp
```

```
Ingress tunnel for project/myapp ready at https://abc123.ngrok.app
  serving: web.myapp.example.com, api.myapp.example.com
  fronting: the cluster ingress controller
  host: passed through untouched
```

Publish one deployment's ingress, with the credential read from a file:

```sh
cornus ingress-tunnel --authtoken-file ~/.config/cornus/ngrok-token web
```

Force the app to see its configured hostname:

```sh
cornus ingress-tunnel --project myapp --host-mode rewrite --host web.myapp.example.com
```

## Publishing automatically on deploy

To publish every time you bring the project up, declare it in compose instead of
running the command:

```yaml
services:
  web:
    image: myapp:latest
    ports: ["8080:80"]
    x-cornus-ingress:
      host: web.myapp.example.com
      tunnel: true
```

`cornus compose up` then publishes the project's ingress and prints the URL, and
tears the tunnel down when the session ends. The credential still comes from the
client (a profile's `authtoken-file`, or `CORNUS_TUNNEL_AUTHTOKEN`) — never from
the compose file, which is checked in.

The object form takes the same options as the flags:

```yaml
    x-cornus-ingress:
      tunnel:
        host_mode: rewrite
        host: web.myapp.example.com
```

## See also

- [`cornus tunnel`](/cli/tunnel) — expose a single workload port
- [Ingress guide](/guides/ingress) — declaring hosts and paths
- [Tunnels guide](/guides/tunnels) — per-backend setup
