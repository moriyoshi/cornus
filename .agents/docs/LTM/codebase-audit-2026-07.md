# Whole-Codebase Adversarial Audit (2026-07)

## Summary

A whole-codebase adversarial security/correctness audit (2026-07-09) sliced every non-test Go file in `pkg/` and `cmd/` into 40 disjoint review slices, ran one high-effort adversarial reviewer per slice, then subjected each finding to two independent skeptic verifiers (a correctness lens and a reachability lens). 85 raw findings distilled to 73 confirmed (14 high, 27 medium, 32 low) with 12 rejected; 72 were then fixed (41 high+medium, 31 of 32 low) and 1 low deferred as a cross-package change. The finding-by-finding report `.agents/docs/AUDIT_2026-07.md` was RETIRED on 2026-07-21 (consolidated into `.agents/docs/TODO.md` once fully resolved); its per-finding detail is recoverable via git history plus the landed fixes and regression tests. This note captures the reusable method, the highlights, and the outcome.

## Key Facts

- Verification bar: a finding survives ONLY if both skeptic verifiers confirm it is real AND reachable against the actual code. Each verifier defaults to "not a bug" and is told to refute. This two-lens gate is what turned 85 raw findings into 73 confirmed.
- Scope: entire codebase (`pkg/` + `cmd/`), extending an earlier session that had audited only the new client-daemon/socks5/tunnel code.
- Cost/scale: 40 review slices, 210 agents, ~6.3M tokens.
- Confirmed severity mix: 14 high, 27 medium, 32 low (verifier-corrected values, not the reviewers' originals).
- Outcome: 72 of 73 fixed; 1 deferred (docker `wait` StatusCode always 0). 35 of the high+medium fixes carry a new unit regression test; the 6 only reachable across a live daemon are covered by E2E (or a fake-net unit test).
- Reconciliation: the audit FIXED by-digest manifest digest verification. The earlier "manifest PUT unvalidated" note (see [[registry-and-storage]]) now applies only to tag pushes, not by-digest PUTs.

## Details

### Method (reusable audit recipe)

1. Slice every non-test Go file in `pkg/` and `cmd/` into 40 disjoint review slices, splitting big packages for balance so no slice is oversized.
2. One adversarial reviewer per slice at high reasoning effort, hunting concrete bugs in correctness, concurrency, resource leaks, error handling, security, API misuse, and nil-deref. Every finding must state a required failure scenario (concrete inputs/state -> wrong behavior).
3. Two independent skeptic verifiers per finding: a correctness lens and a reachability lens. Both default to "not a bug" and are instructed to refute. A finding is confirmed only if BOTH agree it is real and reachable.
4. Severities are set by the verifiers, overriding the reviewer's original rating.

### High-severity highlights

Security:
- Host bind-mount policy bypassable via symlinks: lexical prefix check with no `EvalSymlinks` (`hostpolicy/policy.go`). See [[auth-and-security]].
- Writable 9P `SetAttr(size)` escapes the served root via a symlink fid (`wire/writablefs.go`).
- WebSocket build-attach skips the `apiPolicy` "build" authorization (`server/build_attach.go`).

Correctness:
- Both host backends (`dockerhost`, `containerdhost`) reap a deployment's own sole-member user network between ensure and recreate, breaking every redeploy of a networked app.
- GC deletes live blobs for any repo whose name contains a `manifests` path segment (`storage/cas.go:walkRepos`).
- A failed image pull is reported as success and then tears down the running deployment (`dockerhost/engine.go`).

Resource/API:
- S3 `Commit` uses a single `CopyObject`, which breaks pushes of layers larger than 5 GiB.
- JWKS fetch failure is never recorded, so every request re-fetches under lock across a 10s call (`auth`; see [[auth-and-security]]).
- Concurrent tunnel POSTs leak a live public tunnel.

### Fix approach

One fix agent per owning package. High+medium (41) fixed first across 16 groups of disjoint files (72 files changed); the low sweep (31 of 32) followed across 19 groups (53 more files changed). Bar for the low sweep: "safe minimal fix or document/skip — never regress for a low-severity nit."

Constraint: the audited code lived largely in UNCOMMITTED working-tree changes, so git-worktree isolation was impossible. Fix agents edited the real tree and self-checked only their own package, treating breakage in files they did not touch as non-blocking. The authoritative module-wide gate ran afterward: `gofmt -l` clean on all changed files, then `go build/vet/test ./...` all green, plus `make e2e-check` green.

Representative low-sweep fixes: `server` `deployLocks` is now reference-counted and reaped; `POST /.cornus/v1/gc` takes the `gcRunning` CAS and 409s on overlap; `readPreamble` is bounded to 64 KiB; `containerdhost` exec registry is TTL-reaped and tarcopy zero-pads a shrunk file; `e2e` builtins guard pre-`serve()` nil-deref and `probe9P` reads `modules.dep`.

### The one deferred finding

Docker `wait` always reports `StatusCode` 0. The real exit code is not carried by `deploywire.Event` / `api.InstanceStatus`, so a true fix must thread it through DeployAttach events + session across `deploywire`/`api`/`server` — a cross-package change too large for the sweep's bar. A KNOWN LIMITATION comment was added in `dockerproxy` and it is tracked in TODO.md.

## Files

- `.agents/docs/AUDIT_2026-07.md` — RETIRED 2026-07-21 (was the finding-by-finding report for all 73 confirmed findings); consolidated into `.agents/docs/TODO.md`, per-finding detail recoverable via git history.
- `hostpolicy/policy.go`, `wire/writablefs.go`, `server/build_attach.go` — high-severity security fixes.
- `storage/cas.go` (`walkRepos`), `dockerhost/engine.go` — high-severity correctness fixes.
- `dockerproxy` — carries the KNOWN LIMITATION comment for the deferred docker-wait StatusCode finding.

## Test Coverage

Fixes reachable only across a live daemon are covered by the Starlark E2E harness (see [[e2e-harness-and-coverage]]). E2E additions (`make e2e-check` green):

- `deploy-redeploy-network.star` (+`.yaml`, new; docker/containerd, self-skips otherwise; wired into Makefile `SCENARIOS`): deploy a sole-member networked app, redeploy the same spec, assert it stays up and reachable — the host-backend network-reap HIGH bug.
- `deploy-errors.star` (+section 5): redeploy with a nonexistent tag must FAIL up front AND leave the running instance up — the failed-pull-teardown HIGH bug.
- `registry-errors.star` (+section 8): by-digest manifest PUT with a mismatched body -> 400 DIGEST_INVALID — the manifest-digest-verification MED bug.
- `exec.star` (+`/bin/false`): a non-zero remote command must exit non-zero, not report success — the exec exit-status MED bug.
- `deploy-network.star` (+`deploy-port-dedup.yaml`): a service on 53/tcp+53/udp must deploy (Service accepted with per-protocol port names) — the duplicate port-name HIGH/MED bug.

The `containerdhost` network reap also has a fake-net unit test, `TestApplyRecreateReensuresReapedNetwork`.

## Pitfalls

- The E2E additions were validated by `--check` plus reading the fixes only; they were NOT run live during the audit session (no kind/docker/containerd available). Confirm them in the dind runner before trusting them as green live.
- Severities were verifier-corrected (recorded in the retired `AUDIT_2026-07.md`), not the original reviewer ratings — do not re-derive severity from a reviewer's wording.
- Fix agents ran under working-tree (not worktree) isolation, so each self-checked only its own package. Any regression cross-cutting untouched files could only be caught by the final module-wide gate, not by the per-package agents.
