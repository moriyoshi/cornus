# Remote Builds: 9P-on-WebSocket Transport (Context, Secrets, SSH, Confinement)

## Summary

Remote builds serve the caller's build context, named build-context directories, and secrets to the server's build engine on demand over 9P (hugelgupf/p9) tunneled through a single WebSocket endpoint, with SSH agent forwarding tunneled as separate yamux streams and caches staying server-side. The caller-side 9P export is confined (read-only, jailed against `..` and symlink escape) and applies `.dockerignore` filtering before anything crosses the wire, with docker-parity symlink semantics (escaping symlinks are transmitted as symlinks, never followed on the caller).

## Key Facts

- Transport: `coder/websocket` + `hashicorp/yamux` split one WebSocket into a control stream (`BuildSpec` + progress) and a 9P stream. Package: `internal/buildwire` (cross-platform; the remote client links no BuildKit).
- Caller side (`Serve`) runs a `p9.Server` exporting `context/`, `dockerfile/`, `ctx/<name>/` (composefs + localfs) and `secrets/<id>` (staticfs). Server side (`Attach`) runs a `p9.Client` and exposes each subtree as an `fsutil.FS` (`p9fs.go`) plus a `secrets.SecretStore`.
- Server endpoint: `GET /.cornus/v1/build/attach` (WebSocket) → `engine.Solve`, progress streamed back.
- CLI: `cornus build --builder <url>` with `--build-context NAME=PATH`, `--secret id=ID,src=PATH`, `--ssh ID[=SOCKET]`. Compose builds (`internal/client.Build`) use the same attach path (the old tar `POST /.cornus/v1/build` bulk-upload was replaced).
- Builder seam: `engine.Solve(SolveInput)` extracted from `Build` — pluggable `fsutil.FS` mounts + optional `secrets.SecretStore` + `SSH session.Attachable`. Named mounts become frontend attr `context:<name>=local:<name>` (for `RUN --mount=type=bind,from=<name>`). Local builds materialize named contexts via `Request.NamedContexts` → `fsutil.NewFS`.
- SSH agent forwarding is tunnelled, not served over 9P: caller declares `BuildSpec.SSHIDs`; the server creates a temp unix socket per id and, per BuildKit connection, opens a **server-initiated** `S`-tagged yamux stream back to the caller, which proxies to `$SSH_AUTH_SOCK` (`internal/buildwire/ssh.go`).
- The 9P export is hardened by `confinedfs.go` (`confinedAttacher` over localfs): no `..` traversal, no symlink escape, read-only (`EROFS`), and caller-side `.dockerignore` filtering (`moby/patternmatcher` v0.6.0, promoted from indirect to direct).
- Symlink fidelity: `Linkname` is read over 9P (`file.Readlink()`) and set on `fstypes.Stat` so fsutil's diskwriter rebuilds real symlinks; escaping symlinks are reachable and transmitted as symlinks (docker parity) but never followed on the caller.
- Named build contexts each honor their **own** `<dir>/.dockerignore` (`loadDockerignore(dir, "")`), independent of the main context's patterns.
- Mental model for `RUN --mount=type=bind,from=NAME`: the process sees a *complete, eager* materialization of the *filtered* context — `.dockerignore`/exclusion omissions are deliberate, symlinks faithful; never a lazy/partial view.

## Details

### Wire protocol

One WebSocket carries a yamux session. Caller-opened streams: control (`BuildSpec` out, progress back) and 9P. Server-opened streams: SSH tunnels (yamux is symmetric, so the server can initiate). Streams are tagged with a 1-byte type then an id line; `pipe` does the bidirectional copy; `readLine` avoids over-reading so agent bytes stay intact.

The 9P export tree served by the caller:

| Path | Backing | Content |
|---|---|---|
| `context/` | localfs via `confinedAttacher` + ignore matcher | main build context |
| `dockerfile/` | localfs via `confinedAttacher` | Dockerfile directory |
| `ctx/<name>/` | localfs via `confinedAttacher` + per-dir ignore matcher | named build contexts (`--build-context NAME=PATH`) |
| `secrets/<id>` | in-memory `staticfs` | secret payloads (no FS to escape) |

`ServeOpts.DockerfileName` (set by `cmd/cornus/build.go`) drives the per-Dockerfile ignore lookup: `loadDockerignore` reads `<dockerfile>.dockerignore` then `.dockerignore`.

### p9 ↔ fsutil integration (server side, `p9fs.go`)

Three non-obvious requirements to make a p9-backed `fsutil.FS` work with BuildKit's filesync:

1. Each walked entry's `Info().Sys()` must return a `*fstypes.Stat` (else "fileinfo without stat info") — wrap with `fsutil.DirEntryInfo`.
2. The p9 client returns `linux.Errno`, which fsutil does not recognize as `os.ErrNotExist`; BuildKit probes optional files (`.dockerignore`, `Dockerfile.dockerignore`) and would fail hard. Map p9 `ENOENT` → `fs.ErrNotExist`.
3. When the walk *target* is missing, fsutil's filter turns the not-exist into `SkipDir`; `FS.Walk` must treat `SkipDir` from the root fn as **stop, not error**.

Symlink entries: `p9fs.go` sets `os.ModeSymlink` on the mode *and* reads the target over 9P (`file.Readlink()` before closing the fid) into `fstypes.Stat.Linkname` — fsutil's diskwriter (`diskwriter.go:178` → `os.Symlink(Linkname, …)`) needs it, otherwise links are rebuilt empty/broken. fsutil keys on `os.FileMode(Stat.Mode)&os.ModeSymlink`, which `osMode` already produces; no mode-encoding change needed.

### Confinement (`confinedfs.go`)

Raw `hugelgupf/p9/fsimpl/localfs` is **not** a jail: `Local.Walk` does `path.Join(root, name...)` with no boundary check (`..` walks out), it follows symlinks on `Open` (`os.OpenFile`), and it exposes `Create`/`WriteAt`/`UnlinkAt`. A malicious build server is just a 9P client that can send arbitrary `Twalk`/`Topen`/`Twrite`, bypassing the honest server-side `cleanRel` sanitizing in `p9fs.go`. `confinedAttacher` enforces, ahead of localfs:

- **No traversal** — `validComponent` rejects walk elements that are `""`/`.`/`..`/contain a separator or NUL.
- **No symlink escape**, split for docker parity:
  - `confinedParent` (Walk): resolves only the *parent* chain with `filepath.EvalSymlinks` against the pre-resolved absolute export root. A final-component escaping symlink is reachable and transmitted *as a symlink* (harmless — it resolves inside the build container, never on the caller; matches `docker build`). Walking *through* an escaping intermediate symlink (`evildir/secret`) is denied because parent resolution escapes.
  - `confinedFollow` (Open): fully resolves and denies (`EACCES`) reading *through* any symlink escaping root.
  - Security property: a malicious server cannot read out-of-tree files. `Readlink` only returns the link *text* (which the caller put there), not content.
- **Read-only** — embeds `templatefs.ReadOnlyFile`; every mutating op returns `EROFS`/`ENOTDIR`; `Open` refuses non-`ReadOnly` modes.
- **`.dockerignore` filtering** — `guard.ignored` hides matches from `Readdir` and denies them on `Walk`. The `.dockerignore` file itself is always served (the engine may re-read it; re-application is idempotent). Filtering at the *caller* means ignored files never cross the wire — privacy on top of docker parity. Each named context gets its own matcher from its own `<dir>/.dockerignore` (chosen semantics: "each tree owns its ignore file" — Docker has no crisp per-named-context behavior to mirror).

### SSH agent forwarding

- Local: `Request.SSH []SSHSource{ID,Socket}`; `Build` constructs `sshprovider.NewSSHAgentProvider`; `Solve` appends the `session.Attachable`. Note `sshprovider.toAgentSource` treats a socket path as agent-forwarding and `os.Stat`s it at construction — the socket must exist first.
- Remote (`internal/buildwire/ssh.go`): the agent is a live socket on the caller, so it is tunnelled. `ServerSession.SSH()` creates a temp unix socket per declared id and points `sshprovider` at it; each BuildKit connection triggers a new `S`-tagged yamux stream to the caller, whose `serveSSH` accept loop proxies to `$SSH_AUTH_SOCK`.
- CLI: `cornus build --ssh default` (or `--ssh ID[=SOCKET]`) → local `Request.SSH`, or with `--builder` → `BuildSpec.SSHIDs` + `ServeOpts.SSHSockets`.

## Files

- `internal/buildwire/` — WebSocket/yamux/9P transport: `Serve` (caller-side p9.Server), `Attach` (server-side p9.Client), `BuildSpec`.
- `internal/buildwire/p9fs.go` — p9-backed `fsutil.FS` adapter (stat wrapping, ENOENT mapping, SkipDir handling, Linkname over the wire).
- `internal/buildwire/confinedfs.go` — `confinedAttacher`, `validComponent`, `guard.confinedParent`/`confinedFollow`, `loadDockerignore`, read-only enforcement.
- `internal/buildwire/ssh.go` — SSH agent tunnel (`ServerSession.SSH()`, `serveSSH`, stream tagging, `pipe`, `readLine`).
- `pkg/build/builder/` — `engine.Solve(SolveInput)` seam; `Request.NamedContexts`, `Request.SSH`; `//go:build linux`.
- `internal/client/` — compose `Build` over the attach path.
- `cmd/cornus/build.go` — `--builder`, `--build-context`, `--secret`, `--ssh` flags; sets `ServeOpts.DockerfileName`.
- `internal/e2e/` — harness `build()` options (`secret`, `build_context`, `ssh`, `builder`, `no_cache`), `ssh_agent()` builtin, `local` Target.
- `e2e/scenarios/build-mounts.star` + `e2e/scenarios/build-mounts/` — committed E2E scenario for all mount types, local + remote.

## Test Coverage

- `internal/buildwire/buildwire_test.go` — WS/yamux/9P round-trip of a context dir + named context + secret; secret-not-found.
- `internal/buildwire/p9fs_fsutil_test.go` — `fsutil.WriteTar` over the p9-backed FS, incl. nested files.
- `internal/buildwire` `TestSSHTunnel` — fake agent socket → temp socket → `S` yamux stream → caller proxy round-trip (no BuildKit).
- `internal/buildwire/confinedfs_test.go` — drives the attacher with a **raw `p9.Client`** over a `net.Pipe` (the honest `p9fs.go` adapter sanitizes `..` via `cleanRel`, so it cannot exercise the escape): asserts `..` walks, escaping-symlink walks, `Create`/`Mkdir`/`UnlinkAt`/`Open(WriteOnly)` all fail with nothing landing on disk, while legitimate paths and in-context symlinks (`Readlink`) work; separate round-trip asserts `.dockerignore` filtering (directory-parent match `node_modules`, `*.log`) with `.dockerignore` itself present.
- `TestGuardFollowVsParent` — white-box `confinedParent` vs `confinedFollow` (Open-follow denial cannot be exercised through an honest client). `TestConfinedSymlinks` — observable wire behavior (escaping symlink Walk+Readlink ok; walk-through denied). `TestSymlinkTargetOverWire` — `Linkname` survives the round trip. `TestDockerignoreNamedContext` — main and named contexts' ignore files are independent.
- `internal/client` — `Build` round-trips the context over 9P to a fake attach server.
- E2E: `e2e/scenarios/build-mounts.star` — Dockerfile RUN steps assert each mount (`BIND_OK`/`SECRET_OK`/`CACHE_OK`/`SSH_FORWARD_OK` via `ssh-add -l`); runs the build twice: local, then remote with `no_cache=True`. Parse check: `cornus-e2e --check` / `TestScenariosParse`. Live execution needs a privileged container (`--target local`); see memory note "Privileged build tests via docker". Standard gate: `gofmt` / `go build ./...` / `go vet ./...` / `go test ./...`.

## Pitfalls

- p9 clients refuse `Topen` on a symlink fid (`EINVAL`) — only regular files are opened; symlinks travel via `Readlink`. Tests asserting `Open` on a symlink are wrong by construction.
- `Readdir` must **refill** from the underlying dir when a page is entirely ignored; otherwise an all-ignored page reads as end-of-directory and truncates the listing.
- p9's `linux.Errno` is not `os.ErrNotExist` to fsutil — map `ENOENT` → `fs.ErrNotExist` or BuildKit's optional-file probes (`.dockerignore`) fail hard.
- fsutil's filter converts a missing walk target into `SkipDir`; treat `SkipDir` returned from the root walk fn as stop, not error.
- `sshprovider.toAgentSource` `os.Stat`s the agent socket at construction — start the agent before building the provider.
- The honest server-side adapter cannot exercise confinement escapes (it sanitizes paths); hostile-input regression tests must use a raw `p9.Client`.
- Local `cornus build` and a harness-launched `cornus serve` share the default data dir, so a remote build can hit the local build's worker cache — use `no_cache=True` when the test must genuinely exercise the wire-supplied mounts.
- Local builds need `Request.NamedContexts` wired; without it BuildKit treats `RUN --mount=type=bind,from=data` as image `docker.io/library/data:latest` and tries to pull it.
- Named contexts never inherit the main context's `.dockerignore` patterns — each tree owns its own ignore file.
