# Native Dev Container Support in cornus compose

## Summary

`cornus compose` natively supports `.devcontainer/devcontainer.json` (https://containers.dev) so a devcontainer repo gets the same build -> push -> deploy (+ client-local workspace mount over 9P) inner loop as a Compose file, with no hand-written compose.yaml. The loader (`internal/devcontainer`) produces the same `*compose.Project` the Compose path drives, so all existing commands work unchanged; lifecycle hooks run on the host or via server-side exec.

## Key Facts

- Both flavors supported: single-container (`image`/`build.dockerfile`) -> one `devcontainer` service; compose-based (`dockerComposeFile`+`service`+`runServices`) -> `compose.Load` the referenced file(s), overlay the devcontainer's workspace mount/env/ports/command on the target service, filter to `{service} ∪ runServices`.
- CLI routing in `cmd/cornus/internal/composecli/main.go` `resolveProject`: `--devcontainer <path>` (file or dir to search), an `-f` naming a `devcontainer.json`, or auto-detect when no `-f`/Compose file is present (`.devcontainer/devcontainer.json`, `.devcontainer.json`, `.devcontainer/*/devcontainer.json`). A Compose file always wins in a mixed repo.
- Loader side channel: `Result{Project, BaseDir, Hooks, Initialize, Warnings}` — only hooks + host-side workspace/user metadata ride it; everything else is a plain compose project.
- Workspace is a host bind at `workspaceFolder` -> `spec.Mounts` -> the existing deploy-attach 9P path.
- `initializeCommand` runs on the HOST (os/exec in baseDir) before the deploy loop; `onCreate`/`updateContent`/`postCreate`/`postStart`/`postAttach` run in the container via server-side exec (`ExecCreate`/`ExecStart`/`ExecInspect`, `execRunner` seam) after the service reaches ready.
- `postStart` -> `postAttach` also re-run on `start`/`restart` actions (`runStartHooks` + `runtime.runServiceStartHooks` called from `runAction`); once-per-create hooks (onCreate/updateContent/postCreate) are NOT re-run. Plain Compose start/restart is a no-op (guarded on the devcontainer lifecycle being present).
- Hooks honor `remoteUser`/`containerUser` (`ExecConfig.User`) and `workspaceFolder` (`WorkingDir`); object-form (parallel) commands run under an errgroup with buffered per-command output; non-zero exit aborts `up`.
- `build.target`/`cacheFrom` are threaded through (with `${}` substitution) via `devcontainer buildFromSpec` -> `compose.BuildPlan{Target, CacheFrom}` -> `client.BuildRequest{Target, CacheFrom}`; target rides `buildwire.BuildSpec.TargetStage` -> `SolveInput.TargetStage` -> `FrontendAttrs["target"]`, and each cacheFrom ref becomes a `type=registry` cache import client-side.
- JSONC handling: `stripJSONC` is LENGTH- and NEWLINE-preserving (removed bytes overwritten with spaces in place), so `json.SyntaxError.Offset` maps 1:1 to the source; `parseJSONC` reports `line N, column M` via `offsetToLine`. String-aware (escapes and `//` in URLs untouched).
- Unsupported fields (`features`, `customizations`, other `runArgs`, `containerUser` at deploy level) are collected as Warnings printed once to stderr, never silently dropped. `runArgs` `--privileged` maps to Privileged.

## Details

### Design — reuse the compose pipeline

The loader produces the SAME `*compose.Project` the Compose path drives, so `Project.Order`/`Plan`/`translateService`/`ResolveMounts` and every `up`/`down`/`ps`/`build`/lifecycle command work unchanged. Devcontainer-specific state (lifecycle hooks, host-side workspace/user metadata) travels in the `Result` side channel rather than polluting the compose model.

### Lifecycle execution

Key insight: exec is a server API independent of who holds the 9P mount session, so hooks run correctly in ALL up paths — foreground, foreground-mounted, and detached (the `up -d` supervisor's `up` reply only returns once `startService` has seen ready, so the container is up when the `up` process runs the hooks). Lifecycle command forms: string (shell), argv list, and object (named parallel commands, run sorted-by-label under an errgroup).

### build.target / cacheFrom threading

`builder.SolveInput`/`Request` gained `TargetStage string` — the image ref field is already named `Target`, so the multi-stage target got a distinct name to avoid collision. `engine.Solve` sets `FrontendAttrs["target"]` from it; the attrs-building lives in a pure `frontendAttrs` helper (`internal/builder/solve_linux.go`) so the mapping is unit-testable without a real build. Wire: `buildwire.BuildSpec.TargetStage` (json `targetStage,omitempty`), mapped in `internal/server/build_attach.go`. `cacheFrom` needed no server change: `internal/client.Build` folds each ref into a `type=registry` cache import (`{ref: <ref>}`) riding the existing `CacheImports` plumbing. `cmd/cornus/internal/composecli` `buildService` forwards both.

## Files

- `internal/devcontainer/` — loader, JSONC parser (`stripJSONC`, `parseJSONC`, `offsetToLine`), `buildFromSpec`, translation
- `cmd/cornus/internal/composecli/main.go` — `resolveProject` routing (`--devcontainer`, auto-detect)
- `cmd/cornus/internal/composecli/` — lifecycle execution (`execRunner` seam, `runStartHooks`, `runtime.runServiceStartHooks`, `runAction`)
- `internal/builder/solve_linux.go` — `frontendAttrs`, `SolveInput.TargetStage`
- `internal/server/build_attach.go` — `TargetStage` wire mapping
- `internal/client/` — `BuildRequest{Target, CacheFrom}`, cache-import folding
- `internal/compose/` — `BuildPlan{Target, CacheFrom}`

## Test Coverage

- `internal/devcontainer`: JSONC (length/newline preservation, strings-left-alone, trailing commas, escaped-quote, line-reporting), single-container image/build (`TestSingleContainerBuild` asserts target/cacheFrom threading + no warning), mounts (bind vs volume + `${localWorkspaceFolder}`), runArgs (`--privileged` -> Privileged, others warned), compose-based (filter + overlay + hooks), lifecycle forms (string/argv/object sorted-by-label), bare `.devcontainer.json`, features-warn.
- `cmd/cornus/internal/composecli/lifecycle_test.go`: fake `execRunner` (net.Conn payload) asserting phase order, User/WorkingDir/attach threading, non-zero-exit abort before later phases, parallel object form; `var _ execRunner = (*client.Client)(nil)` compile check; `TestRunStartHooksOnlyPerStart`, `TestRunStartHooksNilAndEmpty`.
- `internal/builder/solve_linux_test.go`: `frontendAttrs` target + named-context filtering, empty TargetStage sets no attr, cacheEntries registry import.
- `internal/client`: `TestClientBuildTargetAndCacheFrom`; `internal/compose`: `TestBuildTargetAndCacheFrom`, `TestBuildCacheFromScalar`.
- Manual: binary run against an unreachable server for both `--devcontainer <dir>` and auto-detect confirmed JSONC parse, warnings, and translation into a workspace-mounted service routing to `/.cornus/v1/deploy/attach` (failing only at the network dial). A full live deploy needs a server with the 9P kernel-mount stack (CAP_SYS_ADMIN); a gated Starlark E2E scenario remains an open follow-up.

## Pitfalls

- A JSONC stripper that collapses runs or replaces multi-line `/* */` with a single space destroys newlines and shifts byte offsets, making `json.SyntaxError.Offset`-derived line/column reports point at the wrong place. Strip in place, byte-for-byte, preserving newlines.
- Never silently drop unsupported devcontainer.json fields — collect and print warnings once.
- Don't run once-per-create hooks (onCreate/updateContent/postCreate) on `start`/`restart`; only postStart/postAttach re-run.
- `SolveInput` already had a `Target` field for the image ref; the multi-stage build target needed the distinct name `TargetStage` to avoid collision.
- Lifecycle hooks must go through server-side exec, not the mount session, so they work identically in foreground, foreground-mounted, and detached (`up -d` supervisor) paths.
- Parallel object-form lifecycle commands need buffered per-command output to avoid interleaving.
