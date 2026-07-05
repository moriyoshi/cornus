---
name: audit-licenses
description: "Audit the licenses of every third-party dependency Cornus actually ships (the Go modules compiled into the binaries plus the npm packages bundled into the embedded web SPA), classify each as permissive / weak-copyleft / strong-copyleft, flag anything that needs review, and regenerate THIRD_PARTY_NOTICES.md and the NOTICE stanza. Use when adding or bumping dependencies, before a release, or whenever you want to re-verify license compliance."
user-invocable: true
allowed-tools: Bash, Read, Write, Edit, Grep, Glob
---

# Audit dependency licenses

Produce a trustworthy license inventory for everything Cornus **distributes**,
flag anything incompatible with Cornus's own Apache-2.0 license, and regenerate
the third-party notice artifacts.

**Use this skill when:** you add or upgrade a dependency, you are cutting a
release, or you simply want to re-verify that no copyleft or unknown-licensed
code slipped into a shipped binary.

Read [references/policy.md](./references/policy.md) first — it defines the
category policy and the two classifier traps (the MPL "Secondary License" text
that looks like AGPL, and dual "at your option" Apache/GPL grants). The scripts
already encode these; the doc explains why so you can trust the output.

## Scope (only what ships)

- **Go**: modules from `go list -deps ./cmd/...` — what is compiled into
  `cornus` / `cornus-e2e`. NOT all of `go.sum` (mostly test/indirect deps).
- **npm**: the `dependencies` of `web/package.json`. The SPA is embedded into
  the binary via `//go:embed all:dist` (`pkg/webui/webui.go`), so those bundle
  in. `devDependencies` and the `docs/` VitePress site do NOT ship — out of scope.
- **third_party/**: vendored copies, some modified — checked for license retention.

## Step 0: Environment

Put Go on PATH (Go lives at `~/.local/go/bin` in this project):

```
export PATH="$HOME/.local/go/bin:$PATH"
```

The Go scan needs the modules extracted in `GOMODCACHE`; a normal build or
`go mod download` ensures that. `go list -deps ./cmd/...` compiles the
`//go:build linux` BuildKit tree, so run on Linux.

## Step 1: Scan

```
S=.agents/skills/audit-licenses/scripts
python3 $S/scan_licenses.py --repo . --json .agents-workspace/tmp/license-inventory.json
```

This prints a per-surface summary + a VERDICT, and writes the full inventory
JSON. Exit code is **0** only when there is no strong-copyleft and nothing in
the `review` bucket; otherwise **1** (so it can gate CI).

Markers in the summary: `  ` permissive, `! ` weak-copyleft, `!!`
strong-copyleft, `??` review.

## Step 2: Resolve every non-permissive / review item by hand

The scan is a first pass; do not report it as final until each flagged item is
confirmed by reading the actual license file. For a Go module the path is
`$(go env GOMODCACHE)/<escaped-module>@<version>/LICENSE*` (uppercase letters in
the path are escaped as `!<lower>`).

- **strong-copyleft (`!!`)** — a genuine GPL/LGPL/AGPL dependency is a release
  blocker for a static binary. Confirm it is not a false positive (see the MPL
  and dual-license traps in policy.md), then, if real, escalate to the user:
  find a replacement or remove the feature.
- **review (`??`, UNKNOWN / NO-LICENSE-FILE)** — read the file. If it is a known
  permissive license the classifier missed, extend `classify()` in
  `scan_licenses.py` with the new wording and re-run (this is how `coder/websocket`
  ISC and dual-licensed `spdx/tools-golang` were handled). If a module genuinely
  ships no license, raise it with the user.
- **weak-copyleft (`! `, MPL/EPL/CC-BY)** — allowed. If a weak-copyleft module is
  ALSO vendored under `third_party/` and modified, apply the MPL checklist in
  policy.md (currently: `third_party/yamux`).

Only once every flag is understood is the audit trustworthy.

## Step 3: Regenerate THIRD_PARTY_NOTICES.md

```
python3 $S/gen_notices.py --json .agents-workspace/tmp/license-inventory.json \
    --out THIRD_PARTY_NOTICES.md
```

This lists every shipped Go module and bundled npm package with its version and
license, notes vendored/modified components, and appends the de-duplicated full
license texts — satisfying the notice-retention duties of Apache-2.0, BSD/MIT/ISC
and MPL-2.0. Regenerate it (never hand-edit) whenever dependencies change.

## Step 4: Keep the root NOTICE honest

The root `NOTICE` should state that the distributed binary includes Apache-2.0
and MPL-2.0 third-party components (notably a **modified** `hashicorp/yamux`),
and point at `THIRD_PARTY_NOTICES.md` for the full list. Update it with Edit if a
new license category enters the shipped set. Do not remove the existing Cornus
copyright stanza.

## Step 5 (optional): CI gate

`scan_licenses.py` exits non-zero on any strong-copyleft or review item, so a CI
job can run it to fail the build when a dependency with an unvetted or
incompatible license is introduced:

```
export PATH="$HOME/.local/go/bin:$PATH"
python3 .agents/skills/audit-licenses/scripts/scan_licenses.py --repo .
```

Wire it into the Makefile / GitHub Actions only if the user asks.

## Output conventions

- `THIRD_PARTY_NOTICES.md` and `NOTICE` live at the repo root (they ship).
- The inventory JSON and any working notes go under `.agents-workspace/tmp/`.
- If you write a prose audit summary, put it under `.agents/docs/` (append to
  `JOURNAL.md`), never under `/tmp`.

## Known-good baseline (as of the last run)

228 shipped Go modules and 11 bundled npm packages, all permissive except **six
MPL-2.0** HashiCorp libraries (weak-copyleft, compatible). **Zero** GPL/LGPL/AGPL.
`third_party/yamux` is modified MPL-2.0 and must retain its license. If a run
diverges from this baseline, a dependency changed — investigate the delta.
