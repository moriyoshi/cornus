#!/usr/bin/env python3
"""Regression tests for check_punctuation.py."""

from __future__ import annotations

import contextlib
import io
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import check_punctuation  # noqa: E402


class PunctuationTest(unittest.TestCase):
    def write(self, text: str) -> str:
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        path = os.path.join(root.name, "page.md")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(text)
        return path

    def test_prose_is_rejected(self):
        path = self.write("说明：值（默认）\n")
        self.assertEqual(
            check_punctuation.punctuation_violations(path),
            [(1, 3, "："), (1, 5, "（"), (1, 8, "）")],
        )

    def test_fenced_and_inline_code_are_ignored(self):
        path = self.write("`值：默认`\n\n```sh\n# 参数：值（默认）\n```\n")
        self.assertEqual(check_punctuation.punctuation_violations(path), [])

    def test_link_destinations_and_bare_urls_are_ignored(self):
        path = self.write(
            "[链接](/zh/指南#值：默认)\n"
            "https://example.invalid/值：默认\n"
        )
        self.assertEqual(check_punctuation.punctuation_violations(path), [])

    def test_link_text_is_still_checked(self):
        path = self.write("[说明：值](/zh/guide)\n")
        self.assertEqual(
            check_punctuation.punctuation_violations(path),
            [(1, 4, "：")],
        )

    def test_main_reports_failures(self):
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        path = os.path.join(root.name, "page.md")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("值：默认\n")

        argv = sys.argv
        output = io.StringIO()
        sys.argv = ["check_punctuation.py", "--src", root.name]
        try:
            with contextlib.redirect_stdout(output):
                code = check_punctuation.main()
        finally:
            sys.argv = argv

        self.assertEqual(code, 1)
        self.assertIn("1 punctuation violation(s)", output.getvalue())


if __name__ == "__main__":
    unittest.main()
