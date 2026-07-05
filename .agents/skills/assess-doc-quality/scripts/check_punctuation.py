#!/usr/bin/env python3
"""Reject prohibited full-width punctuation in authored Markdown prose.

The repository requires ASCII parentheses and colons in authored
documentation. Code and URL destinations are literal interfaces, so this
checker deliberately ignores fenced code, inline code, Markdown link
destinations, and bare HTTP(S) URLs.
"""

from __future__ import annotations

import argparse
import re
import sys

from check_anchors import LINK_RE, markdown_files, strip_code


PROHIBITED = {"（", "）", "："}
BARE_URL_RE = re.compile(r"https?://[^\s<>()]+")


def strip_destinations(text: str) -> str:
    """Blank link destinations and bare URLs while preserving line/column shape."""

    text = LINK_RE.sub(lambda match: " " * len(match.group(0)), text)
    return BARE_URL_RE.sub(lambda match: " " * len(match.group(0)), text)


def punctuation_violations(path: str) -> list[tuple[int, int, str]]:
    """Return (line, column, character) records for one Markdown file."""

    with open(path, encoding="utf-8") as fh:
        text = strip_destinations(strip_code(fh.read()))

    violations: list[tuple[int, int, str]] = []
    for lineno, line in enumerate(text.splitlines(), 1):
        for column, char in enumerate(line, 1):
            if char in PROHIBITED:
                violations.append((lineno, column, char))
    return violations


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--src", default="docs", help="Markdown root (default: docs)")
    args = parser.parse_args()

    found = 0
    for path in markdown_files(args.src):
        for line, column, char in punctuation_violations(path):
            found += 1
            print(f"{path}:{line}:{column}: prohibited punctuation {char!r}")

    print(f"\nchecked Markdown prose; {found} punctuation violation(s)")
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
