#!/usr/bin/env python3
"""Regression tests for audit_markdown_translation.py's comparison logic.

These exercise the comparison FUNCTIONS directly rather than the audit's exit
code, and that is the point. Inline-code and link differences are advisory
WARNINGS: the audit still exits 0 when they fire. So a regression in either
comparison — one that stops folding line breaks, or starts comparing link order —
changes only the wording of an advisory nobody's CI reads. Running the tool proves
nothing about them; calling the functions does.

Both comparisons are deliberately ORDER-INSENSITIVE and MULTIPLICITY-SENSITIVE
(Counter subtraction, not set difference). A translation may reorder the sentences
that carry links and code spans — that is what translating is — but dropping one of
two identical links means a cross-reference vanished.
"""

from __future__ import annotations

import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import audit_markdown_translation as audit  # noqa: E402


class InlineCodeTest(unittest.TestCase):
    """The folding rule: same span, different line wrapping, no complaint."""

    def test_line_breaks_inside_a_span_are_folded(self):
        source = "Run `cornus config set-context --namespace <ns>` first.\n"
        target = "先に `cornus config set-context\n--namespace <ns>` を実行します。\n"
        self.assertEqual(
            audit.inline_code_spans(source),
            audit.inline_code_spans(target),
            "the same command wrapped at a different column must compare equal; "
            "otherwise every long span in every translated page reports a false difference",
        )

    def test_interior_whitespace_runs_are_folded(self):
        self.assertEqual(
            audit.inline_code_spans("`a   b`"), audit.inline_code_spans("`a b`")
        )

    def test_a_span_containing_a_blank_line_is_not_a_span(self):
        # A blank line ends the paragraph, so the backticks are not a pair.
        self.assertEqual(audit.inline_code_spans("`open\n\nclose`"), [])

    def test_multiplicity_is_preserved(self):
        spans = audit.inline_code_spans("`x` and `x` again")
        self.assertEqual(spans, ["x", "x"], "two occurrences must not collapse to one")


class LinkComparisonTest(unittest.TestCase):
    def setUp(self):
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        self.root = Path(root.name)

    def compare(self, source: str, target: str, prefix: str = "/ja"):
        s = self.root / "source.md"
        t = self.root / "target.md"
        s.write_text(source, encoding="utf-8")
        t.write_text(target, encoding="utf-8")
        return audit.compare_file(s, t, prefix)

    def test_reordered_links_are_not_a_difference(self):
        source = "See [a](/cli/a) and [b](/cli/b).\n"
        target = "[b](/ja/cli/b) と [a](/ja/cli/a) を参照。\n"
        errors, warnings = self.compare(source, target)
        self.assertEqual(errors, [])
        self.assertEqual(
            warnings, [], "a translation may reorder sentences; link ORDER is not a contract"
        )

    def test_wrong_path_is_reported(self):
        source = "See [a](/cli/a).\n"
        target = "[a](/ja/cli/typo) を参照。\n"
        _, warnings = self.compare(source, target)
        self.assertTrue(
            any("link/image destinations differ" in w for w in warnings),
            f"a link pointing somewhere else must be reported, got {warnings}",
        )

    def test_localized_fragment_is_not_a_difference(self):
        """The #fragment is excluded from the comparison, and must stay excluded.

        A link into a translated page carries a TRANSLATED heading id, which can
        never equal the source's fragment — expected_link localizes the path and has
        no way to localize the fragment. Comparing them made every correct
        deep link look broken, which is the noise that hid real findings.
        """
        source = "See [mounts](/reference/deploy-spec#mounts).\n"
        target = "[マウント](/ja/reference/deploy-spec#マウント) を参照。\n"
        errors, warnings = self.compare(source, target)
        self.assertEqual(errors, [])
        self.assertEqual(warnings, [], f"a localized fragment must not be a difference: {warnings}")

    def test_duplicate_multiplicity_is_counted(self):
        source = "[a](/cli/a) then [a](/cli/a) again.\n"
        target = "[a](/ja/cli/a) だけ。\n"
        _, warnings = self.compare(source, target)
        self.assertTrue(
            any("link/image destinations differ" in w for w in warnings),
            "dropping one of two identical links means a cross-reference vanished; "
            "set-difference logic would miss it",
        )

    def test_same_page_anchors_are_counted_not_named(self):
        """A same-page anchor has no path, so it collapses to a placeholder.

        Keyed on the empty string it would report as `missing ` with nothing
        actionable in it. All that can be asserted is that the count survives — and
        that much is worth asserting, since a dropped one is a lost cross-reference.
        """
        self.assertEqual(
            audit.link_key((False, "#some-heading")), (False, "#(same-page anchor)")
        )
        source = "[one](#a) and [two](#b)\n"
        target = "[one](#あ)\n"  # one of the two anchors dropped
        _, warnings = self.compare(source, target)
        self.assertTrue(
            any("link/image destinations differ" in w for w in warnings),
            "a dropped same-page anchor must still be caught by count",
        )

    def test_links_inside_fenced_code_are_ignored(self):
        source = "```sh\ncurl https://example.com/[a](/cli/a)\n```\n\nSee [a](/cli/a).\n"
        target = "```sh\ncurl https://example.com/[a](/cli/a)\n```\n\n[a](/ja/cli/a) を参照。\n"
        errors, warnings = self.compare(source, target)
        self.assertEqual(errors, [])
        self.assertEqual(warnings, [], "sample commands are not documentation links")

    def test_unprefixed_site_absolute_link_is_an_ERROR_not_a_warning(self):
        """This one changes the exit code, unlike the advisory comparisons above.

        A site-absolute link with no locale prefix sends a reader of the Japanese
        docs to the English page, silently. That is a defect in the translation
        itself rather than a difference worth a look, so it is an error.
        """
        source = "See [a](/cli/a).\n"
        target = "[a](/cli/a) を参照。\n"  # missing the /ja prefix
        errors, _ = self.compare(source, target)
        self.assertTrue(
            any("lack locale prefix" in e for e in errors),
            f"an unprefixed doc link must be an ERROR, got {errors}",
        )

    def test_external_and_asset_links_are_left_alone(self):
        source = "[site](https://example.com) ![img](/logo.png)\n"
        target = "[site](https://example.com) ![img](/logo.png)\n"
        errors, warnings = self.compare(source, target)
        self.assertEqual((errors, warnings), ([], []),
                         "external URLs and image assets are not localized")


if __name__ == "__main__":
    unittest.main()
