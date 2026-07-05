#!/usr/bin/env python3
"""Track which English source each translated page was written against.

A translated page that has silently fallen behind its source is indistinguishable
from a current one: it renders fine, it passes every structural check, and it
tells the reader something that is no longer true. That is worse than a missing
page, which at least announces itself.

The mechanism is deliberately small: a sidecar map from (locale, path) to the
SHA-256 of the English source as it stood when that translation was last synced.
`check` reports every page whose source has changed since; `update` records the
current digests for the pages you have just translated.

Why a sidecar and not page frontmatter: audit_markdown_translation.py treats a
front-matter key-structure difference between source and target as an ERROR, so a
digest key present only in translations would break the structural audit. The
sidecar also keeps a machine-maintained value out of pages humans edit, and out of
the VitePress frontmatter surface.

What this can and cannot tell you: a digest mismatch proves the SOURCE changed,
not that the translation is wrong — a typo fix in English does not invalidate a
Japanese page. It is a prompt to look, and `--porcelain` output is meant to be fed
back into `update` once you have looked. What it cannot detect is a translation
that was wrong the day it was written; nothing here substitutes for reading.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path

STATE_FILENAME = ".translation-state.json"
DEFAULT_EXCLUDES = ("node_modules", ".vitepress", "README.md")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=("check", "update"))
    parser.add_argument("--source-root", type=Path, default=Path("docs"))
    parser.add_argument(
        "--locale",
        action="append",
        default=[],
        dest="locales",
        help="Locale directory under the source root, e.g. ja; repeat for several",
    )
    parser.add_argument(
        "--path",
        action="append",
        default=[],
        dest="paths",
        help="Restrict `update` to these source-relative paths; repeat as needed",
    )
    parser.add_argument(
        "--porcelain",
        action="store_true",
        help="Print one `locale<TAB>path` per stale page and nothing else",
    )
    return parser.parse_args()


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_pages(source_root: Path, locales: list[str]) -> list[Path]:
    """Every English page, as paths relative to the source root."""
    skip = set(DEFAULT_EXCLUDES) | set(locales)
    pages = []
    for path in sorted(source_root.rglob("*.md")):
        rel = path.relative_to(source_root)
        if rel.parts[0] in skip or str(rel) in skip:
            continue
        pages.append(rel)
    return pages


def load_state(state_path: Path) -> dict:
    if not state_path.is_file():
        return {}
    try:
        return json.loads(state_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as err:
        print(f"error: {state_path} is not valid JSON: {err}", file=sys.stderr)
        raise SystemExit(2)


def save_state(state_path: Path, state: dict) -> None:
    # Sorted and newline-terminated so the file diffs cleanly and a re-run with no
    # changes produces no diff at all.
    state_path.write_text(json.dumps(state, indent=2, sort_keys=True, ensure_ascii=False) + "\n", encoding="utf-8")


def main() -> int:
    args = parse_args()
    locales = args.locales or ["ja", "zh"]
    source_root: Path = args.source_root
    if not source_root.is_dir():
        print(f"error: source root is not a directory: {source_root}", file=sys.stderr)
        return 2

    state_path = source_root / STATE_FILENAME
    state = load_state(state_path)
    pages = source_pages(source_root, locales)

    if args.command == "update":
        selected = {Path(p) for p in args.paths} if args.paths else None
        if selected is not None:
            # A --path that matches no source page is refused rather than skipped.
            # It used to record nothing and still print "recorded 0 ... " and exit 0,
            # which reads as success — the operator believes the pages are marked
            # reviewed and moves on, while `check` keeps reporting them stale. The
            # spelling that produces it is the obvious one: these paths are
            # SOURCE-relative, so `--path ja/cli/foo.md` (the file actually edited)
            # matches nothing. Silently recording nothing is the same class of defect
            # this tool exists to prevent, so it fails loudly and records NOTHING —
            # all-or-nothing, so a mistyped path cannot half-record a batch.
            unknown = sorted(str(p) for p in selected - set(pages))
            if unknown:
                print(
                    f"error: --path matched no source page: {', '.join(unknown)}\n"
                    "Paths are relative to the SOURCE root and name the ENGLISH page, not the\n"
                    "translation: use `--path cli/foo.md`, not `--path ja/cli/foo.md`. Every locale\n"
                    "that has that page is recorded together. Nothing was recorded.",
                    file=sys.stderr,
                )
                return 2
        recorded = 0
        for rel in pages:
            if selected is not None and rel not in selected:
                continue
            current = digest(source_root / rel)
            for locale in locales:
                if not (source_root / locale / rel).is_file():
                    continue
                state.setdefault(locale, {})[str(rel)] = current
                recorded += 1
        save_state(state_path, state)
        print(f"recorded {recorded} (locale, page) digest(s) in {state_path}")
        return 0

    stale: list[tuple[str, str]] = []
    untracked: list[tuple[str, str]] = []
    for rel in pages:
        current = digest(source_root / rel)
        for locale in locales:
            if not (source_root / locale / rel).is_file():
                continue  # a missing translation is the structural audit's business
            recorded = state.get(locale, {}).get(str(rel))
            if recorded is None:
                untracked.append((locale, str(rel)))
            elif recorded != current:
                stale.append((locale, str(rel)))

    if args.porcelain:
        for locale, rel in stale:
            print(f"{locale}\t{rel}")
        return 1 if stale else 0

    for locale, rel in untracked:
        print(f"UNTRACKED: {locale}/{rel} (no recorded source digest)")
    for locale, rel in stale:
        print(f"STALE: {locale}/{rel} — the English source changed after this translation was synced")

    if stale:
        print(
            f"\n{len(stale)} translated page(s) are behind their source.\n"
            "Read each against the current English, update it where the change is substantive, then record it:\n"
            f"  python3 {sys.argv[0]} update --path <page.md>\n"
            "A digest mismatch means the SOURCE moved, not that the translation is wrong — a typo fix\n"
            "in English does not invalidate a translated page. Recording it without looking is the one\n"
            "use that defeats the mechanism."
        )
        return 1
    if untracked:
        print(f"\n{len(untracked)} translated page(s) have no recorded digest; run `update` to establish a baseline.")
        return 1
    print(f"translation freshness: {len(pages)} source page(s) x {len(locales)} locale(s), all current")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
