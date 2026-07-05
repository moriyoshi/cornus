---
name: translate-documents
description: Translate, localize, review, or synchronize Markdown documentation while preserving source meaning, document structure, code, configuration, links, and project terminology. Use for creating or updating locale trees such as docs/ja or docs/zh, auditing an existing translation against its source, fixing mixed-language or unnatural translated prose, or propagating source-document changes into translations.
---

# Translate Documents

Produce faithful, natural translations without changing the documentation's technical contract. Treat the source document as the authority and validate both language quality and Markdown structure.

## Establish the translation contract

1. Read the repository instructions that govern the source and target files.
2. Identify the source locale, target locale, source root, target root, and exact pages in scope.
3. Find the matching source page for every translated page. Do not translate an already translated locale as a proxy for the source.
4. Read any project glossary or style guide before editing. In Cornus, read `.agents/docs/QUALITY_GATE.md`; for Japanese, also read `.agents/docs/JA_TRANSLATION_GLOSSARY.md`.
5. Inspect the worktree and preserve unrelated or pre-existing edits.

If the requested source or target locale cannot be determined from paths, configuration, or context, ask before editing.

## Translate from the source

- Translate each page section by section, using the model's own language ability. Do not call an external translation service, send document text to a third party, or use scripted machine translation unless the user explicitly authorizes that service.
- Preserve every source fact, qualification, warning, example, and ordering relationship. Do not add translator notes, glossary explanations, first-use parenthetical English, or new product claims.
- Translate complete sentences, predicates, and compound technical phrases in context. Do not perform word-by-word or frequency-based replacement.
- Prefer natural target-language prose over source-language syntax. Re-read the result without looking at the source, then compare it with the source again for fidelity.
- Translate reader-facing headings, table labels, captions, link text, and explanatory code comments when appropriate.
- Keep terminology consistent across pages. Add a newly settled Japanese term to `.agents/docs/JA_TRANSLATION_GLOSSARY.md` in the same change; keep translation aids internal rather than publishing them under `docs/`.

## Preserve literal and structured content

Keep these verbatim unless the source itself changes them:

- product and standard names;
- commands, flags, environment variables, paths, URLs, API routes, identifiers, type names, and configuration values;
- inline code and executable portions of code blocks;
- Markdown fence markers and language identifiers;
- YAML, JSON, front matter, and configuration keys.

Translate only reader-facing values inside structured content. Never translate a key because its English spelling also appears in prose. For VitePress front matter, keys such as `layout`, `hero`, `image`, `src`, `actions`, `theme`, `link`, and `linkText` are interfaces; changing one may silently remove content without failing the build.

Preserve Markdown hierarchy and intent: front matter, heading levels, lists, tables, admonitions, code fences, and link/image destinations. Do not reformat untouched code or configuration as a side effect of translation.

## Localize links deliberately

For a translated VitePress page:

- Prefix site-absolute links to translated documentation routes with the target locale, for example `/cli/build` to `/ja/cli/build`.
- Leave relative links, fragment-only links, external URLs, and asset URLs unchanged unless the locale layout specifically requires otherwise.
- Translate link text when it is reader-facing.

A site build cannot detect a live link that accidentally sends the reader back to the source locale, so audit locale prefixes separately.

## Work in reviewable batches

For a large locale tree, translate a coherent small batch at a time. After each batch:

1. Compare every target section with its source section.
2. Check headings, tables, lists, code, literals, and links.
3. Run the structural audit on the batch.
4. Fix detected issues before starting the next batch.

Do not claim a whole locale is complete until file parity and all requested pages have been checked.

## Run the structural audit

Use the bundled script from the repository root. With no `--path`, it checks every Markdown file and requires source/target tree parity:

```sh
python3 .agents/skills/translate-documents/scripts/audit_markdown_translation.py \
  docs docs/ja --locale-prefix /ja \
  --exclude ja --exclude zh --exclude README.md
```

For a partial batch, repeat `--path` with paths relative to both roots:

```sh
python3 .agents/skills/translate-documents/scripts/audit_markdown_translation.py \
  docs docs/ja --locale-prefix /ja \
  --path guides/registry.md --path introduction/quick-start.md
```

The audit checks front matter key structure, heading levels, fenced-block
signatures, inline code, and link destinations. Structural violations and
wrong-locale links fail the command; inline-code and link-sequence differences
are review warnings unless `--strict` is passed. It is not a
translation-quality oracle. Review every warning against the source; do not
mechanically alter prose merely to silence a heuristic.

Use `--exclude` for locale or contributor-only subtrees that live beneath the
source root but are not source-language pages.

## Complete the quality gate

1. Manually scan target prose for source-language fragments after excluding literal interfaces and code. Treat results as a review queue, not replacement candidates.
2. Verify every changed page against its source for omissions, additions, mistranslations, and unnatural target-language syntax.
3. Run the repository's documentation build and any localization checks required by its instructions. For Cornus:

   ```sh
   cd docs
   PATH="$HOME/.local/share/mise/shims:$PATH" npm run docs:build
   ```

4. Run `git diff --check` and inspect the final diff.
5. Append durable translation-review findings to `.agents/docs/JOURNAL.md` when they will help future work. Never rewrite existing journal sections, and do not record routine progress as durable knowledge.

Report which pages and locale were handled, which checks passed, and any remaining source-language candidates or unverified build step.
