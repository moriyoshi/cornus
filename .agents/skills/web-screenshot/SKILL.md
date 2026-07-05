---
name: web-screenshot
description: "Capture a screenshot of a web page — primarily the `cornus web` SPA running locally (localhost:5000), or any URL — and embed it into a document (VitePress docs/, .agents/docs notes, or generic markdown) with the image saved to the correct asset location and a proper markdown/HTML embed. Use when asked to screenshot the web UI or a page for docs, add a UI image to a guide, or illustrate a document with a captured web view."
user-invocable: true
allowed-tools: Bash, Read, Write, Edit, Glob, Grep
---

# Capture a web screenshot and embed it in a document

Take a reproducible screenshot of a web page — most often the `cornus web` SPA —
and drop it into a user-facing document as a properly-referenced image.

**Use this skill when:** you are asked to screenshot the web UI (or any page)
for the docs, add a UI image to a guide, or illustrate a document with a
captured web view.

The default backend is **Playwright headless** (deterministic sizing, no user
Chrome required). Browsers are already cached on this machine; the driver is
provisioned on demand by `npx`, so there is no install step and no `node_modules`
added to the tree.

## Step 0 — Pick the source

**Cornus web UI (primary).** The SPA is served by `cornus serve` on the address
from `--addr` (default `:5000`). Get the server running, then screenshot
`http://localhost:5000/`.

- Prefer the built-in `run` skill to launch the app.
- Otherwise build and start it yourself, poll until it listens, capture, and
  stop it afterward. Example:
  ```
  export PATH="$HOME/.local/go/bin:$PATH"
  go build -o ./.agents-workspace/tmp/cornus ./cmd/cornus
  ./.agents-workspace/tmp/cornus serve --addr :5000 &   # note the PID
  # wait for readiness, e.g. curl -sf http://localhost:5000/ >/dev/null
  # ... capture (Step 1) ...
  kill %1                                                # stop when done
  ```
- The SPA is theme-aware (see `.agents/docs/DESIGN_SYSTEM.md`). Use
  `--color-scheme light|dark` so the shot matches the surrounding document.

**Any URL (secondary).** Just pass the URL to `--url`; no local server needed.

## Step 1 — Capture (default: Playwright headless)

Write the raw capture into `./.agents-workspace/tmp/` first so you can review it
before committing it to the doc tree.

Primary command (element crop, retina scale, theme, waits):

```
npx --yes -p playwright node .claude/skills/web-screenshot/scripts/shot.mjs \
  --url http://localhost:5000/ \
  --out ./.agents-workspace/tmp/web-ui.png \
  --width 1440 --height 900 --scale 2 --color-scheme dark
```

Useful flags (full reference at the top of `scripts/shot.mjs`):

- `--full-page` — capture the whole scroll height instead of just the viewport.
- `--selector <css>` — crop to a single element (e.g. a card or panel).
- `--wait <css|ms>` — wait for a selector or a millisecond delay before shooting
  (navigation always awaits `networkidle` first).
- `--width` / `--height` / `--scale` — viewport and `deviceScaleFactor`
  (default `1440x900@2x`, i.e. crisp on retina).

Zero-setup fallback for a simple full-page or viewport shot (no custom flags):

```
npx --yes playwright screenshot --full-page --viewport-size=1440,900 \
  http://localhost:5000/ ./.agents-workspace/tmp/web-ui.png
```

Then `Read` the PNG to confirm it captured what you expect.

## Step 1b — Alternate backend: `claude-in-chrome`

For pages that need the **user's authenticated session** or live interaction
(logged-in dashboards, multi-step flows), invoke the `claude-in-chrome` skill and
use its screenshot tool instead of Playwright. It drives the user's real Chrome,
so it captures the actual session and viewport — but sizing is less deterministic
than headless Playwright, so prefer Playwright for docs whenever the page is
reachable without a login.

## Step 2 — Place the asset for the target doc tree

Move the reviewed PNG from `./.agents-workspace/tmp/` to the location the doc
tree expects. Use a descriptive kebab-case filename.

- **VitePress `docs/` (including `docs/ja`, `docs/zh`):** save to
  `docs/public/screenshots/<name>.png`. VitePress serves `docs/public/` at the
  site root and shares it across all locales, so reference it from any language
  as an absolute path: `/screenshots/<name>.png`.
- **`.agents/docs/` notes:** save to `.agents/docs/assets/<name>.png` and
  reference it with a relative path from the note.
- **Generic markdown elsewhere:** co-locate an `images/` directory next to the
  document and reference the image with a relative path.

## Step 3 — Embed in the document

Insert the reference where the user asked. Always write meaningful alt text.

- Plain markdown (default):
  ```
  ![Cornus web dashboard showing a running deployment](/screenshots/web-ui.png)
  ```
- When you need to control the rendered width, use inline HTML (VitePress and
  GitHub-flavored markdown both render it):
  ```
  <img src="/screenshots/web-ui.png" alt="Cornus web dashboard" width="720">
  ```
- Add a short caption line below if the document style uses them.

## Step 4 (optional) — Optimize

`docs/public/` images ship with the site, so keep them reasonably sized. If
`oxipng` or `pngquant` is available, losslessly shrink the PNG; otherwise skip.

```
command -v oxipng >/dev/null && oxipng -o 4 --strip safe docs/public/screenshots/<name>.png
```

## Conventions

- Raw/working captures go in `./.agents-workspace/tmp/`; only the final,
  reviewed asset lands in the doc tree.
- Do not commit `node_modules` or built binaries into the tree.
- Any prose you add to repo-authored docs uses half-width punctuation (no `（`
  `）` or `：`).
