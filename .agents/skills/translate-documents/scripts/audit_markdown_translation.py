#!/usr/bin/env python3
"""Audit structural invariants between Markdown source and translation trees."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from collections import Counter
from urllib.parse import urlsplit, urlunsplit


FRONTMATTER_KEY = re.compile(r"^(\s*)(?:-\s+)?([A-Za-z_][A-Za-z0-9_-]*):(?:\s|$)")
FENCE = re.compile(r"^\s*(`{3,}|~{3,})(.*)$")
HEADING = re.compile(r"^(#{1,6})\s+")
# A code span may WRAP across lines: CommonMark folds an interior newline into a
# space, so `cornus config set-context\n--namespace <ns>` is one span, not two
# stray backticks. Forbidding \r\n here used to lose both real spans on such a
# line AND invent a third from the now-mismatched backticks, flagging 7 ja and 8
# zh files for a property of the ENGLISH source's line breaks. The span may not
# cross a blank line, which ends the paragraph; that is enforced below rather
# than in the pattern, because a negative lookahead for \n\n inside a character
# class the span already can't leave only makes the regex harder to read.
INLINE_CODE = re.compile(r"(?<!`)`([^`]+)`(?!`)")
LINK = re.compile(r"(!?)\[[^\]]*\]\(([^\s)]+)(?:\s+[\"'][^)]*[\"'])?\)")
ASSET_EXTENSIONS = {
    ".avif",
    ".gif",
    ".ico",
    ".jpeg",
    ".jpg",
    ".pdf",
    ".png",
    ".svg",
    ".webp",
}
DEFAULT_EXCLUDES = (Path("node_modules"),)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source_root", type=Path)
    parser.add_argument("target_root", type=Path)
    parser.add_argument(
        "--locale-prefix",
        required=True,
        help="Site prefix for target documentation routes, for example /ja",
    )
    parser.add_argument(
        "--path",
        action="append",
        default=[],
        dest="paths",
        help="Markdown path relative to both roots; repeat for a partial audit",
    )
    parser.add_argument(
        "--exclude",
        action="append",
        default=[],
        help="Source-root path to exclude during a full-tree audit; repeat as needed",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="Treat inline-code and link-sequence review warnings as errors",
    )
    return parser.parse_args()


def frontmatter_keys(text: str) -> list[tuple[int, str]]:
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        return []
    keys: list[tuple[int, str]] = []
    for line in lines[1:]:
        if line.strip() == "---":
            return keys
        match = FRONTMATTER_KEY.match(line)
        if match:
            keys.append((len(match.group(1)), match.group(2)))
    return keys


def markdown_shape(text: str) -> tuple[list[int], list[str], list[str]]:
    headings: list[int] = []
    fences: list[str] = []
    prose_lines: list[str] = []
    active_fence: str | None = None

    for line in text.splitlines():
        fence = FENCE.match(line)
        if fence:
            marker = fence.group(1)
            if active_fence is None:
                active_fence = marker[0]
                fences.append(fence.group(2).strip())
            elif marker[0] == active_fence:
                active_fence = None
            continue
        if active_fence is not None:
            continue
        heading = HEADING.match(line)
        if heading:
            headings.append(len(heading.group(1)))
        prose_lines.append(line)

    inline_code = inline_code_spans("\n".join(prose_lines))
    return headings, fences, inline_code


def inline_code_spans(text: str) -> list[str]:
    """Return the inline code spans in text, with interior whitespace folded.

    Folding matters as much as the pattern: the source and the translation are
    free to break lines in different places, so the SAME span reads
    "cornus config set-context\\n--namespace <ns>" in one and
    "cornus config set-context --namespace <ns>" in the other. Comparing them
    raw reports a difference that exists only in the line wrapping. A span that
    contains a blank line is not a span at all (the paragraph ended), so it is
    dropped rather than folded.
    """
    spans = []
    for span in INLINE_CODE.findall(text):
        if "\n\n" in span:
            continue
        spans.append(" ".join(span.split()))
    return spans


def links(text: str) -> list[tuple[bool, str]]:
    prose_lines: list[str] = []
    active_fence: str | None = None
    for line in text.splitlines():
        fence = FENCE.match(line)
        if fence:
            marker = fence.group(1)
            if active_fence is None:
                active_fence = marker[0]
            elif marker[0] == active_fence:
                active_fence = None
            continue
        if active_fence is None:
            prose_lines.append(line)
    return [
        (bool(image), destination)
        for image, destination in LINK.findall("\n".join(prose_lines))
    ]


def expected_link(destination: str, is_image: bool, locale_prefix: str) -> str:
    if is_image or not destination.startswith("/") or destination.startswith("//"):
        return destination

    parsed = urlsplit(destination)
    if Path(parsed.path).suffix.lower() in ASSET_EXTENSIONS:
        return destination

    prefix = "/" + locale_prefix.strip("/")
    if parsed.path == prefix or parsed.path.startswith(prefix + "/"):
        return destination

    localized_path = prefix + (parsed.path if parsed.path != "/" else "/")
    return urlunsplit(
        (parsed.scheme, parsed.netloc, localized_path, parsed.query, parsed.fragment)
    )


def link_key(link: tuple[bool, str]) -> tuple[bool, str]:
    """Reduce a link to what a translation must preserve: image-ness and path.

    The #fragment is deliberately excluded — see compare_file. Query is kept: it
    is part of the destination and nothing localizes it.

    A SAME-PAGE anchor ("#some-heading") has nothing left once the fragment goes,
    so it would key on the empty string and report as `missing ` with no
    destination to act on. Such links collapse to one named placeholder instead:
    all that can be asserted about them is that the translation carries as many
    as the source, which is still worth asserting — a dropped one means a
    cross-reference vanished.
    """
    is_image, destination = link
    parsed = urlsplit(destination)
    path = urlunsplit((parsed.scheme, parsed.netloc, parsed.path, parsed.query, ""))
    if not path and parsed.fragment:
        return (is_image, "#(same-page anchor)")
    return (is_image, path)


def unprefixed_doc_links(text: str, locale_prefix: str) -> list[str]:
    prefix = "/" + locale_prefix.strip("/")
    invalid: list[str] = []
    for is_image, destination in links(text):
        if is_image or not destination.startswith("/") or destination.startswith("//"):
            continue
        parsed = urlsplit(destination)
        if Path(parsed.path).suffix.lower() in ASSET_EXTENSIONS:
            continue
        if parsed.path != prefix and not parsed.path.startswith(prefix + "/"):
            invalid.append(destination)
    return invalid


def compare_file(
    source: Path, target: Path, locale_prefix: str
) -> tuple[list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    source_text = source.read_text(encoding="utf-8")
    target_text = target.read_text(encoding="utf-8")

    if not target_text.strip():
        return ["target file is empty"], warnings

    if frontmatter_keys(source_text) != frontmatter_keys(target_text):
        errors.append("front matter key structure differs")

    source_headings, source_fences, source_code = markdown_shape(source_text)
    target_headings, target_fences, target_code = markdown_shape(target_text)
    if source_headings != target_headings:
        errors.append("heading-level sequence differs")
    if source_fences != target_fences:
        errors.append("fenced-block count or language identifiers differ")
    # Inline code is compared as a MULTISET, not a sequence. Word order is exactly
    # what a translation is allowed to change, so an ordered comparison flags a
    # faithful page whenever a sentence puts two code spans the other way round —
    # measured at 21 of 34 ja and 28 of 37 zh warnings, i.e. most of them. That
    # noise buried the real ones. What matters is that the same spans are PRESENT:
    # a missing one means the translation dropped a flag, key, or value the source
    # documents, and an extra one usually means it documents something the source
    # does not. Naming them turns a boolean into something actionable.
    missing = Counter(source_code) - Counter(target_code)
    extra = Counter(target_code) - Counter(source_code)
    if missing or extra:
        detail = []
        if missing:
            detail.append("missing " + ", ".join(f"`{s}`" for s in sorted(missing.elements())))
        if extra:
            detail.append("extra " + ", ".join(f"`{s}`" for s in sorted(extra.elements())))
        warnings.append("inline code differs: " + "; ".join(detail))

    # Link destinations are compared as a multiset of (is_image, path) — WITHOUT the
    # #fragment, and without regard to order.
    #
    # Dropping the fragment is not a relaxation, it is a correction. A link into a
    # translated heading points at a slug derived from the TRANSLATED text, so it
    # can never equal the source's fragment; expected_link localizes the path and
    # has no way to localize the fragment. Comparing them made a correct link look
    # wrong: 25 of 26 ja warnings and 24 of 26 zh were exactly this, by
    # construction. Whether a fragment resolves is a real question, and
    # `docs:check-anchors` answers it properly, against the ids VitePress actually
    # emitted — which this script cannot see. Two checks, one of which is wrong by
    # design, is worse than one that is right.
    #
    # Order is dropped for the same reason as inline code: a translation may
    # reorder the sentences that carry the links.
    expected_links = [
        (is_image, expected_link(destination, is_image, locale_prefix))
        for is_image, destination in links(source_text)
    ]
    missing_links = Counter(map(link_key, expected_links)) - Counter(
        map(link_key, links(target_text))
    )
    extra_links = Counter(map(link_key, links(target_text))) - Counter(
        map(link_key, expected_links)
    )
    if missing_links or extra_links:
        detail = []
        if missing_links:
            detail.append("missing " + ", ".join(sorted(d for _, d in missing_links.elements())))
        if extra_links:
            detail.append("extra " + ", ".join(sorted(d for _, d in extra_links.elements())))
        warnings.append("link/image destinations differ: " + "; ".join(detail))
    invalid_links = unprefixed_doc_links(target_text, locale_prefix)
    if invalid_links:
        errors.append(
            "site-absolute documentation links lack locale prefix: "
            + ", ".join(invalid_links)
        )

    return errors, warnings


def selected_paths(args: argparse.Namespace) -> tuple[list[Path], list[str]]:
    errors: list[str] = []
    if args.paths:
        paths = [Path(path) for path in args.paths]
        for path in paths:
            if path.is_absolute() or ".." in path.parts:
                errors.append(f"unsafe relative --path: {path}")
            if path.suffix.lower() != ".md":
                errors.append(f"--path is not a Markdown file: {path}")
        return paths, errors

    excluded = [*DEFAULT_EXCLUDES, *(Path(path) for path in args.exclude)]
    source_paths = {
        path.relative_to(args.source_root)
        for path in args.source_root.rglob("*.md")
        if not any(
            path.relative_to(args.source_root) == prefix
            or prefix in path.relative_to(args.source_root).parents
            for prefix in excluded
        )
    }
    target_paths = {
        path.relative_to(args.target_root)
        for path in args.target_root.rglob("*.md")
    }
    for path in sorted(source_paths - target_paths):
        errors.append(f"missing target file: {path}")
    for path in sorted(target_paths - source_paths):
        errors.append(f"target has no source file: {path}")
    return sorted(source_paths & target_paths), errors


def main() -> int:
    args = parse_args()
    args.locale_prefix = "/" + args.locale_prefix.strip("/")

    if not args.source_root.is_dir():
        print(f"error: source root is not a directory: {args.source_root}", file=sys.stderr)
        return 2
    if not args.target_root.is_dir():
        print(f"error: target root is not a directory: {args.target_root}", file=sys.stderr)
        return 2

    paths, errors = selected_paths(args)
    warnings: list[str] = []
    for relative in paths:
        source = args.source_root / relative
        target = args.target_root / relative
        if not source.is_file():
            errors.append(f"missing source file: {relative}")
            continue
        if not target.is_file():
            errors.append(f"missing target file: {relative}")
            continue
        file_errors, file_warnings = compare_file(source, target, args.locale_prefix)
        errors.extend(f"{relative}: {message}" for message in file_errors)
        warnings.extend(f"{relative}: {message}" for message in file_warnings)

    for warning in warnings:
        print(f"WARNING: {warning}")
    if args.strict:
        errors.extend(warnings)

    if errors:
        for error in errors:
            print(f"ERROR: {error}")
        print(f"audit failed: {len(errors)} issue(s) across {len(paths)} compared file(s)")
        return 1

    print(f"audit passed: {len(paths)} file(s), {len(warnings)} review warning(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
