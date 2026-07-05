# CLI reference

`cornus` is a single binary that bundles a tiny OCI registry, an in-process
BuildKit-based build engine, and an imperative deploy engine. The same binary
runs the server and acts as the client for building, pushing, deploying, and
reaching workloads.

```sh
cornus [global flags] <command> [command flags]
```

The command tree is parsed with [kong](https://github.com/alecthomas/kong).
Run `cornus --help` or `cornus <command> --help` for the built-in usage text.

## Global flags

These flags sit on the root command and apply to every subcommand.

| Flag | Env var | Default | Description |
| --- | --- | --- | --- |
| `--data-dir` | `CORNUS_DATA` | platform data dir | Persistent data directory (registry CAS + build cache). |
| `--context` | `CORNUS_CONTEXT` | current context | Connection profile to use from the cornus client config (see [`cornus config`](/cli/config)). Overrides the config current-context. |
| `--config-file` | `CORNUS_CONFIG` | platform user config dir | Path to the cornus client config file. Defaults to the platform user config dir, honoring `$XDG_CONFIG_HOME`. |
| `--output` | `CORNUS_OUTPUT` | `auto` | Output rendering: `auto`, `plain`, `fancy`, or `json`. See [Output modes](/guides/output-modes). |
| `--context-file` | `CORNUS_CONTEXT_FILE` | discovered | Explicit project context override file (bare Context in JSON, YAML, or TOML). Without it, Cornus searches upward for `cornus-context.{json,yaml,toml}`. |
| `--no-context-file` | — | `false` | Disable automatic project-context discovery. Conflicts with `--context-file`. |
| `--trust-context-file` | `CORNUS_TRUST_CONTEXT_FILE` | `false` | Allow endpoint, credential, and TLS fields from an auto-discovered project context file. Use only for a trusted working tree. |
| `--no-color` | — | `false` | Disable color in fancy output (layout is kept). Also honored via `NO_COLOR` / `CLICOLOR=0`. |

The `--output` values are:

- `auto` - fancy on a terminal, plain otherwise.
- `plain` - deterministic, no color.
- `fancy` - color plus layout.
- `json` - machine-readable NDJSON.

See [Output modes](/guides/output-modes) for the full behavior.

## Commands

| Command | Description |
| --- | --- |
| [`cornus serve`](/cli/serve) | Run the cornus server (registry + build + deploy). |
| [`cornus build`](/cli/build) | Build an image from a context and push it. |
| [`cornus push`](/cli/push) | Push a local image into the registry. |
| [`cornus deploy`](/cli/deploy) | Apply a deployment spec. |
| [`cornus exec`](/cli/exec) | Run a command inside a deployment (docker exec) via a cornus server. |
| [`cornus port-forward`](/cli/port-forward) | Forward local TCP ports to a deployment container port. |
| [`cornus socks5`](/cli/socks5) | Run a local SOCKS5 split-tunnel proxy for reaching workloads by name. |
| [`cornus tunnel`](/cli/tunnel) | Expose a deployment port to the public internet through a hosted tunnel. |
| [`cornus config`](/cli/config) | Manage connection profiles (contexts) for reaching a remote cornus server. |
| [`cornus compose`](/cli/compose) | Docker Compose-compatible client for Compose / devcontainer projects. |
| [`cornus web`](/cli/web) | Serve the loopback-only browser UI and client-side BFF. |
| [`cornus daemon`](/cli/daemon) | Docker API frontend and unified background-agent controls. |
| [`cornus hub`](/cli/hub) | Join the workload-to-workload overlay as a spoke. |
| [`cornus token`](/cli/token) | Mint JWTs for a server with bearer auth. |
| [`cornus version` / `cornus health`](/cli/version-health) | Print the cornus version, or probe a running server health endpoint. |

## See also

- [What is cornus](/introduction/what-is-cornus)
- [Installation](/introduction/installation)
- [Quick start](/introduction/quick-start)
- [Architecture](/architecture/)
