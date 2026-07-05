#!/usr/bin/env python3
"""Regression tests for translation_state.py.

The tool's whole value is that a translated page which has silently fallen behind
its source is indistinguishable from a current one — it renders fine, passes every
structural check, and tells the reader something no longer true. Its own failure
modes are therefore the interesting ones: any bug that makes it report "all
current" when it should not is invisible in exactly the same way.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import translation_state  # noqa: E402


class TranslationStateTest(unittest.TestCase):
    def setUp(self):
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        self.root = Path(root.name)

    def write(self, rel: str, text: str) -> Path:
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def run_tool(self, *argv: str) -> tuple[int, str, str]:
        """Invoke main() with argv, capturing exit code and both streams."""
        out, err = io.StringIO(), io.StringIO()
        old = sys.argv
        sys.argv = ["translation_state.py", *argv]
        try:
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                code = translation_state.main()
        finally:
            sys.argv = old
        return code, out.getvalue(), err.getvalue()

    def state(self) -> dict:
        path = self.root / translation_state.STATE_FILENAME
        return json.loads(path.read_text(encoding="utf-8")) if path.is_file() else {}

    def seed(self) -> None:
        """One English page with a ja and a zh translation, all recorded current."""
        self.write("cli/foo.md", "# Foo\n\nEnglish body.\n")
        self.write("ja/cli/foo.md", "# Foo\n\n日本語。\n")
        self.write("zh/cli/foo.md", "# Foo\n\n中文。\n")
        code, _, _ = self.run_tool("update", "--source-root", str(self.root))
        self.assertEqual(code, 0)

    # --- the four flows the audit named ------------------------------------

    def test_changed_source_is_stale(self):
        self.seed()
        self.write("cli/foo.md", "# Foo\n\nEnglish body, revised.\n")
        code, out, _ = self.run_tool("check", "--source-root", str(self.root))
        self.assertEqual(code, 1, "a changed source must fail, not merely mention it")
        self.assertIn("STALE: ja/cli/foo.md", out)
        self.assertIn("STALE: zh/cli/foo.md", out)

    def test_missing_state_entry_is_untracked(self):
        self.write("cli/foo.md", "# Foo\n")
        self.write("ja/cli/foo.md", "# Foo\n")
        code, out, _ = self.run_tool("check", "--source-root", str(self.root))
        self.assertEqual(code, 1)
        self.assertIn("UNTRACKED: ja/cli/foo.md", out)
        # UNTRACKED and STALE must not be conflated: one means "never reviewed",
        # the other "reviewed, then the source moved". They need different actions.
        self.assertNotIn("STALE:", out)

    def test_wrong_digest_is_stale(self):
        self.seed()
        state = self.state()
        state["ja"]["cli/foo.md"] = "0" * 64
        (self.root / translation_state.STATE_FILENAME).write_text(
            json.dumps(state), encoding="utf-8"
        )
        code, out, _ = self.run_tool("check", "--source-root", str(self.root))
        self.assertEqual(code, 1)
        self.assertIn("STALE: ja/cli/foo.md", out)
        self.assertNotIn("STALE: zh/cli/foo.md", out, "only the tampered locale is stale")

    def test_update_after_review_clears_it(self):
        self.seed()
        self.write("cli/foo.md", "# Foo\n\nRevised.\n")
        self.assertEqual(self.run_tool("check", "--source-root", str(self.root))[0], 1)
        code, _, _ = self.run_tool("update", "--source-root", str(self.root))
        self.assertEqual(code, 0)
        code, out, _ = self.run_tool("check", "--source-root", str(self.root))
        self.assertEqual(code, 0, "recording after review must clear the staleness")
        self.assertIn("all current", out)

    # --- behaviours that guard the tool's own honesty ----------------------

    def test_locale_prefixed_path_is_refused_not_silently_ignored(self):
        """`--path ja/cli/foo.md` must fail loudly.

        Paths are SOURCE-relative, so the locale-prefixed spelling — the file the
        translator actually edited, and the obvious thing to type — matches no
        source page. It used to record nothing, print "recorded 0 ...", and exit 0:
        the operator reads success, believes the page is marked reviewed, and
        `check` goes on reporting it stale. Reporting success for work not done is
        precisely the failure this tool exists to prevent.
        """
        self.seed()
        self.write("cli/foo.md", "# Foo\n\nRevised.\n")
        code, _, err = self.run_tool(
            "update", "--source-root", str(self.root), "--path", "ja/cli/foo.md"
        )
        self.assertEqual(code, 2, "a --path matching no source page must be an error")
        self.assertIn("ja/cli/foo.md", err)
        self.assertIn("not the", err)  # the message explains the source-vs-translation mix-up
        # And it must still be stale: nothing may have been recorded.
        self.assertEqual(self.run_tool("check", "--source-root", str(self.root))[0], 1)

    def test_unknown_path_records_nothing_at_all(self):
        """All-or-nothing: one bad path must not half-record a batch."""
        self.write("a.md", "A\n")
        self.write("b.md", "B\n")
        self.write("ja/a.md", "A\n")
        self.write("ja/b.md", "B\n")
        code, _, _ = self.run_tool(
            "update", "--source-root", str(self.root), "--path", "a.md", "--path", "gone.md"
        )
        self.assertEqual(code, 2)
        self.assertEqual(
            self.state(), {}, "a rejected batch must record nothing, not the valid half"
        )

    def test_missing_translation_is_not_this_tool_s_business(self):
        """An English page with no translation is skipped, not reported.

        A missing page is the structural audit's finding. Reporting it here too
        would produce two different tools blaming the same fact, and — worse — a
        `check` that can never go green until an unrelated gap is filled, which is
        how a gate stops being run.
        """
        self.write("cli/foo.md", "# Foo\n")  # no ja/ or zh/ copy
        code, out, _ = self.run_tool("check", "--source-root", str(self.root))
        self.assertEqual(code, 0, out)
        self.assertIn("all current", out)

    def test_porcelain_lists_only_stale_pages(self):
        self.seed()
        self.write("cli/foo.md", "# Foo\n\nRevised.\n")
        code, out, _ = self.run_tool(
            "check", "--source-root", str(self.root), "--porcelain"
        )
        self.assertEqual(code, 1)
        self.assertEqual(
            sorted(out.strip().split("\n")),
            ["ja\tcli/foo.md", "zh\tcli/foo.md"],
            "porcelain output is meant to be fed to another command; prose in it breaks that",
        )

    def test_state_file_is_stable_across_reruns(self):
        """A no-op update must produce no diff, or the file churns in every commit."""
        self.seed()
        first = (self.root / translation_state.STATE_FILENAME).read_text(encoding="utf-8")
        self.run_tool("update", "--source-root", str(self.root))
        second = (self.root / translation_state.STATE_FILENAME).read_text(encoding="utf-8")
        self.assertEqual(first, second)
        self.assertTrue(second.endswith("\n"), "the state file must be newline-terminated")

    def test_missing_source_root_is_an_error(self):
        code, _, err = self.run_tool("check", "--source-root", str(self.root / "nope"))
        self.assertEqual(code, 2)
        self.assertIn("source root", err)


if __name__ == "__main__":
    unittest.main()
