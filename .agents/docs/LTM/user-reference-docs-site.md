# User Reference Documentation Site

## Summary

`docs/` is Cornus's multilingual VitePress user-reference site and a canonical documentation surface
beside `README.md`, `ARCHITECTURE.md`, and agent-maintained project knowledge. One instance serves
English at the root, Japanese under `/ja/`, and Simplified Chinese under `/zh/`. It holds
task-oriented guidance while reference pages remain authoritative for flags, environment variables,
schemas, and Helm values.

## Key Facts

- `docs/.vitepress/config.mts` derives locale-prefixed navigation and sidebars from one trilingual
  route tree; local search indexes each locale separately.
- The taxonomy is Introduction, Guides, Cookbook, CLI reference, Reference, and Architecture. Page directory, sidebar label, and URL must remain aligned after moves.
- `.github/workflows/docs.yml` uses Node 20 and `npm ci` to deploy to GitHub Pages. Pages must be enabled once with GitHub Actions as the source.
- `npm run docs:build` is the documentation gate because it rejects dead relative links and broken anchors.
- Public translations must be source-faithful. Literal interfaces and structured keys stay exact;
  internal glossary/audit material stays under `.agents/docs/`.

## Details

`README.md` is intentionally a slim landing page that links to the full site. Pages outside the VitePress source directory, including root `ARCHITECTURE.md`, must use a GitHub blob URL rather than `../ARCHITECTURE.md`. Reference pages are the canonical home for configuration detail: privilege posture lives in `reference/deploy-backends.md`, persistence in `reference/storage-backends.md`, and Helm values in `reference/helm-values.md`.

Operation instructions were folded into `introduction/installation.md`; observability has a dedicated Guide; and editor-specific devcontainer material was folded into the Remote dev environment cookbook variation. Bulk link rewrites can miss `config.mts`, so every page move requires a rebuild after file, sidebar, and inbound-link updates.

### Source-verified material

The observability guide covers `CORNUS_OTEL`, standard `OTEL_*`, Prometheus `/metrics` through `CORNUS_METRICS_PROMETHEUS`, logging, and health probes; `OTEL_SDK_DISABLED` wins. Tunnel docs cover ngrok, SSH, Cloudflare, and Tailscale. `cornus daemon docker` gets its conduit from a connection profile rather than a `--conduit` flag and does not implement Docker `/build`, so remote devcontainer flows use prebuilt images.

Credential pages distinguish loopback `AWS_CONTAINER_CREDENTIALS_FULL_URI` from the `wellKnown` EC2 IMDSv2 binding at `169.254.169.254`, and document OAuth forwarding and refresh for supported Anthropic-compatible credentials.

### Reader-facing architecture section

The root `ARCHITECTURE.md` remains canonical. The user site adapts it into eight pages under
`docs/architecture/`: overview, server and registry, build engine, deploy engine, networking,
caretaker, clients, and security. Contributor-only package/testing/release detail links back to the
repository instead of being duplicated.

Moving `docs/architecture.md` to `docs/architecture/index.md` changes its route to
`/architecture/`. VitePress only maps directory index links when the URL ends in `/`; body links,
frontmatter cards, nav, and sidebar keys must all use the trailing slash. A path-keyed sidebar also
needs an explicit `'/architecture/'` entry. Mermaid diagrams use `vitepress-plugin-mermaid`; SSR
emits a hydration container, so verify payloads in built JS rather than expecting rendered SVG in
the initial HTML.

An inline code span containing `<tag>`-like text must not break so that the token begins a source
line. With `html: true`, markdown-it can parse it as an HTML block and Vue reports a misleading
missing-end-tag error. Render the page with VitePress's markdown renderer and inspect the generated
HTML near the reported line.

### Tunnels/hub page split

`docs/topics/tunnels-and-hub.md` combined two materially different features under one page
("two ways to reach a workload") and eventually needed to split: `git mv`'d to
`docs/topics/hub.md` (rewritten hub-only — the old framing intro and the "Public tunnels"
section dropped, H3 subsections promoted to H2), with the tunnels material extracted into a
new standalone `docs/topics/tunnels.md`. The tunnels page cross-references Ingress rather than
the hub, since a tunnel-facing page citing the workload-to-workload hub reads as a
non-sequitur to a reader who only cares about exposing one service publicly. A new
`docs/guides/tunnels.md` holds step-by-step per-backend setup recipes (see
[[public-tunnels]]); the Cloudflare and Tailscale sections each need a "the published
`ghcr.io/moriyoshi/cornus:latest` image doesn't bundle `cloudflared`/`tailscale` — build a
custom image" Dockerfile snippet, confirmed by inspecting the repo's `Dockerfile` (only
`runc`/`ca-certificates`/`uidmap` are installed).

Splitting one combined page into two-plus-a-guide touches far more inbound links than the new
pages themselves: roughly 15 other pages (`cli/hub.md`, `cli/tunnel.md`,
`architecture/networking.md`, `reference/deploy-spec.md`, `reference/deploy-backends.md`,
`reference/server-env-vars.md`, `reference/helm-values.md`, `cookbook/*`, `topics/ingress.md`,
`introduction/quick-start.md`, `guides/networking.md`, `guides/index.md`, sidebar config) had
to be repointed to whichever of `/topics/tunnels` or `/topics/hub` fit their context — a
mechanical but easy-to-miss step of any page split. One bug of this kind was self-referential:
`architecture/networking.md`'s own "## Public tunnels" section linked out with text that just
echoed its own heading ("See [public tunnels](/topics/tunnels)"); reworded to name what each
target actually offers ("See the [backends table](/topics/tunnels) for what each needs and the
[Tunnels guide](/guides/tunnels) for step-by-step setup instructions").

**Process lesson for multi-round doc work:** dispatch locale-sync (en/ja/zh) fork agents only
once the English source has actually stabilized. This split needed two locale-sync passes
because the English source kept changing after the first sync round completed (a CLI
credential-safety rename and a Helm-sidecar Tailscale split both landed afterward) — expect a
second pass whenever a doc restructuring and an in-flight feature land in the same session. One
doc regression from the first pass was caught by the zh sync agent itself: splitting the
Tailscale section into "Kubernetes via Helm" vs "anywhere else" accidentally dropped the
custom-image Dockerfile guidance for a plain-container (non-Kubernetes, non-bare-host)
deployment — restored in en/ja/zh once caught.

This entry covers the docs-site file-move/sidebar/locale-sync mechanics only. For the underlying
product content of these pages (the `cornus tunnel` CLI/env-var changes and the Helm Tailscale
sidecar), see [[public-tunnels]]. For the hub network overlay's own technical content, see
[[hub-network-overlay]] (unchanged by this restructuring beyond the page rename/URL).

### Topics folded into Guides (2026-07-24)

The Topics section was retired: all seven pages moved into `docs/guides/`, so the site is now
one page per feature rather than a concept page and a recipe page per feature. The trigger was
that the "recipe vs. concept" boundary was not being maintained and had inverted between pairs
(`guides/tunnels.md` 12 KB vs `topics/tunnels.md` 2.5 KB, while `topics/ingress.md` 12 KB vs
`guides/ingress.md` 5 KB) — content landed in whichever page the last editor happened to open.
Readers also saw both entries ("Tunnels" and "Public tunnels") in the same sidebar.

The map: tunnels/ingress/egress/credentials merged into their `guides/` namesakes;
`topics/auth-and-tls.md` into `guides/security.md` (retitled "Security and authentication");
`topics/hub.md` plus the two hub sections lifted out of `guides/networking.md` became a new
`guides/hub.md`; and `guides/networking.md` was retitled "Networking and conduits". Each merged
page is now `# Title` -> intro -> `## How it works` (H3s from the topic) -> the existing recipe
H2s -> See also.

`topics/remote-workflows.md` was a **redistribution, not a merge** — its sections genuinely
belonged in four different guides (endpoint/profiles/auto-port-forward/via-server/kube-auth to
`guides/remote-clusters.md`, session conduits to `guides/networking.md`, remote builds and lazy
contexts to `guides/building-images.md`, client-local bind mounts to
`guides/deploying-workloads.md`). Its ~46 inbound links cited it for those four different
reasons, so a blanket sed would have been wrong; each referrer had to be classified by why it
linked.

Two traps worth remembering for any merge of this shape:

- **Self-links.** ~15 inbound links lived in pages that were themselves merge targets
  (`guides/networking.md`, `guides/remote-clusters.md`, `guides/deploying-workloads.md`,
  `guides/building-images.md` all had `**See also:** ... [remote workflows]` lines). After the
  merge those point at the page they are on; they must be **deleted**, not rewritten. A second
  variant appeared after the sed pass: `architecture/security.md` and `architecture/networking.md`
  each listed two differently-named links that both collapsed onto the same merged target, so
  their "Related pages" lists had visible duplicates.
- **Retired public URLs.** `https://cornus.dev/topics/*` was live and GitHub Pages has no
  server-side redirects. Static meta-refresh stubs under `docs/public/topics/*.html` preserve
  them; `public/` (rather than stub markdown pages) keeps them out of the sidebar, the local
  search index, and the dead-link check. **Ordering matters:** do not add
  `public/<locale>/topics/x.html` while `docs/<locale>/topics/x.md` still exists — both write
  the same `dist` path. Add each locale's stubs only after deleting that locale's markdown.

The sidebar was scoped in the same pass. `sidebarMap()` had mapped every section prefix to the
identical fully-expanded tree, so all ~60 links from every section rendered on every page.
`sidebarFor()` now takes an optional active-section argument and sets
`collapsed: SECTIONS[i] !== active`; a new module-level `SECTIONS` array must stay index-aligned
with `TREE`. Removing a `TREE` section also requires renumbering the `NAV` section indices,
which are positional.

### VitePress localization

Locale directories and `locales: { root, ja, zh }` provide one-site i18n. Shared theme settings
(logo, search, social links, footer) remain top-level; each locale supplies translated nav/sidebar
labels and UI strings. Top-level theme config deep-merges into each locale.

VitePress does not rewrite cross-locale links. A site-root absolute `/cli/build` inside Japanese
content silently leads to English and still passes dead-link checking. Translated pages must prefix
internal absolute links with `/ja/` or `/zh/`; relative, anchor, and external links remain unchanged.

Translate frontmatter values, never keys. Keys such as `layout`, `hero`, `image`, `src`, `actions`,
`theme`, `link`, and `linkText` are structured configuration. Translating `image:` silently removes
the home-page logo without a build error. The reusable `translate-documents` skill and
`audit_markdown_translation.py` compare source/target trees for missing or empty pages, changed
frontmatter keys, heading/fence structure, and unprefixed locale links. Inline-code/link-sequence
differences remain human-review warnings unless `--strict` is used.

### Translation quality

Translation is phrase-level and source-checked, never mechanical token replacement. Commands,
flags, environment variables, paths, URLs, code, YAML keys, type names, and product/standard names
are literal interfaces. Public pages must not add translator notes, glossaries, first-use English,
or explanatory material absent from the source. Preferred Japanese terminology lives in
`.agents/docs/JA_TRANSLATION_GLOSSARY.md`.

Review queues should extract prose containing Latin words only after excluding code fences and
inline code, then compare every candidate with the English source. Hyphenated compounds,
transport/session language, and operational modifiers require contextual translation. Japanese and
Chinese home pages demonstrated the recurring defects: inline English residue, calqued verbs,
mixed register, and prohibited full-width punctuation. The same source-checked spot review remains
appropriate for other translated pages.

Established Japanese terminology includes:

| English | Preferred Japanese |
|---------|--------------------|
| observability | オブザーバビリティ |
| credential(s) / authentication | 資格情報 / 認証 |
| credential brokering | 資格情報ブローキング |
| imperative / declarative | 命令的 / 宣言的 |
| Kubernetes access | Kubernetes へのアクセス権 |
| persistence / pluggable | 永続化 / 差し替え可能 |
| mint (token or credential) | 発行する |
| port-forward / split-tunnel in prose | ポート転送 / スプリットトンネル |
| task-oriented recipe | タスク指向のレシピ |
| distributed hub store | 分散型ハブストア |
| GC leader gate | GC のリーダー選出による制御 |

Render `rendezvous` contextually as connection establishment or mediation rather than leaving an
unexplained English noun. Literal commands, flags, YAML keys, and locale route fragments remain
verbatim even when the corresponding prose term is translated.

### Home-page positioning

The six-card home-page grid includes “The opposite of a local bridge,” summarizing the contrast
with Telepresence/Gefyra: those tools run a process locally and project it into the cluster, while
Cornus deploys the workload in the cluster and brings access back to the developer. The card links
to the long-form comparison page.

Keeping six cards required a merge. Registry and build engine form the coherent image-supply side
and are combined as “Build engine + OCI registry”; authentication and observability remain separate
because they are orthogonal operational concerns. The “nothing to provision” value belongs in the
card body rather than as a title qualifier.

## Files

- `docs/` and `docs/.vitepress/config.mts` - site source, logo, navigation, and sidebar.
- `.github/workflows/docs.yml` and `README.md` - Pages deployment and project landing page.
- `.agents/skills/distill-memories/SKILL.md` - promotion workflow that also targets the user site.
- `.agents/skills/translate-documents/` - source-faithful translation workflow and structural audit.
- `.agents/docs/JA_TRANSLATION_GLOSSARY.md`, `.agents/docs/QUALITY_GATE.md` - internal terminology
  and documentation/localization gate.

## Test Coverage

- Run `npm ci` then `npm run docs:build` from `docs/` with Node 20.
- The build has caught stale section paths, cross-page anchors, and invalid links outside the VitePress source directory.
- Run `audit_markdown_translation.py` for locale-tree parity, frontmatter structure, and locale-link
  checks; manually resolve its review warnings against the English source.

## Pitfalls

- GitHub Pages cannot publish until repository settings select GitHub Actions as the Pages source.
- `cornus deploy -f -` is unsupported, YAML examples must use `yaml:` / `json:` tags, and Zed compatibility is documented but not repository-tested.
- A green VitePress build does not detect wrong-locale absolute links or translated frontmatter
  keys. Run the localization checks separately.
- Do not use unapproved external or scripted machine translation. Translate from the authoritative
  source while preserving Markdown and interfaces.
- Generated `docs/.vitepress/dist/` is not hand-edited. Rebuild it before publication when source or
  locale content changes.
