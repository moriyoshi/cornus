#!/usr/bin/env python3
"""Validate every in-site link FRAGMENT against the ids VitePress actually generated.

`ignoreDeadLinks: false` makes VitePress fail the build on a dead ROUTE, but it
never checks the `#fragment` part. That gap is not theoretical: it hides a whole
class of silently broken links, and the Japanese pages hit it systematically.

markdown-it-anchor slugifies a heading AFTER Unicode decomposition, so a Japanese
heading like `## ポートではなく…` produces an id whose dakuten are separate
combining code points — 22 of them where the composed heading has 19. A
hand-authored fragment is composed (NFC). Browsers compare fragment to id by
exact code points, with no normalization, so the link is dead. The two spellings
are byte-different but visually identical in an editor, a diff, and a review,
which is why this survives review every time.

Run it against a built site, from the repository root:

    (cd docs && npm run docs:build)
    python3 .agents/skills/assess-doc-quality/scripts/check_anchors.py

or, from `docs/`, via the wrapper npm script:

    npm run docs:build
    npm run docs:check-anchors

Failures are reported as one line per dead fragment, with the reason:

    normalization  the target id exists but in a different Unicode form. Copy the
                   id this script prints; do NOT retype it.
    missing        no such heading on the target page (a genuine broken link, or
                   a section that page has not been given yet).

Exits non-zero when anything is dead, so it can gate CI.

Scope, stated so the count is not mistaken for more than it is:

  checked     site-absolute (`/a/b#f`), relative (`./b#f`, `../a/b#f`) and
              fragment-only (`#f`) links, with or without a `.md`/`.html` route
              suffix and with or without a link title (`](/a/b#f "t")`). Routes
              resolve to `<route>.html` or `<route>/index.html`, whichever the
              build emitted.
  not checked external URLs and `mailto:`; anything inside a fenced code block or
              an inline code span (documentation ABOUT links is not a link).
  skipped     fragments whose target page is not in the build. That is a dead
              ROUTE, which the build itself already fails on; this script counts
              them and says so rather than folding them into `checked`.
"""

from __future__ import annotations

import argparse
import os
import posixpath
import re
import sys
import unicodedata
import urllib.parse

ID_RE = re.compile(r'id="([^"]*)"')

# A markdown inline link's destination, with an optional title. The destination
# cannot contain whitespace; the title may, so it is matched and discarded —
# without this arm, `](/a/b#f "t")` matches nothing at all and the link is
# checked by no one.
LINK_RE = re.compile(
    r"""\]\(\s*
        ([^\s)]+)                                  # destination
        (?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))?      # optional title, discarded
        \s*\)""",
    re.VERBOSE,
)

FENCE_RE = re.compile(r"^\s{0,3}(`{3,}|~{3,})")
# Deliberately single-line: a multi-line span is vanishingly rare in these docs,
# while a stray unmatched backtick under a multi-line pattern would blank out the
# rest of the page and silently stop checking its links. Err toward checking.
INLINE_CODE_RE = re.compile(r"(?P<t>`+)[^\n]*?(?P=t)")

# Directories that hold markdown nobody in this repo authored (or the build's own
# output). `--src .` from `docs/` would otherwise walk into `node_modules` and
# judge a dependency's README against this site's routes.
SKIP_DIRS = {".vitepress", "node_modules", "dist"}


def strip_code(text: str) -> str:
    """Blank out fenced blocks and inline code, preserving line structure.

    A page that documents a link — `](/guides/x#y)` in a code sample — must not be
    audited as if it contained one. Lines are replaced rather than deleted so any
    future line-number reporting stays honest.
    """
    out, fence = [], None
    for line in text.split("\n"):
        match = FENCE_RE.match(line)
        if fence is None:
            if match:
                # Remember the run's length: per CommonMark a closing fence must
                # be at least as long as the opening one, so a ```` block may
                # contain ``` lines without ending there.
                fence = (match.group(1)[0], len(match.group(1)))
                out.append("")
                continue
        else:
            out.append("")
            if match and match.group(1)[0] == fence[0] and len(match.group(1)) >= fence[1]:
                fence = None
            continue
        out.append(line)
    return INLINE_CODE_RE.sub(lambda m: " " * len(m.group(0)), "\n".join(out))


def candidate_paths(dist: str, route: str) -> list[str]:
    """Built files a route may have been emitted as.

    VitePress writes `/a/b` as `a/b.html` and `/a/b/` (a directory route, e.g.
    `/guides/`) as `a/b/index.html`. Authors write both, so try both.
    """
    clean = route
    for suffix in (".md", ".html"):
        if clean.endswith(suffix):
            clean = clean[: -len(suffix)]
    clean = clean.strip("/")
    if not clean:
        return [os.path.join(dist, "index.html")]
    return [
        os.path.join(dist, *clean.split("/")) + ".html",
        os.path.join(dist, *clean.split("/"), "index.html"),
    ]


def page_ids(dist: str, route: str, cache: dict[str, set[str] | None]) -> set[str] | None:
    """Heading ids of a built page, or None when the page does not exist."""
    if route not in cache:
        cache[route] = None
        for path in candidate_paths(dist, route):
            try:
                with open(path, encoding="utf-8") as fh:
                    cache[route] = set(ID_RE.findall(fh.read()))
                break
            except OSError:
                continue
    return cache[route]


def resolve(target: str, own_route: str) -> str:
    """A link destination as a site-absolute route."""
    if target.startswith("/"):
        return target
    return posixpath.normpath(posixpath.join(posixpath.dirname(own_route), target))


def markdown_files(src: str):
    for root, dirs, names in os.walk(src):
        # Prune in place so os.walk never descends; a substring test on `root`
        # would still pay for the descent and would match a path that merely
        # contains the name.
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for name in sorted(names):
            if name.endswith(".md") and name != "README.md":
                yield os.path.join(root, name)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--src", default="docs", help="markdown root (default: docs)")
    ap.add_argument("--dist", default="docs/.vitepress/dist", help="built site root")
    args = ap.parse_args()

    if not os.path.isdir(args.dist):
        print(f"{args.dist} not found — run `npm run docs:build` first", file=sys.stderr)
        return 2

    cache: dict[str, set[str] | None] = {}
    dead: list[tuple[str, str, str, str, str]] = []
    unbuilt: dict[str, int] = {}
    checked = 0

    for path in markdown_files(args.src):
        with open(path, encoding="utf-8") as fh:
            text = strip_code(fh.read())
        own_route = "/" + os.path.relpath(path, args.src).removesuffix(".md").replace(os.sep, "/")
        for match in LINK_RE.finditer(text):
            dest = match.group(1)
            if "#" not in dest or dest.startswith(("http://", "https://", "mailto:")):
                continue
            route, _, raw_fragment = dest.partition("#")
            if not raw_fragment:
                continue
            target = resolve(route, own_route) if route else own_route
            fragment = urllib.parse.unquote(raw_fragment)
            ids = page_ids(args.dist, target, cache)
            if ids is None:
                # A dead ROUTE is already the build's job to catch; do not
                # duplicate (or contradict) it here — but do not pretend the
                # fragment was checked either.
                unbuilt[target] = unbuilt.get(target, 0) + 1
                continue
            checked += 1
            if fragment in ids:
                continue
            same = [
                i for i in ids
                if unicodedata.normalize("NFC", i) == unicodedata.normalize("NFC", fragment)
            ]
            reason = "normalization" if same else "missing"
            dead.append((path, target, fragment, reason, same[0] if same else ""))

    for path, target, fragment, reason, suggestion in dead:
        line = f"{reason:13} {path} -> {target}#{fragment}"
        if suggestion:
            line += f"\n              the built page has this id instead: {target}#{suggestion}"
        print(line)

    print(f"\nchecked {checked} fragment link(s); {len(dead)} dead")
    if unbuilt:
        total = sum(unbuilt.values())
        print(f"{total} fragment link(s) NOT checked: target page not in the build (dead route — the build's job)")
        for target, count in sorted(unbuilt.items(), key=lambda kv: (-kv[1], kv[0]))[:10]:
            print(f"              {count:4}  {target}")
        if len(unbuilt) > 10:
            print(f"              ... and {len(unbuilt) - 10} more route(s)")
    return 1 if dead else 0


if __name__ == "__main__":
    sys.exit(main())
