---
name: assess-doc-quality
description: "Assess the quality of user-facing documentation — the VitePress site under docs/ (en, ja, zh), README.md, and ARCHITECTURE.md — across mechanical correctness (build, dead routes, dead link fragments, locale prefixes, punctuation rules), factual accuracy against the code, and editorial quality (structure, completeness, task orientation, freshness), then report findings by severity. Use when asked to review, audit, or grade the docs, to check whether a documentation change is publishable, or to find what is stale, wrong, missing, or unreachable in the docs."
user-invocable: true
allowed-tools: Bash, Read, Write, Edit, Glob, Grep, Agent
---

# Assess documentation quality

Produce an evidence-backed assessment of Cornus's user-facing documentation. Every
finding must name a file, a line or heading, and what is wrong — never a vague
impression. Report; do not silently rewrite unless the user asked for fixes too.

**Use this skill when:** you are asked to review/audit/grade the docs, to judge
whether a docs change is ready to publish, or to hunt for stale, wrong, missing,
or unreachable documentation.

Assessment only reads. If the user also wants the problems fixed, fix them after
reporting, and re-run the mechanical checks afterwards.

## Step 0 — Scope and baseline

1. Settle the scope: the whole site, one locale tree, one section, or just the
   pages touched by a change (`git status --short`, `git diff --name-only`).
2. Read the governing docs before judging anything: `CLAUDE.md`,
   `.agents/docs/QUALITY_GATE.md` section 5, `docs/README.md` (the site's
   intended layout), and for translated pages
   `.agents/docs/JA_TRANSLATION_GLOSSARY.md` / `.agents/docs/ZH_TRANSLATION_GLOSSARY.md`.
3. Preserve unrelated in-flight edits in the worktree; another agent may be
   working in the same directory.

The site is trilingual: `docs/` is the English source of truth, `docs/ja/` and
`docs/zh/` are translations. Never treat a translation as a source.

## Step 1 — Mechanical checks (run these first; they are cheap and objective)

Run from the repository root unless noted. The gate needs Node on `PATH`;
it may not be there in a non-interactive shell, so locate the installed
toolchain and prepend it for the command at hand.

```sh
cd docs
npm run docs:check
```

- **Build** — `ignoreDeadLinks: false`, so a failing build means a dead internal
  route or malformed Markdown. A green build is necessary, not sufficient.
- **Anchors** — the build validates routes but never the `#fragment`.
  `docs:check-anchors` wraps `scripts/check_anchors.py` in this skill and
  resolves every site-absolute fragment link against the ids VitePress actually
  emitted. Two failure reasons:
  - `normalization` — the id exists in a different Unicode form. markdown-it-anchor
    slugifies AFTER Unicode decomposition, so a Japanese heading yields an id with
    separated combining marks while a hand-typed fragment is composed (NFC). They
    are visually identical everywhere a human looks and dead in every browser.
    **Copy the id the script prints; do not retype it.**
  - `missing` — no such heading on the target page: a genuine broken link, or a
    section that page has not been given yet (common in the locale trees).

  Do NOT "fix" the normalization class by overriding `markdown.anchor.slugify`.
  The conventional recipe strips combining marks and mangles every Japanese id
  (measured: 25 dead anchors became 64).

  What the count covers: site-absolute (`/a/b#f`), relative (`./b#f`,
  `../a/b#f`) and fragment-only (`#f`) links, with or without a `.md`/`.html`
  route suffix and with or without a link title; routes resolve to `<route>.html`
  or `<route>/index.html`. External URLs, and anything inside a code fence or an
  inline code span, are out of scope by design. Fragments whose target page is
  not in the build are reported separately as NOT checked — that is a dead route,
  which the build already fails on. The walk prunes `node_modules`, `dist`, and
  `.vitepress`, so vendored markdown is never judged against this site's routes.

  If you change the checker, its regression tests are stdlib-only and run in
  milliseconds:

  ```sh
  python3 -m unittest discover -s .agents/skills/assess-doc-quality/scripts
  ```

- **Duplicate list targets** — `docs:check-duplicate-targets` canonicalizes
  in-site destinations and rejects repeated targets within one Markdown list.
  This catches navigation and "Related pages" entries that collapse onto the
  same destination during a restructure without flagging intentional repetition
  elsewhere in a page. Distinct fragments on one page remain distinct targets.
  VitePress's build does not detect this class of defect.

- **Repository punctuation** — `docs:check-punctuation` rejects full-width
  parentheses and colons in authored Markdown prose. It skips fenced and inline
  code plus URL destinations so literal interfaces are not rewritten or
  misreported.

- **Translation structure and locale prefixes** — for any locale work, run the
  audit from the `translate-documents` skill; it checks front matter keys,
  heading levels, code fences, inline code, and link destinations, and flags
  links that send a translated reader back to English:

  ```sh
  python3 .agents/skills/translate-documents/scripts/audit_markdown_translation.py \
    docs docs/ja --locale-prefix /ja --exclude ja --exclude zh --exclude README.md
  ```

- **Repo punctuation rules** (repo-authored docs only, not
  `skills/**/references/**`): no full-width parentheses, no full-width colon.

  ```sh
  rg -n '[（）：]' --glob '*.md' docs .agents/docs README.md ARCHITECTURE.md
  ```

- **Mixed-language review queue** for `docs/ja` (a queue, not a verdict —
  exclude code and commands before judging):

  ```sh
  rg -n --glob '*.md' '[ぁ-んァ-ン一-龠][[:space:]]+[A-Za-z]' docs/ja
  rg -n --glob '*.md' '^(イメージ|レイアウト|ヒーロー|ソース|リンク):' docs/ja
  ```

- **Reachability** — every page should be reachable from the nav tree in
  `docs/.vitepress/config.mts`. Compare the `TREE` routes against the files on
  disk in both directions: a page absent from `TREE` is orphaned (reachable only
  by search), and a `TREE` route with no file breaks the build.

- **Locale parity** — file-level gaps between the trees:

  ```sh
  diff <(cd docs && find . -name '*.md' -not -path './node_modules/*' -not -path './ja/*' -not -path './zh/*' -not -name README.md | sort) \
       <(cd docs/ja && find . -name '*.md' | sort)
  ```

## Step 2 — Accuracy against the code

Documentation drifts from the binary silently; the build cannot catch it. For
every documented interface in scope, verify it still exists and still means what
the page says. Sample generously; verify exhaustively for pages the user named.

- **CLI commands and flags** — the CLI is kong, defined in `cmd/cornus/`. Check
  each documented command, flag, and default in `docs/cli/*` against the struct
  tags. Flags that were renamed or removed are the most common stale finding.
- **Environment variables** — `docs/reference/server-env-vars.md` against the
  actual lookups (`grep -rn 'CORNUS_[A-Z_]*' --include='*.go'`).
- **Config and spec keys** — `docs/reference/deploy-spec.md`,
  `connection-config.md`, `helm-values.md` against the Go structs and the Helm
  chart values.
- **Behavioral claims** — defaults, ports, precedence rules, "X implies Y"
  statements. Trace each to the code path that implements it.
- **Examples** — commands, YAML, and JSON must be runnable/valid as written:
  right subcommand, real flags, coherent field names. Do not execute anything
  with side effects to check this.

Report each drift as: documented claim → what the code does → file:line of both.

## Step 3 — Editorial quality

Judge each page in scope against the layout contract in `docs/README.md` (guides
lead with **How it works**, then recipes; `cli/` is one page per command group;
`reference/` is lookup material). Assess:

- **Audience fit** — does the page state who it is for and what it assumes?
- **Task orientation** — can a reader accomplish the task end to end without
  leaving the page for undocumented knowledge?
- **Completeness** — documented feature surface vs. what actually ships. Look
  for shipped commands, flags, or subsystems with no page at all.
- **Structure** — heading hierarchy that matches the reading order, no orphan
  H3s, no section that is a single sentence stub.
- **Accuracy of cross-references** — links point at the section that actually
  answers the question they promise.
- **Freshness** — pages describing behavior that changed recently. Cross-check
  `.agents/docs/JOURNAL.md` and `.agents/docs/TODO.md` for known documentation
  debt before claiming something is undocumented; it may already be tracked.
- **Prose** — for translations, natural target-language phrasing, no
  word-by-word substitution artifacts, consistent glossary terms. For English,
  concrete and unhedged.

Do not grade style by personal taste. A finding is only a finding if a reader
would be misled, blocked, or unable to find something.

## Step 4 — Report

Write the assessment in the reply, and — when the user wants it recorded — into
`.agents/docs/` (never `/tmp`). Structure it as:

1. **Verdict** — publishable / needs work / broken, in one line, with the
   mechanical check results (build, anchors, audit) stated plainly. If a check
   was not run, say so; never imply coverage you do not have.
2. **Blocking** — dead links and fragments, wrong commands or flags, claims the
   code contradicts, missing pages for shipped features.
3. **Should fix** — stale details, structural problems, locale-prefix leaks,
   translation-fidelity gaps, punctuation-rule violations.
4. **Nits** — wording and consistency.

Each item: `file:line` (or heading), what is wrong, and the concrete fix. For
anchor normalization findings, paste the exact id the checker printed.

For a large scope, fan the per-page reads out over parallel agents and keep the
mechanical checks in one place — but every reported finding must still carry its
own file/line evidence.

## Step 5 — If asked to fix

Apply fixes smallest-blast-radius first, then re-run Step 1 in full (build,
anchors, and the translation audit if any locale page changed). Append durable
findings to the end of `.agents/docs/JOURNAL.md` — never edit existing sections —
and update `.agents/docs/TODO.md` for anything left open.
