# Cornus Web UI — Design System

The visual design system for the `cornus web` single-page app. It is a small,
token-driven system implemented in **one stylesheet**, `web/src/styles.css`, with
**no CSS framework** (no Tailwind, no CSS modules) — just CSS custom properties
and a flat vocabulary of global class names that the Solid.js views consume.

Read this before restyling the UI, adding a screen, or introducing a new
component, so the app keeps one coherent look-and-feel.

## Where it lives and how it ships

- **Source of truth**: `web/src/styles.css`, imported once in `web/src/index.tsx`.
- **Consumers**: the views in `web/src/views/*.tsx` and components in
  `web/src/components/*.tsx`. They reference the class vocabulary below; they do
  not carry their own stylesheets (except third-party `@xterm/xterm/css/xterm.css`
  imported by `Term.tsx`).
- **Brand assets**: `web/public/cornus-logo.svg` (a copy of the canonical
  `assets/cornus-logo.svg`). Vite copies everything in `web/public/` to the build
  root, so the logo/favicon land in `pkg/webui/dist/` and get embedded by
  `//go:embed all:dist` in `pkg/webui/webui.go`.
- **Build**: `cd web && npm run build` (`tsc --noEmit` + `vite build`) emits into
  `pkg/webui/dist/`. `make web` wraps this and is a prerequisite of `make build`.

The stylesheet is organized top-to-bottom in five layers; keep additions in the
matching layer:

1. **Design tokens** — `:root` custom properties + the dark-theme overrides.
2. **Base & typography** — resets, `body`, headings, links, focus.
3. **Layout: sidebar** — the nav shell.
4. **Controls & forms** — `input`/`select`/`textarea`/`button`/`label`.
5. **Components** — cards, badges, tables, logs, kv lists, editor, terminal, graph.
6. **Responsive** — the single `max-width: 720px` breakpoint.

## Principles

- **Token-first.** Never hard-code a color, radius, or spacing value in a
  component rule — reference a `var(--token)`. If the value you need does not
  exist, add a token, do not inline a literal. The only literals in component
  rules are structural (`0`, `100%`, `1px` hairlines, the terminal's `#000`).
- **Theme through tokens, not through media queries.** Components are styled once;
  light/dark is expressed purely by redefining tokens (see Theming). A component
  rule should never appear inside `@media (prefers-color-scheme: dark)`.
- **One accent, semantic colors are separate.** The purple accent is the brand
  identity (links, primary buttons, focus, active nav). Good/warning/bad are a
  distinct semantic axis (`--ok`/`--warn`/`--bad`) and never double as the accent.
- **Reuse the class vocabulary.** Prefer an existing class (`.card`, `.badge`,
  `.row`, `table.grid`) over a one-off inline `style`. Inline styles in the views
  are reserved for per-instance layout nudges (margins), not for re-theming.
- **Comfortable leading.** Body text runs at `--leading-normal` (1.55); headings
  tighten to `--leading-tight`. Do not ship text with the browser default.

## Design tokens

All tokens are defined on `:root` for the light theme and overridden under
`@media (prefers-color-scheme: dark)`. Values below are the current definitions.

### Color — neutrals & surfaces

| Token | Light | Dark | Use |
|-------|-------|------|-----|
| `--bg` | `#ffffff` | `#16181d` | Page background |
| `--bg2` | `#f4f5f7` | `#1f2229` | Sidebar, log blocks, subtle fills, row hover |
| `--bg-elevated` | `#ffffff` | `#1c1f26` | Cards, inputs, buttons — surfaces that sit "above" the page |
| `--fg` | `#1a1d21` | `#e5e7eb` | Primary text |
| `--fg-dim` | `#5c6570` | `#9aa4af` | Secondary text, labels, table headers, `.muted` |
| `--fg-muted` | `#8b95a1` | `#6b7480` | Placeholders, the select chevron |
| `--border` | `#d8dde3` | `#333842` | Default borders / hairlines |
| `--border-strong` | `#b7bfc9` | `#464d59` | Hover borders on controls |

### Color — brand accent (from `cornus-logo.svg`)

| Token | Light | Dark | Use |
|-------|-------|------|-----|
| `--accent` | `#4b1dc7` | `#a78bfa` | Links, primary button bg, active nav text, focus border |
| `--accent-hover` | `#3d17a3` | `#c4b5fd` | Primary button hover |
| `--accent-fg` | `#ffffff` | `#16181d` | Text/icon on an accent-filled surface |
| `--accent-subtle` | `rgba(75,29,199,.10)` | `rgba(167,139,250,.16)` | Focus ring, active-nav pill, hover tint |
| `--accent-border` | `rgba(75,29,199,.35)` | `rgba(167,139,250,.40)` | Reserved for accent-tinted borders |

The logo gradient runs `#4b1dc7 → #b39af5` (stroke `#876cd0`); the light accent
takes the deep end, the dark accent a lighter violet from the same family so it
stays legible on the dark ground.

### Color — semantic (status)

| Token | Light | Dark | Subtle fill (light / dark) |
|-------|-------|------|----------------------------|
| `--ok` | `#16a34a` | `#4ade80` | `--ok-subtle` `rgba(22,163,74,.12)` / `rgba(74,222,128,.14)` |
| `--warn` | `#b45309` | `#fbbf24` | `--warn-subtle` `rgba(180,83,9,.12)` / `rgba(251,191,36,.14)` |
| `--bad` | `#dc2626` | `#f87171` | `--bad-subtle` `rgba(220,38,38,.12)` / `rgba(248,113,113,.14)` |

The `-subtle` fills back the `.badge` variants; the solid colors are the text/icon.

### Typography

| Token | Value |
|-------|-------|
| `--font-sans` | `system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif` |
| `--font-mono` | `ui-monospace, "SF Mono", "JetBrains Mono", Menlo, Consolas, monospace` |
| `--text-xs` | `12px` (badges, table headers, log text) |
| `--text-sm` | `13px` (controls, form fields) |
| `--text-base` | `14px` (body — set on `body`) |
| `--text-lg` | `16px` (h2, brand wordmark) |
| `--text-xl` | `20px` (h1) |
| `--text-2xl` | `24px` (reserved) |
| `--leading-tight` | `1.25` (headings) |
| `--leading-normal` | `1.55` (body, `.muted`, `.kv`, logs) |
| `--leading-relaxed` | `1.7` (reserved for long-form) |
| `--fw-medium` / `--fw-semibold` / `--fw-bold` | `500` / `600` / `700` |

No web fonts are loaded (the system stack keeps the binary asset-free and avoids
CDN/CSP issues). Headings use `letter-spacing: -0.01em` (the brand wordmark
`-0.02em`); uppercase table headers use `+0.04em`.

### Spacing, radii, elevation, controls

| Group | Tokens |
|-------|--------|
| Spacing (4px base) | `--space-1:4` `--space-2:8` `--space-3:12` `--space-4:16` `--space-5:20` `--space-6:24` `--space-8:32` |
| Radii | `--radius-sm:6` (controls) `--radius-md:8` (logs, editor, terminal) `--radius-lg:12` (cards) `--radius-pill:999px` (badges, nav pill) |
| Elevation | `--shadow-sm` (cards), `--shadow-md` (reserved) — softer/lower-alpha in dark |
| Controls | `--control-h:32px` (shared height for input/select/textarea/button), `--control-px:10px` (horizontal padding), `--transition:120ms ease` |

Prefer `gap` with flex/grid over per-element margins for spacing between siblings.

## Theming model

The app follows the OS theme automatically via `color-scheme: light dark` and a
`@media (prefers-color-scheme: dark)` block that redefines **only the tokens**.
There is no in-app theme toggle. To add or change a color:

1. Add/adjust the token under `:root` (light).
2. Add/adjust the matching override under the dark media block.
3. Style the component through the token — never inside the media query.

`web/index.html` carries two `theme-color` meta tags (light `#4b1dc7`, dark
`#16181d`) so the browser chrome matches the theme.

## Component & class vocabulary

These are the stable hooks the views rely on. Keep them; if you rename one, update
every `.tsx` that uses it.

### Layout & brand

- `nav.sidebar` — fixed 200px left rail; collapses to a horizontal scroll bar
  under 720px. Contains the brand lockup and the nav links.
- `.brand` / `.brand-mark` / `.brand-name` — the logo + wordmark lockup
  (`App.tsx`): a 24px `<img>` of the logo beside the "Cornus" wordmark.
- `nav.sidebar a` — nav item; rounded, `--fg-dim`. `a.active` (and `:hover`) get
  an `--accent-subtle` pill with `--accent` text. Active state is driven by the
  router's `activeClass="active"`.
- `main` — content area; `--space-6 --space-8` padding, horizontal scroll for
  overflow.

### Typography & text

- `h1` (20/bold), `h2` (16/semibold), `h3` (14/semibold) — tight leading,
  consistent top/bottom margins.
- `a` — accent-colored, underline on hover.
- `.muted` — `--fg-dim` secondary text. `.error` — `--bad`, `white-space: pre-wrap`.

### Controls & forms

- **`input, select, textarea`** — one shared rule: `--control-h` height,
  `--control-px` padding, `--text-sm`, `--bg-elevated` surface, `--border`,
  `--radius-sm`. Hover → `--border-strong`; focus → `--accent` border +
  `--accent-subtle` ring; `::placeholder` → `--fg-muted`; disabled dims. `select`
  is `appearance: none` with an inline-SVG chevron and extra right padding;
  `textarea` is auto-height and vertically resizable.
- **`label`** — 13px, medium, `--fg-dim`. **`.field`** — a `column` flex wrapper
  (`gap: --space-1`) for a label + control pair.
- **`button`** — matches control height/radius, `--bg-elevated`. Variants:
  `.primary` (accent fill, `--accent-fg` text, hover → `--accent-hover`),
  `.danger` (bad text, hover → `--bad-subtle` fill). Focus-visible ring on all.

### Data & content components

- `table.grid` — full-width; uppercase dim headers with letter-spacing; hairline
  row borders; `tbody tr:hover` → `--bg2`. Cell modifier `td.wrap` allows wrapping
  and word-break for long values (image refs, paths).
- `.badge` — pill, `--text-xs`, medium. Base is a neutral `--bg2` chip; `.ok` /
  `.warn` / `.bad` swap to the matching `-subtle` fill + solid semantic text and
  drop the border. Solid state also driven inline via
  `classList={{ badge:true, ok, warn }}` in some views — keep those class names.
- `.cards` / `.card` — responsive auto-fill grid (min 260px) of
  `--bg-elevated` + `--shadow-sm` + `--radius-lg` panels; `.card h3` is the panel
  title.
- `.row` — flex row, `gap: --space-2`, wraps. The go-to inline grouping for
  buttons/inputs on one line.
- `.kv` — two-column definition-list grid (`dt` dim label / `dd` value) used on
  Overview's Server card.
- `pre.log` — scrollable monospace output block (`--font-mono`, `--text-xs`,
  `--bg2`, `--radius-md`) for apply output, spec JSON, streamed logs.
- `.editor-wrap` — border wrapper around the CodeMirror editor (`Editor.tsx`),
  caps height at `65vh`, hides the CM focus outline.
- `.term-wrap` — black padded frame around the xterm terminal (`Term.tsx`).
- `svg.graph …` — the dependency graph (`DependencyGraph.tsx`) reads its node /
  edge / arrow colors from the tokens, so it re-themes for free. `.node.running`
  outlines in `--ok`.

## Brand assets

- **Logo / favicon**: `web/public/cornus-logo.svg`. It is a **copy** of the
  canonical `assets/cornus-logo.svg` — treat `assets/` as the source; if the logo
  changes, re-copy it into `web/public/`. The same file serves as the sidebar mark
  and the `<link rel="icon" type="image/svg+xml">` favicon in `web/index.html`.
- **Wordmark**: "Cornus", rendered as `.brand-name` text (not baked into the SVG),
  so it inherits the theme's `--fg`.

## Accessibility

- **Focus is always visible.** A global `:focus-visible` rule paints an
  `--accent-subtle` ring; controls additionally shift their border to `--accent`.
  Do not remove outlines without providing an equivalent visible state.
- **Contrast.** Accent/foreground pairs are chosen to stay legible on both grounds
  (deep purple text on white, light violet on near-black). When adjusting accent
  or semantic colors, re-check text contrast in both themes.
- Semantic state is encoded in **both** color and shape (a tinted pill), not color
  alone.

## Extending the system

- **New token** → add under `:root`, add the dark override, then reference it.
- **New component** → add a rule in the Components layer using existing tokens;
  add the class to the consuming `.tsx`. Avoid inline `style` for anything
  themeable.
- **New form field** → just use a native `<input>`/`<select>`/`<textarea>`; the
  shared rule styles it automatically. Wrap label+control in `.field` if you need
  a stacked group.
- **New screen** → compose from `h1` + `.cards`/`table.grid`/`.row`; register the
  route in `web/src/index.tsx` and the nav link in `App.tsx`.

## Verifying visual changes

1. **Build/tests**: `cd web && npm run build` (must pass `tsc` + `vite`) and
   `npm test` (vitest renders the views against the mock BFF; asserts by
   text/role, so restyling should not break them). `go test ./pkg/webui/` checks
   the embed still holds an `index.html`.
2. **Live preview** (no backend): `npm run dev:mock` serves the app against the
   mock BFF; open the printed Vite URL and click through every view.
3. **Screenshot pass** (optional but recommended for design work): drive the mock
   preview with a headless browser and capture each route in **both** light and
   dark (set the emulated `colorScheme`). Playwright works well for this; install
   it as a throwaway dev tool for the capture and uninstall it after so it does
   not linger in `web/package.json`. Note the Vite `build`'s `emptyOutDir` wipes
   `pkg/webui/dist/`, including the tracked `.gitkeep` — recreate it if a build
   removes it (`.gitignore` keeps `dist/*` out of git except that marker).

## File map

| File | Role |
|------|------|
| `web/src/styles.css` | The entire design system (tokens + components) |
| `web/src/App.tsx` | Sidebar shell + brand lockup |
| `web/index.html` | Favicon, `theme-color`, `<title>` |
| `web/public/cornus-logo.svg` | Logo/favicon (copy of `assets/cornus-logo.svg`) |
| `web/src/views/*.tsx`, `web/src/components/*.tsx` | Consumers of the class vocabulary |
| `pkg/webui/webui.go` | Embeds the built `dist/` and serves the SPA |
