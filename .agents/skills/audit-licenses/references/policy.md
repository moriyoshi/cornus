# License policy and classifier pitfalls

This is the reference behind `scripts/scan_licenses.py`. It records what counts
as acceptable, and the two detection traps that make a naive scan wrong.

## Cornus's own license

Cornus is Apache-2.0 (`LICENSE`, `NOTICE`). Everything it distributes must be
compatible with redistribution under Apache-2.0.

## Categories

| Category | Licenses | Policy |
|---|---|---|
| permissive | Apache-2.0, MIT, MIT-0, ISC, BSD-2/3-Clause, Unlicense, Zlib, 0BSD | Allowed. Retain copyright/notice text when shipping binaries. |
| weak-copyleft | MPL-2.0, EPL-2.0, CDDL-1.0, CC-BY-4.0 | Allowed when used unmodified. File-level obligation only: if you modify a covered file you must offer that file's source under the same license. Honor notice terms. |
| strong-copyleft | GPL, LGPL, AGPL | Blocker for a statically linked, redistributed binary. Investigate before shipping. (LGPL can be acceptable only with dynamic linking / relinking ability, which does not apply to a single static Go binary.) |
| review | UNKNOWN, NO-LICENSE-FILE, anything unrecognized | Must be resolved by hand before the audit is trustworthy. |

`scan_licenses.py` exits non-zero if any strong-copyleft OR review item is
present, so it can gate CI.

## Scope: only what actually ships

Do not audit all of `go.sum` (~450 modules). Most are test-only or indirect
build deps that never enter a binary. The compliance-relevant set is:

- Go: `go list -deps ./cmd/...` — the modules compiled into `cornus` /
  `cornus-e2e`. (~228 modules.)
- npm: the `dependencies` (not `devDependencies`) of `web/package.json`, because
  the SPA is embedded into the binary via `//go:embed all:dist` in
  `pkg/webui/webui.go`. `docs/` (VitePress) is a standalone site and is NOT
  shipped, so its deps are out of scope.

## Trap 1: MPL text names the GPL/LGPL/AGPL

The MPL-2.0 license text defines "Secondary Licenses" by naming the GNU GPL,
LGPL and AGPL. A substring scan therefore reports every MPL-2.0 library
(`hashicorp/*`, `pgregory.net/rapid`, ...) as AGPL/LGPL. The classifier must
match "mozilla public license" BEFORE any GPL/AGPL/LGPL check. This is why the
order of tests in `classify()` is load-bearing.

## Trap 2: dual "at your option" grants

`github.com/spdx/tools-golang` ships a single `LICENSE.code` file containing
BOTH the Apache-2.0 and GPL-2.0 texts under "may be used, at your option, under
either". That is a permissive grant — you elect Apache-2.0. The classifier
detects the "at your option" / "under either" phrasing and returns the
permissive arm. It also prefers a `LICENSE.code` over a `LICENSE.docs` (which is
often CC-BY and covers documentation we do not ship).

## MPL-2.0 obligations we must honor

Five MPL-2.0 modules (`hashicorp/errwrap`, `go-cleanhttp`,
`go-immutable-radix/v2`, `go-multierror`, `golang-lru/v2`) are used unmodified —
retaining their license text in `THIRD_PARTY_NOTICES` satisfies MPL-2.0.

`hashicorp/yamux` is different: it is vendored into `third_party/yamux/` and
extended (e.g. `batched.go`, `priority.go`). MPL-2.0 requires the modified
files stay available under MPL-2.0. Compliance checklist for it:

- keep `third_party/yamux/LICENSE` (the MPL text) in place;
- keep the modified source in the public tree (it is);
- recommended: add the MPL Exhibit A header to the `.go` files;
- mention the modified MPL-2.0 component in the root `NOTICE`.
