#!/usr/bin/env python3
"""Audit structural invariants between Markdown source and translation trees."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit


FRONTMATTER_KEY = re.compile(r"^(\s*)(?:-\s+)?([A-Za-z_][A-Za-z0-9_-]*):(?:\s|$)")
FENCE = re.compile(r"^\s*(`{3,}|~{3,})(.*)$")
HEADING = re.compile(r"^(#{1,6})\s+")
INLINE_CODE = re.compile(r"(?<!`)`([^`\r\n]+)`(?!`)")
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

    inline_code = INLINE_CODE.findall("\n".join(prose_lines))
    return headings, fences, inline_code


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
    if source_code != target_code:
        warnings.append("inline code sequence differs")

    source_links = links(source_text)
    target_links = links(target_text)
    expected_links = [
        (is_image, expected_link(destination, is_image, locale_prefix))
        for is_image, destination in source_links
    ]
    if expected_links != target_links:
        warnings.append("link/image destination sequence differs from localized expectation")
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
