# Quality gate

The standard verification an agent (or human) runs before declaring a change
complete. This is the one-stop reference so you don't have to rediscover the
gate from `CLAUDE.md`, `TESTING.md`, the `Makefile`, and CI each time. Details
of the E2E harness itself live in [TESTING.md](./TESTING.md); this file is the
"what do I run before I say done" checklist.

## Purpose / when to use

Run this gate:

- Before declaring any change complete.
- Before claiming "CI should pass" or "this is green".

Match the depth of the gate to what you touched (see "Pick your level" at the
end). At minimum every Go change runs section 1.

## 1. Go gate (always)

The mandatory gate for any Go change (from `CLAUDE.md`). Put the toolchain on
`PATH` first (Go 1.26 at `~/.local/go/bin`; the module's `go 1.26.0` directive
auto-fetches the matching toolchain when needed):

```sh
export PATH="$HOME/.local/go/bin:$PATH"

gofmt -l <changed files>      # must print nothing
go build ./...
go vet ./...
go test ./...                 # or a focused package: go test ./pkg/<pkg>/
```

Run `gofmt -w` on every Go file you changed before building. Fix violations and
re-run until clean. Do not declare a change complete with a failing build, vet,
or test.

Notes:

- `go test ./...` needs no external daemons. The build-engine integration test
  (`pkg/build/builder`), the S3/GCS/Azure storage tests, and the `aws-sts`
  credential test are opt-in and self-skip unless root / a rootless userns stack
  / an emulator endpoint is present.
- Build outputs NEVER go into the version-controlled tree. Write binaries to
  `./.agents-workspace/tmp` (e.g. `go build -o ./.agents-workspace/tmp/cornus
  ./cmd/cornus`). Temp files go under `./.agents-workspace/tmp`, not `/tmp`.
- Static / container-ready binary:
  `CGO_ENABLED=0 go build -tags "netgo osusergo" -o ./.agents-workspace/tmp/cornus ./cmd/cornus`.
- Prezto shell pitfalls when scripting: `cp` is aliased to `cp -i` (`rm -f dst`
  first), `rm` to `rm -i` (use `rm -f`), and `NO_CLOBBER` makes `>` / heredocs
  fail on an existing file (`rm -f` first or use `tee`).

## 2. Scenario syntax check (cheap, no daemons)

For any edit to an E2E scenario (`e2e/scenarios/*.star`) or a harness builtin,
run the parse+resolve check. It needs no Docker/kind and no privileges:

```sh
make e2e-check
```

This builds the binaries and runs `cornus-e2e --check` over the `SCENARIOS`
list plus `EXTRA_CHECK_SCENARIOS` (`build-lazy-9p.star`,
`devcontainer-vscode.star`, `web.star`). It catches structural errors and
undefined-builtin typos against the same dialect + predeclared names a real run
uses. This is exactly what CI runs on PRs (`ci.yml`).

## 3. E2E harness locally

The harness drives a real `cornus` binary against a chosen target. Targets and
how to run them:

| Target | Make target | Needs on the host | What it runs |
|--------|-------------|-------------------|--------------|
| `docker` | `make e2e-docker` | a reachable Docker daemon (+ build engine: root or a rootless userns stack) | the full `SCENARIOS` list |
| `containerd` | `make e2e-containerd` | root, a reachable containerd socket, CNI reference plugins (`bridge`/`portmap`/`host-local`/`loopback` under `CORNUS_CNI_BIN_DIR`/`CNI_PATH`/`/opt/cni/bin`) | the backend-agnostic `SCENARIOS_CONTAINERD` subset |
| `kube` | `make e2e-kube` | host `kind` + `kubectl` + docker (creates/destroys a kind cluster; `KEEP=1` keeps it) | the full `SCENARIOS` list |
| `local` | `cornus-e2e --target local <scenario>` | build engine only (root/rootless + ssh tooling) | build-only scenarios, no deploy backend |

Single scenario on a host-tool target:

```sh
make e2e-one TARGET=<docker|containerd|kube|local> SCENARIO=e2e/scenarios/<name>.star
```

### kube WITHOUT a host kind cluster (containerized runner)

You do NOT need `kind`/`kubectl` on the host to exercise the kube target. The
all-in-one containerized runner (`e2e/container/Dockerfile`) bundles
Docker-in-Docker + kind + kubectl + the build engine + the binaries + scenarios,
and only needs a privileged `docker run`. This is the same path CI uses.

Run the whole kube suite in-container:

```sh
make e2e-container E2E_TARGETS=kube
```

`E2E_TARGETS` (default `docker`) selects targets; combine them, e.g.
`make e2e-container E2E_TARGETS="docker kube containerd"`.

To run a SUBSET of scenarios on kube, pass `E2E_SCENARIOS` (the entrypoint honors
it as an explicit scenario list, overriding the default `e2e/scenarios/*.star`
glob and the containerd subset). `make e2e-container` forwards it:

```sh
make e2e-container E2E_TARGETS=kube E2E_SCENARIOS=e2e/scenarios/deploy.star
```

Equivalently, build the image once and drive `docker run` yourself — useful when
iterating, since it skips the `e2e-image` dependency:

```sh
make e2e-image                                  # build cornus-e2e:latest
docker run --rm --privileged \
  -e E2E_TARGETS=kube \
  -e E2E_SCENARIOS="e2e/scenarios/deploy.star" \
  cornus-e2e:latest
```

Useful knobs (env into the `docker run`, or `make e2e-container` vars where noted):

- `E2E_TARGETS` — space-separated targets: `docker`, `kube`, `containerd`, `local` (default `docker`).
- `E2E_SCENARIOS` — explicit scenario paths (space/glob separated); overrides the default set (the `e2e/scenarios/*.star` glob and the per-target subsets). Honored by the entrypoint and forwarded by `make e2e-container`.
- `KEEP_CLUSTER=1` — keep the in-container kind cluster on exit (entrypoint env). The host `make e2e-kube` equivalent is `KEEP=1`.
- `E2E_MULTUS=1`, `E2E_MULTUS_IPVLAN=1`, `E2E_MULTUS_MACVLAN=1`, `E2E_MULTUS_DETACHED=1`, `E2E_MULTUS_PARENT=<nic>` — un-skip the Multus scenarios on the kube target (installs the vendored Multus DaemonSet). These ARE forwarded by `make e2e-container`.
- `E2E_KNATIVE=1` — install Knative Serving + Kourier into the kind cluster, un-skipping `deploy-knative.star`. Forwarded by `make e2e-container`.
- `E2E_INGRESS_NGINX=1` — install a real ingress controller into the kind cluster, un-skipping `ingress-tunnel-controller.star` and `ingress-settings-controller.star` (both self-skip without it). Note it also CHANGES the meaning of the controller-less ingress scenarios: `ingress-tunnel-ssh.star` self-skips once a controller exists, and `ingress-front-door.star`'s kube run covers the mux fallback that only exists when none is discoverable. That is why CI runs it as a separate `kube-ingress` leg rather than flipping it on the main kube leg. Forwarded by `make e2e-container`:

  ```sh
  make e2e-container E2E_TARGETS=kube E2E_INGRESS_NGINX=1 \
    E2E_SCENARIOS=e2e/scenarios/ingress-settings-controller.star
  ```

The container must be `--privileged`: the build engine mounts overlayfs and
creates namespaces, and kind + the 9p mount sidecars need it.

## 4. What CI runs

- On PRs (`ci.yml`), seven independent jobs. No full E2E execution on PRs.
  - `gate` — the Go gate (`gofmt -l`, `go build ./...`, `go vet ./...`,
    `go test ./...`) plus `go build -tags cloudblob ./pkg/storage/...`, the
    nested `e2e/scenarios/ftp` fixture module build, `make test-otel`, and
    `make e2e-check`.
  - `obsstore` — `make test-imbh`. Blocking (no longer `continue-on-error`).
  - `qos` — the yamux fork under `-race -short`, a full-size no-race run, and
    the `qosab` / `netemab` / `sqliteab` bench smokes.
  - `integration-backends` — emulator-backed storage and credential tests
    (winterbaume S3+STS, fake-gcs-server, Azurite) via
    `go test -tags "credaws cloudblob"`.
  - `helm` — `helm lint` plus two `helm template` renders.
  - `web` — the SPA gate: `npm ci`, `npm run build` (`tsc --noEmit && vite
    build`), and `npm test` (vitest) in `web/`. The SPA is embedded into every
    released binary, so this is not optional.
  - `image` — an amd64 build of the ROOT production `Dockerfile` plus smoke
    checks (`cornus version`, `version --features` asserting the cgo obsstore
    linked, and a `/healthz` probe against the started container). Catches a
    Dockerfile regression on the PR rather than at release time.
- Full E2E execution (`e2e.yml`, on pushes to `main` and manual
  `workflow_dispatch`): a matrix of the containerized runner over six legs —
  `docker`, `kube`, `containerd`, `bare`, `incus`, and `kube-ingress`
  (`fail-fast: false`, each reported independently). The `kube` leg sets
  `E2E_MULTUS=1`, `E2E_KNATIVE=1`, and `CORNUS_TEST_OTEL=1`. The `kube-ingress`
  leg is a second kube run with `E2E_INGRESS_NGINX=1` and `E2E_SCENARIOS`
  narrowed to the ingress set (`deploy-ingress.star`,
  `ingress-tunnel-controller.star`, `ingress-settings-controller.star`); it is
  separate because installing a controller changes what the controller-less
  ingress scenarios cover.

  The dedicated `bare` and `incus` legs use the same privileged runner image as
  the other targets and set `E2E_STRICT=1`; an unavailable runtime, an obsolete
  Incus daemon, or an unbuildable bare companion image fails rather than
  green-skipping. A separate `E2E_PREFLIGHT_ONLY=1` step performs the real
  per-target setup before the scenario run. Reproduce them with
  `make e2e-bare-container E2E_STRICT=1` and
  `make e2e-incus-container E2E_STRICT=1`.
- On tags (`release.yml`): its own `gate` job runs the four core Go gate
  commands on the tagged commit, and every artifact job depends on it. The
  GitHub Release depends on `image`, `chart`, AND `binaries`, so a failure in
  any one of them blocks publication rather than shipping a partial release.
  `workflow_dispatch` remains a publish-nothing dry run.

So "green locally on the matching target" approximates the corresponding CI job.
For a change that touches a runtime path, run the affected scenario on its
target (use the containerized runner for kube) to mirror CI before pushing.

## 5. Documentation and localization gate

Run this gate for changes under `docs/`, especially translated pages under
`docs/ja/` or `docs/zh/`. A successful VitePress build proves that Markdown and
links render, but it does not prove that a translation is faithful, natural, or
still uses the intended locale.

1. Compare each changed translated page with its English source section by
   section. Preserve all facts, ordering, commands, values, code, API paths,
   flags, configuration keys, and links. Do not add translator explanations,
   glossary content, or information absent from the source.
2. Treat front matter, YAML, JSON, shell snippets, and code fences as structured
   data. Translate reader-facing prose and comments only when appropriate; do
   not translate schema keys. In particular, VitePress front matter keys such as
   `layout`, `hero`, `image`, `src`, `actions`, `theme`, `link`, and `linkText`
   must remain verbatim. A translated `image:` key silently drops the home-page
   hero image without failing the build.
3. Review candidate English-Japanese interleaving manually. This scan is a
   review queue, not a replacement tool; exclude commands and inline code before
   deciding whether a candidate is a problem:

   ```sh
   rg -n --glob '*.md' '[ぁ-んァ-ン一-龠][[:space:]]+[A-Za-z]' docs/ja
   rg -n --glob '*.md' '^(イメージ|レイアウト|ヒーロー|ソース|リンク):' docs/ja
   ```

   Translate complete predicates and compounds from context. Do not perform
   word-frequency substitutions: they create wording such as `mint します`,
   malformed adjective phrases, and untranslated reader-facing headings.
4. Check that every Japanese character you authored is in NFKC normal form.
   Voiced and semi-voiced kana must be the precomposed code point (`が` U+304C,
   `ぱ` U+3071), never a base kana followed by a combining mark (U+3099 / U+309A);
   NFKC additionally folds half-width katakana (`ｶﾞ` -> `ガ`) and full-width ASCII.
   Decomposed kana renders identically to composed kana in every editor,
   terminal, and diff, so it only shows up under a scan:

   ```sh
   rg -n --pcre2 '[\x{3099}\x{309A}]' --glob '*.md' docs README.md ARCHITECTURE.md .agents
   ```

   Every hit must be inside a link `#fragment` — that is the one place the
   decomposed form is correct, for the markdown-it-anchor reason in step 6.
   A hit in prose, a heading, a table cell, or front matter is a defect: retype
   the run of text, or normalize just that string:

   ```sh
   python3 -c 'import sys,unicodedata; print(unicodedata.normalize("NFKC", sys.argv[1]))' 'ﾊﾟｽ'
   ```

   Do NOT run NFKC across a whole translated file — it would rewrite the anchors
   in step 6 into ids VitePress never emits, and silently kill those links.
5. Check that internal links on translated pages retain the locale prefix when
   they target a translated page. A link such as `/cli/build` is live but sends a
   Japanese reader to English; use `/ja/cli/build` instead. Do not prefix
   relative, external, or anchor links.
6. Run the complete documentation gate:

   ```sh
   cd docs
   npm run docs:check
   ```

   This rejects prohibited full-width punctuation in authored prose, rebuilds
   `docs/.vitepress/dist/` from the Markdown source, validates routes during the
   VitePress build, validates fragments against the generated heading ids, and
   rejects duplicate link targets within one Markdown list. The punctuation
   check ignores fenced and inline code plus URL destinations, where literal
   spelling is part of the documented interface. Never edit or publish generated
   files in `.vitepress/dist/` by hand.

   The gate needs Node on `PATH`. It may not be there in a non-interactive shell; if
   so, locate the installed toolchain and prepend it for the command at hand.

   `ignoreDeadLinks: false` fails the build on a dead route but never looks at the
   `#fragment`, so a link to a heading that does not exist is invisible to it.
   That gap bites hardest in the locale trees: markdown-it-anchor slugifies a
   heading AFTER Unicode decomposition, so a Japanese heading yields an id whose
   dakuten are separate combining code points, while a hand-typed fragment is
   composed. The two are visually identical everywhere a human would look, and a
   browser matches them by exact code points — so the link is silently dead.

   `docs:check-anchors` resolves every fragment against the ids VitePress actually
   emitted and, for this case, prints the exact id to paste. Copy what it prints;
   do not retype it. The checker itself lives in the `assess-doc-quality` skill
   (`.agents/skills/assess-doc-quality/scripts/check_anchors.py`); the npm script
   is a thin wrapper, and the script can also be run directly from the repository
   root. Do NOT try to "fix" this by overriding
   `markdown.anchor.slugify` — the conventional recipe strips combining marks,
   which deletes the dakuten and mangles every Japanese id (measured: 25 dead
   anchors became 64).

   `docs:check-duplicate-targets` canonicalizes in-site destinations and reports
   repeated targets within a single navigation, "See also", or "Related pages"
   list. Repetition elsewhere in a page is allowed, and distinct fragments on
   the same page remain distinct targets. Its implementation and regression
   tests live beside the anchor checker.

Maintain terminology guidance in `.agents/docs/JA_TRANSLATION_GLOSSARY.md` and
`.agents/docs/ZH_TRANSLATION_GLOSSARY.md` (both listed in `AGENTS.md`); add a newly
settled term in the same change that introduces it.
Record translation-review findings and the remaining review queue in
`.agents/docs/JOURNAL.md`; these aids are internal and must not appear in public
documentation.

## Pick your level

- Pure-Go change with no runtime/scenario surface: the Go gate (section 1).
- Scenario edit or new builtin: Go gate + `make e2e-check` (section 2).
- Harness or product change touching a runtime path (deploy/build/registry/
  network/etc.): also run the specific affected scenario on its target — the
  host `make e2e-docker`/`e2e-containerd`/`e2e-one` for docker/containerd, and
  the containerized runner (`make e2e-container E2E_TARGETS=kube`, or a direct
  `docker run` with `E2E_SCENARIOS=...` for a subset) for kube.
- Documentation or localization change: run the documentation and localization
  gate (section 5), then the VitePress build.
</content>
</invoke>
