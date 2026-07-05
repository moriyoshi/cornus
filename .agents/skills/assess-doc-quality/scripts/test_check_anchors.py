#!/usr/bin/env python3
"""Regression tests for check_anchors.py. Stdlib only, no network, no daemons.

    python3 -m unittest discover -s .agents/skills/assess-doc-quality/scripts
"""

from __future__ import annotations

import contextlib
import io
import os
import sys
import tempfile
import unicodedata
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import check_anchors  # noqa: E402


def write(path: str, text: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def built_page(*ids: str) -> str:
    headings = "".join(f'<h2 id="{i}">x</h2>' for i in ids)
    return f"<html><body>{headings}</body></html>"


def run(src: str, dist: str) -> tuple[int, str]:
    """Invoke main() with argv pointed at a fixture tree; capture its report."""
    argv = sys.argv
    sys.argv = ["check_anchors.py", "--src", src, "--dist", dist]
    out = io.StringIO()
    try:
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            code = check_anchors.main()
    finally:
        sys.argv = argv
    return code, out.getvalue()


class WalkTest(unittest.TestCase):
    def test_skips_vendored_and_generated_trees(self):
        """The walk must never judge markdown this repo did not author."""
        with tempfile.TemporaryDirectory() as root:
            write(os.path.join(root, "guides/ingress.md"), "x")
            write(os.path.join(root, "node_modules/vitepress/README.md"), "x")
            write(os.path.join(root, "node_modules/mermaid/CHANGELOG.md"), "x")
            write(os.path.join(root, "dist/generated.md"), "x")
            write(os.path.join(root, ".vitepress/theme/notes.md"), "x")
            write(os.path.join(root, "README.md"), "x")

            found = sorted(os.path.relpath(p, root) for p in check_anchors.markdown_files(root))

        self.assertEqual(found, [os.path.join("guides", "ingress.md")])

    def test_vendored_dead_fragment_does_not_fail_the_gate(self):
        """The regression: a dependency's site-absolute link is not ours to check."""
        with tempfile.TemporaryDirectory() as root:
            src, dist = os.path.join(root, "docs"), os.path.join(root, "dist")
            write(os.path.join(src, "guides/ingress.md"), "see [x](/guides/ingress#live)\n")
            write(
                os.path.join(src, "node_modules/some-dep/README.md.md"),
                "see [x](/guides/ingress#no-such-heading)\n",
            )
            write(os.path.join(dist, "guides/ingress.html"), built_page("live"))

            code, out = run(src, dist)

        self.assertEqual(code, 0, out)
        self.assertIn("0 dead", out)


class ClassificationTest(unittest.TestCase):
    def test_live_fragment_passes(self):
        with tempfile.TemporaryDirectory() as root:
            src, dist = os.path.join(root, "docs"), os.path.join(root, "dist")
            write(os.path.join(src, "cli/deploy.md"), "[a](/cli/deploy#flags)\n")
            write(os.path.join(dist, "cli/deploy.html"), built_page("flags"))

            code, out = run(src, dist)

        self.assertEqual(code, 0, out)
        self.assertIn("checked 1 fragment link(s); 0 dead", out)

    def test_missing_heading_is_reported(self):
        with tempfile.TemporaryDirectory() as root:
            src, dist = os.path.join(root, "docs"), os.path.join(root, "dist")
            write(os.path.join(src, "cli/deploy.md"), "[a](/cli/deploy#gone)\n")
            write(os.path.join(dist, "cli/deploy.html"), built_page("flags"))

            code, out = run(src, dist)

        self.assertEqual(code, 1, out)
        self.assertIn("missing", out)
        self.assertIn("/cli/deploy#gone", out)

    def test_decomposed_id_is_reported_as_normalization_with_the_exact_id(self):
        """The case the checker exists for: NFC fragment vs the NFD id VitePress emits."""
        composed = "ポートではなく"
        decomposed = unicodedata.normalize("NFD", composed)
        self.assertNotEqual(composed, decomposed)

        with tempfile.TemporaryDirectory() as root:
            src, dist = os.path.join(root, "docs"), os.path.join(root, "dist")
            write(os.path.join(src, "ja/guides/networking.md"), f"[a](/ja/guides/networking#{composed})\n")
            write(os.path.join(dist, "ja/guides/networking.html"), built_page(decomposed))

            code, out = run(src, dist)

        self.assertEqual(code, 1, out)
        self.assertIn("normalization", out)
        # The report must carry the id to paste, not just say "wrong form".
        self.assertIn(decomposed, out)

    def test_dead_route_is_left_to_the_build(self):
        with tempfile.TemporaryDirectory() as root:
            src, dist = os.path.join(root, "docs"), os.path.join(root, "dist")
            write(os.path.join(src, "cli/deploy.md"), "[a](/cli/nope#anything)\n")
            os.makedirs(dist)

            code, out = run(src, dist)

        self.assertEqual(code, 0, out)
        self.assertIn("checked 0 fragment link(s)", out)

    def test_external_links_are_out_of_scope(self):
        with tempfile.TemporaryDirectory() as root:
            src, dist = os.path.join(root, "docs"), os.path.join(root, "dist")
            write(
                os.path.join(src, "cli/deploy.md"),
                "[a](https://example.com/x#y) [b](mailto:x@y.z#w) [c](#own)\n",
            )
            write(os.path.join(dist, "cli/deploy.html"), built_page("own"))

            code, out = run(src, dist)

        # Only the fragment-only link, resolved against the page itself.
        self.assertEqual(code, 0, out)
        self.assertIn("checked 1 fragment link(s); 0 dead", out)

    def test_percent_encoded_fragment_is_decoded_before_comparison(self):
        with tempfile.TemporaryDirectory() as root:
            src, dist = os.path.join(root, "docs"), os.path.join(root, "dist")
            write(os.path.join(src, "ja/cli/deploy.md"), "[a](/ja/cli/deploy#%E3%83%95%E3%83%A9%E3%82%B0)\n")
            write(os.path.join(dist, "ja/cli/deploy.html"), built_page("フラグ"))

            code, out = run(src, dist)

        self.assertEqual(code, 0, out)
        self.assertIn("0 dead", out)


class WidenedScopeTest(unittest.TestCase):
    """Link forms the checker used to skip in silence. Each asserts BOTH that a
    live one passes and that a dead one is caught — a form that is merely 'not
    skipped' proves nothing if every verdict comes back live."""

    def check(self, page: str, dist_files: dict[str, str]) -> tuple[int, str]:
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        src, dist = os.path.join(root.name, "docs"), os.path.join(root.name, "dist")
        write(os.path.join(src, "guides/ingress.md"), page)
        for rel, html in dist_files.items():
            write(os.path.join(dist, rel), html)
        return run(src, dist)

    DIST = {
        "guides/ingress.html": built_page("here"),
        "cli/deploy.html": built_page("flags"),
        "guides/index.html": built_page("overview"),
        "index.html": built_page("hero"),
    }

    def test_relative_links_are_resolved_against_the_page_directory(self):
        code, out = self.check("[a](../cli/deploy#flags) [b](./ingress#here)\n", self.DIST)
        self.assertEqual(code, 0, out)
        self.assertIn("checked 2 fragment link(s); 0 dead", out)

    def test_relative_link_with_a_dead_fragment_is_caught(self):
        code, out = self.check("[a](../cli/deploy#gone)\n", self.DIST)
        self.assertEqual(code, 1, out)
        self.assertIn("missing", out)

    def test_md_suffixed_route_resolves_to_the_built_page(self):
        code, out = self.check("[a](/cli/deploy.md#flags) [b](../cli/deploy.md#gone)\n", self.DIST)
        self.assertEqual(code, 1, out)
        self.assertIn("checked 2 fragment link(s); 1 dead", out)

    def test_link_title_does_not_hide_the_link(self):
        page = (
            '[a](/cli/deploy#flags "Deploy")\n'
            "[b](/cli/deploy#flags 'Deploy')\n"
            '[c](/cli/deploy#gone "Deploy")\n'
        )
        code, out = self.check(page, self.DIST)
        self.assertEqual(code, 1, out)
        self.assertIn("checked 3 fragment link(s); 1 dead", out)

    def test_directory_and_root_routes_resolve_to_index_html(self):
        code, out = self.check("[a](/guides/#overview) [b](/#hero)\n", self.DIST)
        self.assertEqual(code, 0, out)
        self.assertIn("checked 2 fragment link(s); 0 dead", out)

    def test_unbuilt_route_is_counted_and_named_not_silently_dropped(self):
        code, out = self.check("[a](/cli/nope#x) [b](/cli/nope#y)\n", self.DIST)
        self.assertEqual(code, 0, out)  # still the build's job, not a failure here
        self.assertIn("checked 0 fragment link(s); 0 dead", out)
        self.assertIn("2 fragment link(s) NOT checked", out)
        self.assertIn("/cli/nope", out)


class CodeSpanTest(unittest.TestCase):
    """Documentation ABOUT a link is not a link."""

    def run_page(self, page: str) -> tuple[int, str]:
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        src, dist = os.path.join(root.name, "docs"), os.path.join(root.name, "dist")
        write(os.path.join(src, "guides/ingress.md"), page)
        write(os.path.join(dist, "guides/ingress.html"), built_page("here"))
        return run(src, dist)

    def test_fenced_block_is_not_audited(self):
        code, out = self.run_page("```md\n[a](/guides/ingress#gone)\n```\n[b](#here)\n")
        self.assertEqual(code, 0, out)
        self.assertIn("checked 1 fragment link(s); 0 dead", out)

    def test_tilde_fence_and_language_tag_are_not_audited(self):
        code, out = self.run_page("~~~markdown\n[a](/guides/ingress#gone)\n~~~\n")
        self.assertEqual(code, 0, out)
        self.assertIn("checked 0 fragment link(s)", out)

    def test_inline_code_span_is_not_audited(self):
        code, out = self.run_page("Write `[a](/guides/ingress#gone)` to link.\n[b](#here)\n")
        self.assertEqual(code, 0, out)
        self.assertIn("checked 1 fragment link(s); 0 dead", out)

    def test_longer_fence_survives_a_shorter_one_inside_it(self):
        """A ```` block quoting ``` must not be treated as closed at the inner fence."""
        page = "````md\n```\n[a](/guides/ingress#gone)\n```\n````\n[b](#here)\n"
        code, out = self.run_page(page)
        self.assertEqual(code, 0, out)
        self.assertIn("checked 1 fragment link(s); 0 dead", out)

    def test_unmatched_backtick_does_not_blank_the_rest_of_the_page(self):
        """Erring toward checking: a stray backtick must not hide later links."""
        code, out = self.run_page("A stray ` backtick.\n[a](/guides/ingress#gone)\n")
        self.assertEqual(code, 1, out)
        self.assertIn("missing", out)

    def test_a_real_link_after_a_fence_is_still_audited(self):
        code, out = self.run_page("```sh\necho hi\n```\n[a](/guides/ingress#gone)\n")
        self.assertEqual(code, 1, out)
        self.assertIn("missing", out)


class UsageTest(unittest.TestCase):
    def test_unbuilt_site_exits_2_not_1(self):
        """'You forgot to build' must not look like 'your links are dead'."""
        with tempfile.TemporaryDirectory() as root:
            src = os.path.join(root, "docs")
            write(os.path.join(src, "index.md"), "x")

            code, out = run(src, os.path.join(root, "absent"))

        self.assertEqual(code, 2, out)
        self.assertIn("docs:build", out)


if __name__ == "__main__":
    unittest.main()
