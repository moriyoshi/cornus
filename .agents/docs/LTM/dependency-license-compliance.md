# Dependency License Compliance

## Summary

Cornus audits the licenses of dependencies that ship in its binaries or embedded SPA, rather than every module recorded in `go.sum`. The reusable `audit-licenses` skill generates notices and fails on unreviewed or strong-copyleft shipped dependencies.

## Key Facts

- Scope is Go modules from `go list -deps ./cmd/...`, runtime `dependencies` in `web/package.json`, and `third_party/` copies. Docs and npm devDependencies do not ship.
- The July 2026 baseline is 228 shipped Go modules plus 11 bundled npm packages: zero GPL/LGPL/AGPL; six HashiCorp MPL-2.0 dependencies are allowed weak copyleft.
- `third_party/yamux` is a modified MPL-2.0 copy. Keep its LICENSE and declare it in the root `NOTICE` and generated `THIRD_PARTY_NOTICES.md`.
- Run `.agents/skills/audit-licenses/scripts/scan_licenses.py --repo .`; it exits non-zero for strong copyleft or review classifications.

## Details

The scan's classifier handles two important false-positive traps. MPL-2.0 text refers to GPL-family secondary licenses, so identify the Mozilla Public License before checking copyleft substrings. `spdx/tools-golang` offers Apache-2.0 or GPL-2.0 at the recipient's option; Cornus elects Apache-2.0. Prefer `LICENSE.code` to documentation licenses where relevant, and recognize `coder/websocket`'s ISC wording.

Generate `THIRD_PARTY_NOTICES.md` only with `gen_notices.py` from the inventory JSON. The root `NOTICE` must retain Cornus's own copyright and accurately state that distributed binaries include Apache-2.0 and MPL-2.0 components. Investigate every weak-copyleft or review result against its actual license file before treating a scan as final.

## Files

- `.agents/skills/audit-licenses/` - policy, scanner, and notice generator.
- `THIRD_PARTY_NOTICES.md` - generated shipped-dependency notices.
- `NOTICE` - top-level attribution summary.
- `third_party/yamux/` - modified MPL-2.0 vendored source.

## Pitfalls

- `go.sum` is not a shipping inventory and overstates the audit scope.
- A substring match for AGPL/GPL in MPL text is not a GPL dependency.
- Do not hand-edit generated notices.

## Published Module Without a License File

`github.com/rootless-containers/proto/go-proto` is Apache-2.0 but its published Go
module zip omits the parent repository's license file. The license-generation
commands therefore ignore it for scanner completion and explicitly re-inject the
canonical license plus attribution row. This is a scanner packaging exception,
not permission to omit the dependency from notices.

## Shipped-Tag and Replacement Rules

The dependency inventory must use the shipped tag set from `LICENSE_TAGS` in the
Makefile. Omitting `otelcol`, `imbh`, or `sable_extern_lib` removes shipped
dependencies from the graph while still producing a plausible notices file.
Manual tooling and CI therefore read the same Makefile value rather than
maintaining another tag list.

Module directories come from `go list`'s `.Dir`. Constructing a module-cache path
from path and version fails for local `replace` directives, as the vendored yamux
fork demonstrated on a cold CI cache.

The policy scan runs on linux/amd64, while the historical notices file is
reproduced on arm64. The amd64-only dependency difference remains an explicitly
tracked gap rather than being hidden by pinning the policy scan to the smaller
graph.

Go-module scanners cannot enumerate Rust crates statically linked inside
`libimbhgo.a`. The root notice discloses the gap; complete attribution requires an
upstream crate-notices manifest, such as one generated with `cargo about`.
