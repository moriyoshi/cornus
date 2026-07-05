#!/usr/bin/env python3
"""Regression tests for check_duplicate_targets.py."""

from __future__ import annotations

import contextlib
import io
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import check_duplicate_targets  # noqa: E402


def write(path: str, text: str) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def run(src: str) -> tuple[int, str]:
    argv = sys.argv
    sys.argv = ["check_duplicate_targets.py", "--src", src]
    out = io.StringIO()
    try:
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            code = check_duplicate_targets.main()
    finally:
        sys.argv = argv
    return code, out.getvalue()


class DuplicateTargetTest(unittest.TestCase):
    def check(self, markdown: str) -> tuple[int, str]:
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        write(os.path.join(root.name, "architecture/security.md"), markdown)
        return run(root.name)

    def test_duplicate_in_one_list_fails(self):
        code, out = self.check(
            "- [Networking](./networking.md)\n"
            "- [Remote networking](/architecture/networking)\n"
        )
        self.assertEqual(code, 1, out)
        self.assertIn("duplicate target /architecture/networking", out)
        self.assertIn("first used on line 1", out)

    def test_repetition_within_one_list_item_is_allowed(self):
        code, out = self.check(
            "- [Health](/cli/version-health) and [version](/cli/version-health)\n"
        )
        self.assertEqual(code, 0, out)

    def test_distinct_fragments_are_distinct_targets(self):
        code, out = self.check(
            "- [Ingress](/guides/networking#ingress)\n"
            "- [Egress](/guides/networking#egress)\n"
        )
        self.assertEqual(code, 0, out)

    def test_repetition_in_separate_lists_is_allowed(self):
        code, out = self.check(
            "- [Networking](/architecture/networking)\n"
            "\nA paragraph separates the lists.\n\n"
            "- [Networking](/architecture/networking)\n"
        )
        self.assertEqual(code, 0, out)

    def test_code_examples_are_ignored(self):
        code, out = self.check(
            "```md\n"
            "- [Networking](/architecture/networking)\n"
            "- [Networking](/architecture/networking)\n"
            "```\n"
        )
        self.assertEqual(code, 0, out)

    def test_external_destinations_are_checked(self):
        code, out = self.check(
            "- [Project](https://example.com/project)\n"
            "- [Source](https://example.com/project)\n"
        )
        self.assertEqual(code, 1, out)
        self.assertIn("https://example.com/project", out)


if __name__ == "__main__":
    unittest.main()
