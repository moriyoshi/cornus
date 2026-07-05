# Documents for both humans and coding agents

* [README.md](./README.md)
* [ARCHITECTURE.md](./ARCHITECTURE.md) ... system architecture (canonical; human-reader-ready). The former `.agents/docs/ARCHITECTURE.md` is now a pointer to this file.

# Documents for coding agents

* [./.agents/docs/OVERVIEW.md](./.agents/docs/OVERVIEW.md) ... project overview.
* [./.agents/docs/JOURNAL.md](./.agents/docs/JOURNAL.md) ... findings, insights, and peer code review history.
* [./.agents/docs/LTM/INDEX.md](./.agents/docs/LTM/INDEX.md) ... long-term memory index for durable project knowledge under `./.agents/docs/LTM/`.
* [./.agents/docs/TODO.md](./.agents/docs/TODO.md) ... open to-do items extracted from JOURNAL.md during `good-sleep` consolidation. Check and update this file when picking up or finishing work.
* [./.agents/docs/TESTING.md](./.agents/docs/TESTING.md) ... testing guide: the `go test` suite and the Starlark E2E harness (runner, targets, builtin reference, preflight, containerized runner). Read before changing anything under `pkg/e2e/`, `cmd/cornus-e2e/`, or `e2e/scenarios/`.
* [./.agents/docs/QUALITY_GATE.md](./.agents/docs/QUALITY_GATE.md) ... the standard verification gate to run before declaring a change complete: the Go gate, `e2e-check`, and how to run the E2E harness locally per target (including kube via the containerized runner without a host kind cluster).
* [./.agents/docs/DESIGN_SYSTEM.md](./.agents/docs/DESIGN_SYSTEM.md) ... web UI design system: the token vocabulary, theming model, and component class reference for the `cornus web` SPA. Read before restyling the UI or adding a screen/component under `web/`.

# Rules and protocols

## General

* Cornus is a single Go binary providing three subsystems: a tiny OCI registry, an in-process BuildKit-based build engine, and an imperative deploy engine. Read `./ARCHITECTURE.md` before changing subsystem boundaries.
* The CLI uses `github.com/alecthomas/kong` (not cobra). Keep new commands consistent with the existing kong layout in `cmd/cornus/`.

## File Management

* When you'd make summary documents for your work, be sure to write them under `./.agents/docs`, not under `/tmp`.
* Temporary files should be created under `./.agents-workspace/tmp`, not under `/tmp`.
* ❌ Do not build binaries into the version-controlled tree (e.g. `go build -o cornus ./cmd/cornus` at the repo root). Always output to `./.agents-workspace/tmp` (e.g. `go build -o ./.agents-workspace/tmp/cornus ./cmd/cornus`).
* ❌ Never delete user files without permission. Only safe to delete: files YOU created in THIS session that are in `./.agents-workspace/tmp/`. Always ask first if unsure. Assume all pre-existing files belong to the user.

## Building

* Go is installed at `~/.local/go/bin/go` (Go 1.26; the module's `go 1.26.0` directive makes the toolchain auto-fetch go1.26 when needed). Put `~/.local/go/bin` on `PATH` before invoking `go`.
* Run `gofmt -w` on every Go file you change before running `go build`, `go vet`, or `go test`, and before reporting a change as done.
* The standard local gate for any Go change you make — this applies to subagents too:
  ```
  gofmt -l <changed files>      # must print nothing
  go build ./...
  go vet ./...
  go test ./...                  # or a focused package: go test ./pkg/<pkg>/
  ```
  Fix violations and re-run until clean. Do not declare a change complete with failing build, vet, or tests.
* The in-process build engine (`pkg/build/builder`) is `//go:build linux` and pulls in a large BuildKit dependency tree. `go build ./...` compiles it; **executing** a build needs root or a rootless user-namespace stack (see ARCHITECTURE.md "Running with the right privileges"). The build integration test skips on unprivileged hosts.
* For a container-ready binary: `CGO_ENABLED=0 go build -tags "netgo osusergo" -o ./.agents-workspace/tmp/cornus ./cmd/cornus` (fully static).

## Testing

* Make sure that regression tests are ready for your fix.
* Tests must run without external daemons. The registry, deploy backend, and server APIs are covered with in-process servers / fakes; do not introduce tests that require a live Docker daemon, root, or network access in the default `go test ./...` path. Gate any such test (e.g. on `os.Geteuid()==0`) and skip otherwise, as `pkg/build/builder/engine_linux_test.go` does.
* Cross-daemon behavior is covered separately by the Starlark E2E harness (`pkg/e2e/`, driven by `cmd/cornus-e2e/`; scenarios in `e2e/scenarios/`), which is opt-in and not part of `go test ./...`. See [./.agents/docs/TESTING.md](./.agents/docs/TESTING.md) for the harness, its builtins, and the `make e2e-*` targets. When you add or change an E2E builtin, keep `predeclared()` and `predeclaredNames()` in `pkg/e2e/harness.go` in sync (`TestPredeclaredNamesInSync` enforces it), and add new scenarios to the Makefile `SCENARIOS` list.

## Git Workflow

* ❌ Do not run `git checkout` or `git restore` against the working tree — another agent may be working concurrently in the same directory.
* ❌ Never make discretionary commits. Commit or push only when the user explicitly asks.

## Documentation

* Try to write your work summary to one of the existing documents under `./.agents/docs`.
* ❌ Avoid editing any existing sections of `JOURNAL.md`. Append new entries to the end. (The sole exception is the `reconcile-journal-ltm` skill, which may remove entries already consolidated into `.agents/docs/LTM/` per the canonical `## LTM Consolidation Record`.)
* ❌ For repo-authored documentation only (e.g. `AGENTS.md`, `README.md`, `.agents/docs/**`), never use full-width parentheses (`（` `）`). Use half-width parentheses (`(` `)`) with a half-width space before/after when adjacent to a non-whitespace character. This does not apply to generated or third-party reference files under `skills/**/references/**`.
* ❌ For repo-authored documentation only, never use full-width colons (`：`). Use a half-width colon followed by a half-width space. This does not apply to generated or third-party reference files under `skills/**/references/**`.

## Shell Pitfalls (prezto defaults)

The user's shell uses prezto, which sets aliases and options that break non-interactive scripts:

* ❌ `cp src dst` prompts interactively when `dst` exists (prezto aliases `cp` to `cp -i`). Always `rm -f dst` before `cp`. Also kill any process using the destination file first (e.g. `pkill -f cornus` before replacing the binary).
* ❌ `cat > file <<'EOF'` and `echo > file` fail with `file exists` when the target exists (prezto enables `NO_CLOBBER`). Workaround: `rm -f file` before writing, or use `tee` / `/bin/cat`.
* ❌ `rm file` prompts for confirmation on some files (prezto aliases `rm` to `rm -i`). Always use `rm -f` for non-interactive deletion.
