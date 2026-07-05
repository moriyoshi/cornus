#!/usr/bin/env python3
"""Reject duplicate Markdown link destinations within one list.

VitePress accepts two differently labelled list entries that lead to the same
target. This checker catches that restructure defect while allowing intentional
repetition elsewhere in a long page.

Run from the repository root:

    python3 .agents/skills/assess-doc-quality/scripts/check_duplicate_targets.py

The checker ignores fenced and inline code, canonicalizes in-site relative
routes, route suffixes, percent escapes, and trailing slashes, and preserves
fragments because two sections of one page are distinct targets.
"""

from __future__ import annotations

import argparse
import os
import posixpath
import re
import sys
import urllib.parse

from check_anchors import LINK_RE, markdown_files, strip_code


LIST_ITEM_RE = re.compile(r"^(?P<indent>[ \t]*)(?:[-+*]|\d+[.)])\s+")


def canonical_target(destination: str, own_route: str) -> str:
    """Return a stable spelling for one destination."""
    destination = urllib.parse.unquote(destination.strip("<>"))
    split = urllib.parse.urlsplit(destination)
    if split.scheme or split.netloc:
        return destination

    path = split.path
    if path:
        if path.startswith("/"):
            path = posixpath.normpath(path)
        else:
            path = posixpath.normpath(
                posixpath.join(posixpath.dirname(own_route), path)
            )
        for suffix in (".md", ".html"):
            if path.endswith(suffix):
                path = path[: -len(suffix)]
        if path != "/":
            path = path.rstrip("/")
    else:
        path = own_route

    result = path
    if split.query:
        result += "?" + split.query
    if split.fragment:
        result += "#" + split.fragment
    return result


def duplicate_targets(path: str, src: str) -> list[tuple[int, int, str]]:
    """Return (first line, duplicate line, target) records for one page."""
    with open(path, encoding="utf-8") as fh:
        lines = strip_code(fh.read()).splitlines()

    own_route = (
        "/"
        + os.path.relpath(path, src).removesuffix(".md").replace(os.sep, "/")
    )
    seen: dict[str, int] | None = None
    base_indent = 0
    duplicates: list[tuple[int, int, str]] = []

    for lineno, line in enumerate(lines, 1):
        item = LIST_ITEM_RE.match(line)
        if item:
            indent = len(item.group("indent").expandtabs(4))
            if seen is None or indent < base_indent:
                seen = {}
                base_indent = indent
        elif seen is not None:
            if not line.strip():
                continue
            indent = len(line) - len(line.lstrip(" \t"))
            if indent <= base_indent:
                seen = None
                continue

        if seen is None:
            continue
        for match in LINK_RE.finditer(line):
            target = canonical_target(match.group(1), own_route)
            if target in seen and seen[target] != lineno:
                duplicates.append((seen[target], lineno, target))
            else:
                seen[target] = lineno

    return duplicates


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("--src", default="docs", help="markdown root (default: docs)")
    args = ap.parse_args()

    found = 0
    for path in markdown_files(args.src):
        for first, duplicate, target in duplicate_targets(path, args.src):
            found += 1
            print(
                f"{path}:{duplicate}: duplicate target {target} "
                f"(first used on line {first})"
            )

    print(f"\nchecked Markdown lists; {found} duplicate target(s)")
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
