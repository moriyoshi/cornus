#!/usr/bin/env python3
"""Scan the licenses of dependencies that Cornus actually ships.

Covers two distribution surfaces:

  * Go   — the modules compiled into the `cornus` / `cornus-e2e` binaries,
           i.e. the output of `go list -deps ./cmd/...` (NOT the full go.sum,
           which is dominated by test-only / indirect deps that never ship).
  * npm  — the runtime dependencies of the web SPA, which is embedded into the
           binary via `//go:embed all:dist` in pkg/webui/webui.go. Declared in
           web/package.json "dependencies" (plus whatever they pull in that is
           present under web/node_modules). devDependencies are build-time only.

Output: a human summary to stdout and a machine-readable inventory to --json.

The classifier deliberately checks Mozilla Public License and "dual license,
at your option" BEFORE the GPL/AGPL/LGPL checks. This is load-bearing: the
MPL-2.0 license text DEFINES "Secondary Licenses" by naming the GPL, LGPL and
AGPL, so a naive substring scan reports every MPL library as AGPL. Likewise
spdx/tools-golang ships one file containing BOTH the Apache-2.0 and GPL-2.0
texts under an "at your option" grant — it is Apache-2.0 for our purposes.
See references/policy.md for the full rationale.
"""

import argparse
import json
import os
import re
import subprocess
import sys
from collections import defaultdict

# ---------------------------------------------------------------------------
# License policy: category per SPDX-ish id. See references/policy.md.
# ---------------------------------------------------------------------------
PERMISSIVE = {
    "Apache-2.0", "MIT", "MIT-0", "ISC", "BSD-2-Clause", "BSD-3-Clause",
    "BSD", "MIT/ISC", "BlueOak-1.0.0", "Unlicense", "WTFPL", "Zlib",
    "0BSD", "Python-2.0",
}
WEAK_COPYLEFT = {"MPL-2.0", "MPL-1.1", "EPL-2.0", "CDDL-1.0", "CC-BY-4.0"}
STRONG_COPYLEFT = {"GPL", "GPL-2.0", "GPL-3.0", "LGPL", "AGPL"}

# Verified module-level license overrides for modules whose LICENSE file is not
# at the (sub)module root and so reads as NO-LICENSE-FILE. Each entry is
# confirmed by hand from the module's own source-file headers; keep the evidence
# in the comment so the override stays auditable.
KNOWN_MODULE_LICENSES = {
    # go-proto is a Go submodule of github.com/rootless-containers/proto; the
    # LICENSE lives at the repo root, not in the go-proto/ subdir, so the cached
    # submodule ships none. Every source file carries the full Apache-2.0 grant
    # header ("Copyright (C) 2018 Rootless Containers Authors ... Apache License,
    # Version 2.0"). Pulled in transitively by opencontainers/umoci (incus backend).
    "github.com/rootless-containers/proto/go-proto": (
        "Apache-2.0",
        "(verified from source headers; LICENSE at repo root, not in submodule)"),
    # go-localereader ships NO license file at all — not in the module, not at
    # the repo root. Its README.md states "## License\n\nMIT" and names the
    # author (Yasuhiro Matsumoto). Reached only on darwin and windows (through
    # the bubbletea TUI stack), which is why the linux-only policy scan never saw
    # it; the union scan does. The provenance note differs from go-proto's on
    # purpose: claiming a LICENSE file exists somewhere would be false, and this
    # text is reproduced in a legal notice.
    "github.com/mattn/go-localereader": (
        "MIT",
        "(verified from README.md, which declares the license; the module ships no LICENSE file)"),
}


def category(lic):
    if lic in PERMISSIVE:
        return "permissive"
    if lic in WEAK_COPYLEFT:
        return "weak-copyleft"
    if lic in STRONG_COPYLEFT:
        return "strong-copyleft"
    return "review"  # UNKNOWN / NO-LICENSE-FILE / anything unrecognized


# ---------------------------------------------------------------------------
# Classifier
# ---------------------------------------------------------------------------
def classify(text):
    tl = text.lower()

    # 1. Dual "at your option" grants: pick the permissive arm. Must precede
    #    the GPL checks (the file embeds the GPL text too).
    if ("at your option" in tl or "under either" in tl) and "apache license" in tl \
            and ("general public license" in tl):
        return "Apache-2.0"

    # 2. MPL FIRST — its "Secondary License" definition names GPL/LGPL/AGPL.
    if "mozilla public license" in tl:
        m = re.search(r"mozilla public license[,\s]+version\s*([0-9.]+)", tl)
        return "MPL-" + (m.group(1) if m else "2.0")

    # 3. Real copyleft (only reached once MPL/dual are ruled out).
    if "affero general public license" in tl:
        return "AGPL"
    if "lesser general public license" in tl:
        return "LGPL"
    if "gnu general public license" in tl and "apache license" not in tl:
        return "GPL"

    # 4. Permissive families.
    if "apache license" in tl and "version 2.0" in tl:
        return "Apache-2.0"
    if "blue oak model license" in tl:
        return "BlueOak-1.0.0"
    if "this is free and unencumbered software released into the public domain" in tl:
        return "Unlicense"
    if "do what the fuck you want" in tl:
        return "WTFPL"

    # MIT (the "free of charge" grant).
    if "permission is hereby granted, free of charge" in tl:
        if "mit no attribution" in tl or "mit-0" in tl:
            return "MIT-0"
        return "MIT"

    # ISC (the "for any purpose with or without fee" grant). The connective
    # varies across copies ("and distribute", "and/or distribute",
    # ", and distribute"), so match loosely up to "distribute".
    if re.search(r"permission to use, copy, modify.{0,14}distribute", tl) \
            and ("with or without fee" in tl or "isc license" in tl):
        return "ISC"

    # BSD family.
    if "redistribution and use in source and binary forms" in tl:
        if "neither the name" in tl or "endorse or promote" in tl:
            return "BSD-3-Clause"
        if "2. redistributions in binary form" in tl or "2) redistributions in binary" in tl:
            return "BSD-2-Clause"
        return "BSD"

    if "creative commons attribution 4.0" in tl:
        return "CC-BY-4.0"

    return "UNKNOWN"


LICENSE_FILE_RE = re.compile(r"^(licen[sc]e|copying|unlicense)", re.I)


def find_license_file(d):
    if not d or not os.path.isdir(d):
        return None
    entries = sorted(os.listdir(d))
    # Prefer a "code" license over a "docs" license (spdx/tools-golang ships both).
    ranked = []
    for e in entries:
        if not LICENSE_FILE_RE.match(e):
            continue
        el = e.lower()
        score = 0
        if "doc" in el:
            score = 2          # deprioritize LICENSE.docs (often CC-BY)
        elif "code" in el:
            score = -1         # prefer LICENSE.code
        ranked.append((score, e))
    if not ranked:
        return None
    ranked.sort()
    return os.path.join(d, ranked[0][1])


def read_license(d):
    lf = find_license_file(d)
    if not lf:
        return "NO-LICENSE-FILE", None, None
    try:
        with open(lf, encoding="utf-8", errors="replace") as fh:
            txt = fh.read()
    except OSError as e:
        return "ERR:" + str(e), os.path.basename(lf), None
    return classify(txt), os.path.basename(lf), txt


# ---------------------------------------------------------------------------
# Go modules
# ---------------------------------------------------------------------------
def modcache_path(cache, path, version):
    # Go escapes uppercase letters as !<lower>.
    enc = re.sub(r"([A-Z])", lambda m: "!" + m.group(1).lower(), path)
    return os.path.join(cache, enc + "@" + version) if version else None


# SHIPPED_TAGS must match what a release binary is actually built with: the
# Dockerfile's BUILD_TAGS, .github/scripts/build-release-binary.sh, and the
# Makefile's LICENSE_TAGS. `go list -deps` reports the dependency graph for the
# BUILD CONFIGURATION it is given, so a shorter list silently omits real shipped
# dependencies rather than failing — which is the worst possible failure mode for
# an attribution file.
#
# Measured on 2026-07-28: scanning with the default (empty) tag set produced
# notices listing 257 modules where the shipped configuration links 352. The 95
# missing ones were the whole otelcol collector tree, plus imbh-go (Apache-2.0)
# and sable (MIT) and their arrow/flatbuffers dependencies, whose bindings
# compile as stubs without `imbh`/`sable_extern_lib`. Nothing warned; the file
# just came out shorter.
SHIPPED_TAGS = "netgo,osusergo,otelcol,imbh,sable_extern_lib"


def shipped_tags(repo):
    """The shipped build tags, read from the Makefile's LICENSE_TAGS.

    Read rather than hardcoded so this cannot drift from what actually ships —
    which is the same thing ci.yml does for its notices-drift gate, and the
    reason the two agree. SHIPPED_TAGS above is only the fallback for a tree
    where the Makefile cannot be read; a silent divergence between the local
    scan and CI's would show up as an unexplained "notices are STALE" failure.
    """
    try:
        with open(os.path.join(repo, "Makefile"), encoding="utf-8") as fh:
            for line in fh:
                if line.startswith("LICENSE_TAGS"):
                    _, _, value = line.partition("=")
                    value = value.strip()
                    if value:
                        return value
    except OSError:
        pass
    return SHIPPED_TAGS


def parse_platforms(spec):
    """Parse "linux/amd64,darwin/arm64" into [("linux","amd64"), ...]."""
    out = []
    for item in spec.split(","):
        item = item.strip()
        if not item:
            continue
        if item.count("/") != 1:
            raise ValueError("platform %r is not GOOS/GOARCH" % item)
        goos, goarch = item.split("/")
        if not goos or not goarch:
            raise ValueError("platform %r is not GOOS/GOARCH" % item)
        out.append((goos, goarch))
    if not out:
        raise ValueError("no platforms given")
    return out


def scan_go(repo, pkgs, tags=SHIPPED_TAGS, platforms=None):
    """Inventory the Go modules compiled into the shipped binaries.

    `go list -deps` is PLATFORM-SENSITIVE: which packages a build reaches
    depends on GOOS/GOARCH, so a single-platform scan under-reports every module
    only another release target links (linux/amd64, for instance, additionally
    pulls in klauspost/cpuid and tonistiigi/go-archvariant). When platforms is
    given, the module sets of all of them are UNIONED, so the notices cover every
    binary the release ships rather than the one architecture they were generated
    on. platforms=None keeps the ambient GOOS/GOARCH — the single-platform
    behaviour this had before.

    Module VERSIONS do not vary by platform (the build list comes from go.mod,
    which is platform-independent); only reachability does. A disagreement would
    mean the resolution itself is unstable, so it is reported rather than
    silently resolved by last-write-wins.
    """
    go = os.environ.get("GO", "go")
    env = dict(os.environ)
    if tags:
        # Through GOFLAGS rather than an argv -tags, so it also reaches the
        # toolchain's own sub-invocations.
        env["GOFLAGS"] = (env.get("GOFLAGS", "") + " -tags=" + tags).strip()
    cache = subprocess.run([go, "env", "GOMODCACHE"], cwd=repo, env=env,
                           capture_output=True, text=True).stdout.strip()
    # `.Dir` is the module's real on-disk root as go resolved it. For a module
    # replaced by a local directory (`replace X => ./third_party/x` in go.mod)
    # that is the in-repo fork, NOT a module-cache entry: the original zip is
    # never downloaded, so deriving the path from the cache alone reads as
    # NO-LICENSE-FILE on a clean checkout and fails the policy gate — while
    # passing on any machine whose cache still holds a pre-fork download.
    # Tab-separated because a module directory may contain spaces.
    mods = {}
    for goos, goarch in (platforms or [(None, None)]):
        penv = dict(env)
        label = "ambient"
        if goos and goarch:
            penv["GOOS"], penv["GOARCH"] = goos, goarch
            label = "%s/%s" % (goos, goarch)
        out = subprocess.run(
            [go, "list", "-deps", "-f",
             '{{with .Module}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}', *pkgs],
            cwd=repo, env=penv, capture_output=True, text=True)
        if out.returncode != 0:
            sys.stderr.write("go list failed for %s:\n%s\n" % (label, out.stderr))
        seen = 0
        for line in out.stdout.splitlines():
            parts = line.split("\t")
            if len(parts) != 3 or not parts[1]:
                continue  # no version: the main module itself, which we never scan
            seen += 1
            prev = mods.get(parts[0])
            if prev is not None and prev[0] != parts[1]:
                sys.stderr.write(
                    "WARNING: %s resolves to %s on %s but %s elsewhere; "
                    "the notices would understate one of them\n"
                    % (parts[0], parts[1], label, prev[0]))
            # A module reached on several platforms keeps the first .Dir seen;
            # they are the same module version, so the license text is identical.
            if prev is None:
                mods[parts[0]] = (parts[1], parts[2])
        if platforms:
            sys.stderr.write("  %s: %d shipped modules\n" % (label, seen))
    results = []
    for path, (ver, mdir) in sorted(mods.items()):
        d = mdir or modcache_path(cache, path, ver)
        lic, fn, _ = read_license(d)
        if lic in ("NO-LICENSE-FILE", "UNKNOWN") and path in KNOWN_MODULE_LICENSES:
            lic, fn = KNOWN_MODULE_LICENSES[path]
        results.append({"module": path, "version": ver, "license": lic,
                        "license_file": fn, "category": category(lic),
                        "dir": d})
    return results


# ---------------------------------------------------------------------------
# npm modules (bundled web SPA)
# ---------------------------------------------------------------------------
def npm_license_of(pkg_dir):
    pj = os.path.join(pkg_dir, "package.json")
    lic = None
    if os.path.exists(pj):
        try:
            d = json.load(open(pj))
            lic = d.get("license") or d.get("licenses")
            if isinstance(lic, list):
                lic = " OR ".join(
                    x.get("type", "?") if isinstance(x, dict) else str(x) for x in lic)
            if isinstance(lic, dict):
                lic = lic.get("type", "?")
        except (OSError, ValueError):
            pass
    # Cross-check against the actual LICENSE file when the field is vague.
    if not lic or lic in ("SEE LICENSE IN LICENSE", "UNLICENSED"):
        detected, _, _ = read_license(pkg_dir)
        if detected not in ("NO-LICENSE-FILE", "UNKNOWN"):
            lic = detected
    return lic or "UNSPECIFIED"


def iter_npm_pkgs(nm):
    """Yield (name, dir) for every package under a node_modules tree."""
    if not os.path.isdir(nm):
        return
    for name in sorted(os.listdir(nm)):
        if name in (".bin", ".cache") or name.startswith(".") and name != ".pnpm":
            continue
        p = os.path.join(nm, name)
        if name.startswith("@"):
            for sub in sorted(os.listdir(p)):
                yield f"{name}/{sub}", os.path.join(p, sub)
                nested = os.path.join(p, sub, "node_modules")
                yield from iter_npm_pkgs(nested)
        else:
            yield name, p
            yield from iter_npm_pkgs(os.path.join(p, "node_modules"))


def scan_npm(repo):
    webpj = os.path.join(repo, "web", "package.json")
    nm = os.path.join(repo, "web", "node_modules")
    runtime = set()
    if os.path.exists(webpj):
        runtime = set(json.load(open(webpj)).get("dependencies", {}).keys())
    seen, results = {}, []
    for name, d in iter_npm_pkgs(nm):
        if name in seen:
            continue
        seen[name] = True
        lic = npm_license_of(d)
        results.append({"module": name, "version": "", "license": str(lic),
                        "runtime": name in runtime, "category": category(str(lic)),
                        "dir": d})
    return results, sorted(runtime)


# ---------------------------------------------------------------------------
# third_party/ vendored-and-possibly-modified copies
# ---------------------------------------------------------------------------
def read_readme_upstream(d):
    """Upstream repo named by a `| Upstream | <url> |` row in the copy's README.

    Returned in go.mod form (host/path, no scheme or trailing slash) so vendored
    copies read the same in the notices whether their origin came from a go.mod
    or from prose. Returns None when there is no README or no such row.
    """
    for fn in ("README.md", "README"):
        p = os.path.join(d, fn)
        if not os.path.exists(p):
            continue
        with open(p, errors="replace") as f:
            for line in f:
                cells = [c.strip() for c in line.strip().strip("|").split("|")]
                if len(cells) == 2 and cells[0].lower() == "upstream" and cells[1]:
                    return re.sub(r"^https?://", "", cells[1]).rstrip("/")
    return None


def read_readme_modified(d):
    """Whether the copy is MODIFIED, from a `| Modified | yes |` row in its README.

    This is read rather than inferred because the notices must say so for every
    license, not just the one that happens to compel it. It used to be inferred
    from the license category (MPL => modified), which was true only because the
    single modified copy at the time was MPL; an Apache-2.0 or ISC fork would have
    been listed as if it were a pristine copy, which is the exact thing Apache
    section 4(b) is about. Absent row means unmodified.
    """
    for fn in ("README.md", "README"):
        p = os.path.join(d, fn)
        if not os.path.exists(p):
            continue
        with open(p, errors="replace") as f:
            for line in f:
                cells = [c.strip() for c in line.strip().strip("|").split("|")]
                if len(cells) == 2 and cells[0].lower() == "modified":
                    return cells[1].strip().lower() in ("yes", "true")
    return False


def scan_vendored(repo):
    tp = os.path.join(repo, "third_party")
    out = []
    if not os.path.isdir(tp):
        return out
    for name in sorted(os.listdir(tp)):
        d = os.path.join(tp, name)
        if not os.path.isdir(d):
            continue
        lic, fn, _ = read_license(d)
        origin = None
        gm = os.path.join(d, "go.mod")
        if os.path.exists(gm):
            first = open(gm).readline().strip()
            if first.startswith("module "):
                origin = first[len("module "):].strip()
        if origin is None:
            # A vendored copy need not be a Go module of its own — third_party/herdr
            # is a directory of TOML manifests compiled into the parent module, so it
            # has no go.mod to name its upstream. Fall back to the `| Upstream | ... |`
            # row of the provenance table those READMEs carry; without it the notices
            # would attribute the component to a literal "None".
            origin = read_readme_upstream(d)
        out.append({"path": os.path.relpath(d, repo), "origin": origin,
                    "license": lic, "license_file": fn, "category": category(lic),
                    "modified": read_readme_modified(d)})
    return out


# ---------------------------------------------------------------------------
def summarize(title, rows, key="module"):
    groups = defaultdict(list)
    for r in rows:
        groups[r["license"]].append(r)
    print(f"\n===== {title}: {len(rows)} modules =====")
    order = sorted(groups, key=lambda k: (category(k) != "permissive",
                                          category(k) != "weak-copyleft", -len(groups[k]), k))
    for lic in order:
        cat = category(lic)
        mark = {"permissive": "  ", "weak-copyleft": "! ",
                "strong-copyleft": "!!", "review": "??"}[cat]
        print(f"{mark} {lic:16} {cat:16} x{len(groups[lic])}")
    # Always spell out anything that is not plainly permissive.
    flagged = [r for r in rows if r["category"] != "permissive"]
    if flagged:
        print(f"  -- non-permissive / needs review ({len(flagged)}) --")
        for r in sorted(flagged, key=lambda r: (r["category"], r[key])):
            extra = "  [RUNTIME/bundled]" if r.get("runtime") else ""
            print(f"     {r['category']:15} {r['license']:14} {r[key]} {r.get('version','')}{extra}")


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--repo", default=".", help="repo root (default: cwd)")
    ap.add_argument("--pkgs", nargs="*", default=["./cmd/..."],
                    help="Go packages whose shipped deps to scan (default ./cmd/...)")
    ap.add_argument("--json", help="write full inventory JSON to this path")
    ap.add_argument("--tags", default=None,
                    help="comma-separated build tags for the dependency walk "
                         "(default: the shipped set, %(default)s). Pass an empty "
                         "string to scan the untagged build — which under-reports "
                         "what ships and must not be used to regenerate notices.")
    ap.add_argument("--platforms", default=None,
                    help="comma-separated GOOS/GOARCH list to UNION the Go module "
                         "set over (e.g. the released targets "
                         "'linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64'). "
                         "Default: the ambient GOOS/GOARCH only.")
    ap.add_argument("--skip-go", action="store_true")
    ap.add_argument("--skip-npm", action="store_true")
    args = ap.parse_args()
    repo = os.path.abspath(args.repo)
    if args.tags is None:
        args.tags = shipped_tags(repo)

    inventory = {"go": [], "npm": [], "npm_runtime": [], "vendored": []}

    if not args.skip_go:
        platforms = parse_platforms(args.platforms) if args.platforms else None
        inventory["go"] = scan_go(repo, args.pkgs, args.tags, platforms)
        print("  build tags: %s" % (args.tags or "<none — UNDER-REPORTS what ships>"))
        summarize("Go shipped modules (compiled into the binaries)", inventory["go"])

    if not args.skip_npm:
        inventory["npm"], inventory["npm_runtime"] = scan_npm(repo)
        if inventory["npm"]:
            summarize("npm modules under web/node_modules", inventory["npm"])
            print("\n  Runtime (bundled into the SPA) deps declared in web/package.json:")
            byname = {r["module"]: r for r in inventory["npm"]}
            for n in inventory["npm_runtime"]:
                r = byname.get(n)
                print(f"     {n}: {r['license'] if r else 'NOT INSTALLED'}")

    inventory["vendored"] = scan_vendored(repo)
    if inventory["vendored"]:
        print("\n===== Vendored copies under third_party/ (verify modifications honor the license) =====")
        for v in inventory["vendored"]:
            mark = "!!" if v["category"] == "strong-copyleft" else \
                   "! " if v["category"] == "weak-copyleft" else "  "
            print(f"{mark} {v['path']}  origin={v['origin']}  {v['license']} ({v['category']})")

    # Verdict.
    all_rows = inventory["go"] + inventory["npm"] + \
        [{**v, "module": v["path"]} for v in inventory["vendored"]]
    strong = [r for r in all_rows if r["category"] == "strong-copyleft"]
    review = [r for r in all_rows if r["category"] == "review"]
    print("\n===== VERDICT =====")
    print(f"  strong-copyleft (GPL/LGPL/AGPL): {len(strong)}"
          + ("  <-- BLOCKER, investigate" if strong else "  (none)"))
    print(f"  needs manual review (unknown/missing license): {len(review)}")
    weak = [r for r in all_rows if r["category"] == "weak-copyleft"]
    print(f"  weak-copyleft (MPL/EPL/CC-BY): {len(weak)} — compatible, but honor notice/disclosure terms")

    if args.json:
        with open(args.json, "w") as fh:
            json.dump(inventory, fh, indent=2)
        print(f"\nWrote inventory JSON -> {args.json}")

    # Exit non-zero if a genuine blocker or unreviewed unknown is present.
    sys.exit(1 if (strong or review) else 0)


if __name__ == "__main__":
    main()
