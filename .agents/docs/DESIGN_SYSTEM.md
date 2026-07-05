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
3. **Layout: page header** — the fixed top bar that is the whole nav shell.
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
| `--bg2` | `#f4f5f7` | `#1f2229` | Page header, log blocks, subtle fills, row hover |
| `--bg-elevated` | `#ffffff` | `#1c1f26` | Cards, inputs, buttons — surfaces that sit "above" the page |
| `--fg` | `#1a1d21` | `#e5e7eb` | Primary text |
| `--fg-dim` | `#5c6570` | `#9aa4af` | Secondary text, labels, table headers, `.muted` |
| `--fg-muted` | `#8b95a1` | `#6b7480` | Placeholders, the select chevron |
| `--border` | `#d8dde3` | `#333842` | Default borders / hairlines |
| `--border-strong` | `#b7bfc9` | `#464d59` | Hover borders on controls |

### Color — brand accent (from `cornus-logo.svg`)

| Token | Light | Dark | Use |
|-------|-------|------|-----|
| `--accent` | `#4b1dc7` | `#a78bfa` | Links, primary button bg, active nav text, focus ring and field border |
| `--accent-hover` | `#3d17a3` | `#c4b5fd` | Primary button hover |
| `--accent-fg` | `#ffffff` | `#16181d` | Text/icon on an accent-filled surface |
| `--accent-subtle` | `rgba(75,29,199,.10)` | `rgba(167,139,250,.16)` | Active-nav pill, selected palette row, hover tint |
| `--accent-border` | `rgba(75,29,199,.35)` | `rgba(167,139,250,.40)` | Reserved for accent-tinted borders |
| `--focus-ring` | `0 0 0 2px var(--accent)` | (same, resolves per element) | The keyboard focus ring — see below |

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

The warn family carries three more, all for the **attention badge** (below). They are
warn-only on purpose: nothing else in the app has a state that asks for a human.

| Token | Light | Dark | What it is |
|-------|-------|------|------------|
| `--warn-strong` | `rgba(180,83,9,.32)` | `rgba(251,191,36,.34)` | the sweeping band on the badge |
| `--warn-fg` | `#ffffff` | `#16181d` | text on a SOLID `--warn` fill, the warn twin of `--accent-fg` |
| `--warn-deep` | `#6b2f05` | `#b8860f` | the far end of the number bullet's brightness pulse |

`--warn-deep` steps DOWN from `--warn`, which is what keeps `--warn-fg` legible at both ends
of the pulse — 10.3:1 light, 5.5:1 dark. Its value is a **measurement, not a preference**:
the first attempt swung the disc's luminance 1.77x light / 1.42x dark, which animated
correctly and read as motionless. These swing 3.08x / 2.12x. If you retune it, re-measure
both numbers (see Verifying visual changes).

### Color — categorical series (charts only)

`--series-1` … `--series-8`, the identity channel for chart marks. **Eight hues in a
fixed order, assigned in sequence and never cycled.** The order is the
colorblind-safety mechanism, not a taste, so re-ordering or extending it is a
change to the guarantee — a ninth series folds into "not shown" (see
`capSeries`), never into a generated hue.

| Slot | Hue | Light | Dark |
|------|-----|-------|------|
| 1 | blue | `#2a78d6` | `#3987e5` |
| 2 | orange | `#eb6834` | `#d95926` |
| 3 | aqua | `#1baf7a` | `#199e70` |
| 4 | yellow | `#eda100` | `#c98500` |
| 5 | magenta | `#e87ba4` | `#d55181` |
| 6 | green | `#008300` | `#008300` |
| 7 | violet | `#4a3aa7` | `#9085e9` |
| 8 | red | `#e34948` | `#e66767` |

Validated against `--bg-elevated` (the card surface every chart sits on) for the
OKLCH lightness band, the chroma floor, adjacent-pair separation under simulated
protanopia/deuteranopia, and contrast:

| Mode | Worst adjacent CVD ΔE | Worst adjacent normal-vision ΔE | Contrast |
|------|----------------------|--------------------------------|----------|
| light | 9.1 (≥8 target) | 19.6 (≥15 floor) | slots 3/4/5 below 3:1 — relief required |
| dark | 8.4 | 19.3 | all ≥ 3:1 |

Three light-mode slots sit under 3:1 on the light surface, which is legal **only**
because every chart ships visible values: the legend carries each series' latest
figure and every panel has a table view. Do not add a chart that drops both.

Two rules that are easy to break by accident:

- **Color follows the entity, not the row.** Slots are assigned per series KEY
  (`assignSlots` in `web/src/views/metrics/series.ts`) and held for the panel's
  lifetime, so filtering or a series flickering between polls never repaints the
  survivors.
- **Text never wears a series color.** Marks carry the hue; labels, values, and
  legends stay in `--fg` / `--fg-dim`, with a colored `.chart-key` beside them.
  `--series-4` as text is illegible on the light surface.

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
| Layout | `--header-h:52px` (the page header's fixed height; `main` gets `100vh` minus this) |

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

- `header.appbar` — the app shell: a full-width `--header-h` bar pinned to the top
  of the viewport, `--bg2` with a hairline bottom border. Holds the brand lockup
  and the nav, and it is the only chrome — there is no sidebar. It is
  `position: sticky; top: 0` rather than `fixed` **on purpose**: staying in
  `#root`'s column flow is what leaves `main` a definite `100vh - --header-h`
  height, which the full-bleed Workspace sizes itself against. Switching it to
  `fixed` would collapse that and break the tiled layout.
- `.brand` / `.brand-mark` / `.brand-name` — the logo + wordmark lockup
  (`App.tsx`): a 24px `<img>` of the logo beside the "Cornus" wordmark. The
  wordmark hides under 720px so the nav keeps the width.
- `.appbar-nav` — the horizontal nav row; scrolls horizontally (with the
  scrollbar hidden) when the links outrun the bar.
- `.appbar-nav a` — nav item; `--radius-pill`, `--fg-dim`. `a.active` (and
  `:hover`) get an `--accent-subtle` pill with `--accent` text. Active state is
  driven by the router's `activeClass="active"`.
- `#root` — column flex, `min-height: 100vh`: the header row plus `main`.
- `main` — content area; `--space-6 --space-8` padding, horizontal scroll for
  overflow.

### Typography & text

- `h1` (20/bold), `h2` (16/semibold), `h3` (14/semibold) — tight leading,
  consistent top/bottom margins.
- `a` — accent-colored, underline on hover.
- `.muted` — `--fg-dim` secondary text. `.error` — `--bad`, `white-space: pre-wrap`.
  `.warn` — `--warn`, for a situation nobody got wrong that still needs reading: the
  terminal's "No shell found in this image" line, where `--bad` would blame the user
  and `.muted` would hide the one sentence that says what to do next. Reach for
  `.error` only when something failed.

### Keyboard focus

`:focus-visible` paints `box-shadow: var(--focus-ring)` and `outline: none`;
`button` and `.chart-plot` restate the same token, so there is one place to change
the ring and no rule that can quietly keep an old one. Two rules govern it.

- **The ring is a SOLID stroke, and it may not be `--accent-subtle`.** It was a
  3px `--accent-subtle` halo until 2026-08-02: a 10% wash measuring ~1.2:1 against
  the page, under WCAG 2.2's 3:1 floor for a focus indicator — and `--accent-subtle`
  is *also* the fill of the active nav pill and of a highlighted palette row, so on
  the two controls a keyboard user lands on most, the ring was the colour of the
  thing it was drawn on. 2px solid `--accent` measures 8.2:1 light, 5.8:1 dark, 7.0:1
  on the pill. Note the token is defined once: the `var(--accent)` inside it resolves
  against whatever `--accent` the element inherits, so dark mode needs no second
  definition.
- **A container that scrolls or hides its overflow must lend the ring room.** The
  ring is drawn OUTSIDE the border box, and a scroll container clips its descendants'
  ink overflow to its padding box — so a control that fills its scrollport loses the
  whole indicator, not a corner of it. Two containers in the app are exactly that
  shape and were the worst-lit spots in the UI: `.appbar-nav` (`overflow-x: auto`,
  and `overflow-y` computes to `auto` the moment its partner is not `visible`) and
  `.stack-subheader .crumbs` (`overflow: hidden`, load-bearing for the left fade and
  the `scrollLeft` anchoring, so it cannot just be dropped). Both take padding at
  least as wide as the ring and hand it straight back with a matching negative margin,
  so the ring gets somewhere to land and nothing moves. Adding a third such container
  means repeating the pair.
- **A file listing wears no ring, and that is the shape argument, not a dislike of
  rings.** Focus in `.fs-list` sits on the row's name LINK (the rows carry a roving
  tabindex on it), so the ring boxes a word inside a row instead of marking the row
  — and it does that on top of two cues already pointing at the same place: the row's
  `.fs-selected` fill (focusing a row selects it) and the list's `:focus-within`
  accent border. `.fs-list:focus-visible, .fs-list a:focus-visible` therefore take
  `box-shadow: none`. What replaces it is row-shaped: `tr:focus-within > td:first-child`
  gets a 2px inset `--accent` bar down the leading edge, which is the only cue that
  still says *which* row the cursor is on inside a shift+Arrow selection, where every
  row wears the same fill. Suppress a ring anywhere and you owe the same substitution.
- **Opting out is allowed, and must then be explicit.** `.pane-chooser` and
  `.pane-pick-overlay` both set `box-shadow: none` on focus, because focus there is on
  a panel that holds `aria-activedescendant` and the indicator is the highlighted row
  or the lit cross, not a frame round the whole card. The pick overlay's opt-out used
  to be free — its backdrop is `--accent-subtle`, so the old ring vanished on it by
  accident — which is why the rule is now stated rather than inherited.
- **Fields are the one control that does not wear it.** `input:focus` keeps its
  `--accent` border plus the soft `--accent-subtle` halo: the border already carries
  the contrast, and a solid ring outside it is a third edge on a control that reads as
  active anyway.

### Controls & forms

- **`input, select, textarea`** — one shared rule: `--control-h` height,
  `--control-px` padding, `--text-sm`, `--bg-elevated` surface, `--border`,
  `--radius-sm`. Hover → `--border-strong`; focus → `--accent` border +
  `--accent-subtle` ring; `::placeholder` → `--fg-muted`; disabled dims. `select`
  is `appearance: none` with an inline-SVG chevron and extra right padding;
  `textarea` is auto-height and vertically resizable.
- **`label`** — 13px, medium, `--fg-dim`. **`.field`** — a `column` flex wrapper
  (`gap: --space-1`) for a label + control pair. Bind the two with `for`/`id` when
  the control is REQUIRED: a required field whose label is merely adjacent is one a
  screen reader announces unlabelled.
- **`.setting-textarea`** — the multi-line variant of a Settings row's control:
  monospace, `max-width: 46ch`, `resize: vertical` only. Horizontal resize would
  drag it out of the grid column every other `.setting-row` aligns to. Used for a
  free-text LIST (the terminal's shell candidates), where a picker cannot work
  because the values are not enumerable.
- **`button`** — matches control height/radius, `--bg-elevated`. Variants:
  `.primary` (accent fill, `--accent-fg` text, hover → `--accent-hover`),
  `.danger` (bad text, hover → `--bad-subtle` fill). Focus-visible ring on all (see
  **Keyboard focus** above).

### Data & content components

- `table.grid` — full-width; uppercase dim headers with letter-spacing; hairline
  row borders; `tbody tr:hover` → `--bg2`. Cell modifier `td.wrap` allows wrapping
  and word-break for long values (image refs, paths).
  **Under a project heading, every table leads with Service.** The compose service
  is the one identifier the reader wrote themselves; the deployment resource
  (`<project>-<service>`), the local forward address, and the tunnel's public URL
  are all things the tooling derived from it. Leading with it also makes a
  section's tables scannable as one column — a project section stacks four
  (workloads, mounts, tunnels, forwards) and they are read against each other,
  which is exactly what the tunnel table's Workload-first ordering used to break.
  Corollaries: the Name column keeps its link (it is what the detail page, logs,
  and exec are addressed by) but does not lead; and a row with no service — a
  deployment outside the loaded project, where the BFF has no plan to name one —
  renders `—`, never the resource name borrowed into the service column. The BFF
  performs the join, so the browser never has to (`webTunnel.Service`,
  `webMount.Service`, `webWorkload.Service`).
  **Under a single workload's heading, a table carries neither Service nor
  Workload.** The section header states the pairing once (`shop-web · 2/2 running ·
  web · shop`) and every list below it has been filtered to that one deployment, so
  those two columns would be the same two strings on every row — not identification,
  just noise in the two most-read positions, pushing the columns that differ per row
  toward the horizontal scroll. A table shared between the two groupings takes the
  difference as a REQUIRED `scope: "project" | "workload"` prop (`MountTable`,
  `ForwardsView`), so a new section has to answer the question instead of inheriting
  a default. Head and body cells are dropped through separate `<Show>` blocks —
  changing one without the other misaligns every row under a correct-looking header,
  which is why `views.test.tsx` asserts header/row column counts agree in both
  groupings (`expectAlignedColumns`).
- `.table-scroll` — the wrapper a page table goes in. Inert above the breakpoint;
  under it the wrapper (not the page) takes the horizontal overflow, so reaching
  a table's last column no longer drags the headings, cards, and every other
  section sideways with it. The default for a page table — nowrap cells make any
  multi-column grid wider than a phone, and a bare table falls back to `main`'s
  own scroll. Measured at 390px: the workloads grid runs 576px inside a 358px
  wrapper and scrolls on its own, while `main` stays put. Carried by every page
  table: the Overview's (workloads, mounts, tunnels, forwards, terminal sessions)
  and the workload detail page's instances. `.panel-table` is the chart panel's
  own, always-on equivalent.
  **Inside a card the wrapper is always on** (`.card .table-scroll`, outside the
  breakpoint). A card is the narrowest container in the app — a cell of the
  `minmax(260px, 1fr)` grid is narrower at 1200px than the whole page is on a
  phone — so a table in one overflows at every width. Measured before that rule
  existed: the sessions table ran 345px inside a 237px card, spilled 107px past
  the card's right edge (under the next card, which paints over it and makes the
  clipping look intentional), and would not scroll — the State column was
  unreachable on desktop. Put a table in a card and you need this.
- `.section` — one band of a stacked page: a 2px rule above it, its own `h2`, its
  own content. The Overview's project and workload sections and the workload
  detail page's instances / spec / metrics / logs are all this. It is the unit a
  screen is built out of when everything belongs on the page at once, which is
  this app's default — see "Sections, not tabs" below.
  The rule earns its place by separating UNLIKE things: a project from a project,
  Spec from Logs. Where every band on a screen holds the same kind of thing, the
  heading already marks the boundary and the rule is scaffolding — use
  `.setting-group` instead.
- `.setting-group` / `.setting-row` / `.setting-text` / `.setting-title` /
  `.setting-sub` — the Settings screen. A group is an `h2` and its rows at the
  page's full measure, separated by spacing alone: `--space-8` above a heading
  against the `--space-3` below it. That RATIO is the whole grouping now that there
  is no rule, so closing it makes the last row of one group read as the first row
  of the next.
  A row is a two-column grid, `var(--space-4) 1fr`, with `.setting-text` pinned to
  column 2 so the checkbox column is RESERVED rather than merely filled — every
  setting's title starts on the same line whether its control is a leading checkbox
  or not. Left to auto-placement a checkbox-less row's text falls into column 1 and
  hangs 24px left of its neighbours. Inside the text column the order is always
  name, description, then control; a `select` there needs `align-self: flex-start`
  or the column stretches it to the width of a paragraph. `.setting-sub` is a
  continuation of the row above (the prefix combination under its toggle) and
  carries only the tighter top margin — the grid already aligns it.
  Settings is deliberately NOT `.cards`: a card grid is for independent readouts
  scanned side by side, and its `minmax(260px, 1fr)` cells make each panel narrower
  than the page, but a setting is prose with a control on the end of it.
- `.badge` — pill, `--text-xs`, medium. Base is a neutral `--bg2` chip; `.ok` /
  `.warn` / `.bad` swap to the matching `-subtle` fill + solid semantic text and
  drop the border. Solid state also driven inline via
  `classList={{ badge:true, ok, warn }}` in some views — keep those class names.
- `.badge.attention` — **the one badge that is a request rather than a report.** Every other
  badge states what something IS (running, degraded, read-only) and is read when the eye
  arrives; the terminal's "needs you" is the reason to look at all, and it is often on a
  background tab nobody is watching, beside a same-sized "working". So it moves: a
  `--warn-strong` band sweeps across it once every 2.4s, holding still for the first 40%
  so it reads as an occasional glint rather than a barber's pole. Applied at the single
  markup site in `Workspace.tsx`, which is also what the pane chooser lists its rows with,
  so the badge behaves the same wherever the pane is shown.
  - **Except in a list of destinations.** `TileCtx.tabTitle(pane, plain)` renders the pane's
    name and drops every state badge when `plain` is set, and the pane chooser sets it for a
    caller's question (`asking()` in `PaneChooser.tsx`) — *Copy … to another pane*, *Move …*,
    *Follow this terminal in…*. A badge answers "what is this pane doing", which is what a tab
    exists to say; on a row that means "send it here" it reads as a refusal the purpose never
    made, next to the greyed rows that are the real ones. Hidden in CSS rather than not
    rendered, it would still be a rule that has to be kept in step with a decision made in
    TSX — so the decision is made once, where the row is built.
  - **The travelling value is a REGISTERED custom property** (`@property --attn-stop`,
    `syntax: "<percentage>"`). This is not decoration: `background-image` has animation type
    **discrete**, so keyframing between two `linear-gradient()` values does not sweep, it
    flips — and an unregistered custom property fails identically, being a token rather than
    a value. The gradient is declared once on the rule with all three stops placed off
    `--attn-stop`; the keyframes move only that property. A test walks every `@keyframes`
    block and fails on any discrete property, because this mistake reads as correct.
  - **The pane NUMBER answers with it**, keyed off the badge with
    `.tab:has(.badge.attention) .pane-number` — the badge's presence is the state, so the
    tiling chrome (which draws the number and knows nothing of sessions) needs no new input
    and the two cannot disagree. It inverts rather than tinting: a solid `--warn` disc with
    `--warn-fg`, the warn twin of `.pane-number.current`'s accent fill. A wash behind a
    digit in a 1.6em circle is barely a colour; filling the disc is what that element
    already does to say something about itself.
  - **Deliberately NOT the same animation.** A band travelling across a 1.6em disc is a
    smear, not a movement; the disc breathes its whole fill instead (`--warn` -> `--warn-deep`
    -> `--warn`). Same **2.4s** period, written as the same number in both rules rather than
    reached by `alternate` at half the duration — a duration you have to double in your head
    to compare is one that gets changed on one side only.
  - **Attention outranks "you are here."** The number rule out-specifies
    `.pane-number.current` and takes its accent fill, on purpose: where the focus is stays
    written on `.tab.active` and `.stack.focused`. The big `display-panes` plate is excluded
    — it shows one number per TILE while the badge may belong to a background tab of that
    tile, so a `:has()` on `.stack` would light the wrong digit.
  - Reduced motion stops both. The badge pins `--warn-strong` (its emphasis WAS the
    animation); the number needs nothing pinned, its fill being a solid it already has.
- `.cards` / `.card` — responsive auto-fill grid (min 260px) of
  `--bg-elevated` + `--shadow-sm` + `--radius-lg` panels; `.card h3` is the panel
  title, and `.card h4` labels a SECOND block under it (sized down and dimmed, so
  the card still reads as one panel with one name — a second `h3` reads as two
  cards that lost their border). The Overview's row is the session-wide readout —
  the facts that hold no matter which project you are looking at (server, workload
  counts + the live terminal sessions, client agent, conduit) — and every card in
  it renders unconditionally, saying what state it is in rather than disappearing. A card that vanishes when it has nothing to report
  leaves a reader unable to tell "off" from "this dashboard doesn't cover that";
  give it a fallback line instead (the conduit's is "No proxy conduit."). Anything
  scoped to one project belongs in that project's `.section`, not here.
- `.row` — flex row, `gap: --space-2`, wraps. The go-to inline grouping for
  buttons/inputs on one line.
- `.kv` — two-column definition-list grid (`dt` dim label / `dd` value) used on
  Overview's Server card. A value may hold a `.row` (the backend name plus its
  capability chip, an ingress mode plus its domain), and `.kv dd .row` tightens
  that row's `row-gap` below `.kv`'s own: in a card the value column is only about
  150px, so such a value usually wraps, and with `.row`'s default 8px gap the
  wrapped line sits FURTHER from its own label than the next entry does — the chip
  then reads as the next label's value. Keep the inner gap smaller than the outer
  one; the stylesheet test asserts the relation, not the number.
- `pre.log` — scrollable monospace output block (`--font-mono`, `--text-xs`,
  `--bg2`, `--radius-md`) for apply output, spec JSON, streamed logs.
- `.editor-wrap` — border wrapper around the CodeMirror editor (`Editor.tsx`),
  caps height at `65vh`, hides the CM focus outline.
- `.term-wrap` — black padded frame around the xterm terminal (`Term.tsx`).
- `.term-drop` — the layer around a workspace terminal that takes a file drop
  (`TermPane.tsx`). It changes no geometry (it fills the tile the terminal was
  going to fill) and exists so the `dragover` bubbling out of xterm's own DOM has
  somewhere to be `preventDefault`ed; unprevented, the browser pastes the dragged
  text into the focused textarea. It wears the file pane's own `.fs-drop-here`
  ring while a drop is over it — one gesture, one piece of feedback.
- `svg.graph …` — the dependency graph (`DependencyGraph.tsx`) reads its node /
  edge / arrow colors from the tokens, so it re-themes for free. `.node.running`
  outlines in `--ok`.
- `.page-head` — a `.row` holding a screen's `h1` and the one control that governs
  the whole screen (Metrics: the scope switch). The row owns the heading's bottom
  margin, so no other screen's `h1` moves and the control cannot drift from the
  title it belongs to.
- `.filters` — the one filter row a dashboard screen puts **above** everything it
  scopes (Metrics: range, and workload when the scope has workloads in it). Never
  inside a card and never per-chart: two charts filtered differently while looking
  like one dashboard describe two different moments.
- **A control that decides whether another control EXISTS is not that control's
  peer.** It goes above and outside — `.page-head`, not `.filters` — because a
  drill-down reads outside-in. Metrics' scope switch decides which panels exist and
  therefore whether a workload filter exists at all; while it sat in the filter row
  the screen showed three equal-looking controls, one of which appeared and vanished
  for no reason visible from the row. Inside `.filters`, every control narrows the
  chosen scope and none of them changes what the others are.
  This is about the driver/driven relationship, not a blanket "mode switches go in
  the title": the driver belongs immediately above and outside **exactly** what it
  drives. The Overview's "By project / By workload" `.seg` correctly sits below the
  summary cards and above the project sections — it governs only the sections, and
  the cards hold in either grouping, so hoisting it to the `h1` would put it above
  things it does not drive.
- `.panels` / `.panel` — the chart-panel grid (auto-fill, min 420px, one column
  under the 720px breakpoint) and its card. `.panels.compact` + `.panel.compact`
  are the STRIP form the project and workload sections carry (min 300px, a
  128px-tall plot, no hint text), introduced by `.strip-head` — the section's
  "Metrics" heading row with its window and its "All metrics →" link.
  `.panel.stale` is the refetch state:
  the previous render held at reduced opacity, never a skeleton, so nothing jumps.
  Inside: `.panel-head` / `.panel-title` / `.panel-metric` (the PromQL name, mono)
  / `.panel-value` (the current figure, proportional digits) / `.panel-hint` /
  `.panel-capped` (how many series were withheld) / `.panel-table` (the table view,
  tighter than a page table and horizontally scrollable).
- `.chart` and friends — the SVG time-series chart (`components/TimeSeriesChart.tsx`),
  themed entirely through tokens: `.chart-plot` (the focusable `<svg>`),
  `.chart-grid` / `.chart-axis` / `.chart-axis-rule` (hairline, solid, recessive —
  never dashed), `.chart-line` (2px, round caps), `.chart-area` (a 10% wash, only
  ever under a **single** series), `.chart-dot` (r=4 with a 2px `--bg-elevated`
  ring so overlapping ends stay legible), `.chart-crosshair`, `.chart-tip` +
  `.chart-tip-time` / `-row` / `-value` / `-name` (value first and strong, series
  name second and dim), `.chart-legend` + `.chart-key` (a 12×2px stroke of the
  series color, not a filled box) + `.chart-legend-name` / `-value`,
  `.chart-empty` (`.chart-loading` while in flight, `.chart-nodata` once settled —
  the two are different statements and tests wait on the latter).
- `.toaster` / `.toast` — the app-wide transient message layer (`toast.ts` +
  `views/Toaster.tsx`, mounted once in `App.tsx`). Fixed to the bottom-right at
  `z-index: 200`, above the modal layer. A toast is a `<button>` because clicking
  it dismisses; `--accent` on its left edge, `--bad` for `.error`. Use this for
  the outcome of an action ("copied 2 items", a failed transfer) — never a line
  inside a pane or a card, which reflows the content the user was reading.

### Tiled workspace

The Workspace (`web/src/views/Workspace.tsx`) is one tiled screen holding two
kinds of pane — a file browser on the virtual namespace, or a terminal on a BFF
session. It was two screens, Files and Terminal, and they were the same screen
twice: one chrome, one tree model, one set of pane commands, differing only in
the pane payload. The chrome is `web/src/views/tiling/panes.tsx`, over a DOM-free
tree model (`tiling/layout.ts`) and one rearrange protocol (`tiling/drag.ts`),
all three generic in that payload and none of them changed by the merge. The
authoritative commentary is in those files and in `styles.css`; what follows is
the class vocabulary and the one rule that governs adding to it.

- **The workspace may be larger than the viewport.** `.workspace-body` is a scroll
  container and `.workspace-canvas` inside it is sized in PERCENTAGES of that box
  from `LayoutState.ext`, the workspace extent in viewport units (`{w: 1.667, h: 1}`
  means "two thirds wider than the screen"). At an extent of `{1, 1}` the canvas is
  exactly the body and nothing scrolls, which is the fixed-viewport layout the screen
  shipped with — so every rule below is unchanged by this and every layout saved
  before it loads into it. The arithmetic that maintains `ext` is `tiling/grow.ts`,
  which is pure and has its own suite; the tree itself is untouched, because ratios
  are relative and only what they are fractions OF has changed.
  - Percentages, not pixels, for the same reason the tree uses ratios: resizing the
    window rescales the whole workspace instead of re-flowing it.
  - **Two fingers pan it** (`tiling/pan.ts`, attached to `.workspace-body`). One finger
    belongs to whatever is under it — a listing, a terminal, the tab bar, a divider — so
    the workspace takes the gesture nothing else wants rather than competing for the
    one-finger drag. It is told apart from the pinch (`pinch.ts`) by whether the fingers
    keep their separation, and it judges only once BOTH fingers have reported a move:
    browsers deliver one pointer per event, so mid-frame the separation appears to have
    changed by the whole step and a parallel drag would be rejected as a zoom.
  - **The workspace's own right and bottom borders are dividers** (`EdgeDivider` in
    `tiling/panes.tsx`, `resizeEdge` in `tiling/grow.ts`), rendered as the last children of
    `.workspace-canvas` so they sit on the workspace's edge rather than the screen's — which
    means they need scrolling to once the workspace exceeds the viewport. Every other
    divider trades between two tiles; these have no far side, so they move the extent, and
    which tiles come with them is `absorb` again. Hidden under the dividing layout, where
    the workspace is pinned to one screen and there would be nothing to drag.
  - Two modules own the SCROLL OFFSET and they run in a fixed order —
    `tiling/anchor.ts` around every commit (so growth in front of the viewport does
    not lurch the view sideways), then `tiling/reveal.ts` on the focused tile. Reveal
    is deferred by a microtask because a pane claiming the keyboard makes the browser
    scroll that element into view natively, synchronously, and last; without the
    deferral the native scroll wins and reveal is dead code. Both key off
    `data-stack-id` on `.stack` rather than refs, because a re-tile rebuilds the
    element under the same id.
  - Governed by `settings().workspaceGrowth`, read only through `workspaceExtends()`.
    Its `"divide"` state is the ORIGINAL code path, not a re-implementation.
  - **The sizing rule for an extending split** is three numbers: the workspace is multiplied
    by `GOLDEN` along the axis; everything already there shares `EXISTING_SHARE` (⅔) of the
    result, scaled by ONE factor so no two tiles change their relationship; the new tile
    takes the rest. Note this is the ONLY operation that rescales untouched tiles — a close,
    a move, a divider and a border handle all leave them at their exact size and give the
    change to the tiles facing the growing edge (`absorb`). The two rules answer different
    questions and are documented as a pair at the top of `grow.ts`.
  - **Extending is a last resort, not the default motion.** `applySplit` halves the tile
    like any tiler while both halves would still clear the floor for that axis, and only
    extends when they would not. The floors are in rem because they are legibility
    limits; the host converts to viewport extents in `floorFor`, since `grow.ts` measures
    nothing. An unmeasured container yields `Infinity`, which extends — the answer to an
    unknown screen size must be the one that cannot make a pane too small.
  - **Two floors, `MIN_PANE_WIDTH_REM` 40 and `MIN_PANE_HEIGHT_REM` 20**, selected by
    `minPaneRem(dir)`. Not one shared constant: 40rem is 640px, and half of any ordinary
    screen HEIGHT is under that, so a single value made every vertical split extend and
    the height axis never used the dividing layout at all. Measured, not guessed — the
    table is in JOURNAL (2026-08-04). Note `Dir` is the SPLIT's axis, so `"h"` (children
    left and right) is the one constrained by WIDTH.

- **Dragging is one vocabulary over two transports** (`web/src/dnd.ts`). A consumer
  declares a drag SOURCE and a drop TARGET — `accepts` / `over` / `leave` / `drop`
  — and the module decides how they are delivered. `"auto"` gives the mouse the
  real HTML5 drag (so OS file drops in and the Chromium drag-out download keep
  working) and a finger an emulated one built on pointer events; `"emulated"` is
  pointer events on every device, with native never registered. **The tiling
  chrome is `"emulated"` outright** — a tile move never leaves the page, so native
  buys it nothing and cost it the whole gesture on touch; Files is `"auto"`,
  because its drag genuinely crosses the window boundary. Three rules the
  emulated path keeps, because they are native's and the consumers were written
  against them: targets NEST and a refusal falls through to the enclosing one; the
  departing target's `leave` runs BEFORE the arriving one's `over` (which is why
  `accepts` is a separate, pure predicate — two tiles share one "where would this
  land" signal); and a drop with no accepting target is a no-op, not a cancel.
  - **The lift rule is the gesture.** A mouse lifts as soon as it has moved; a
    finger only after a 400ms dwell, and a press that moves first is a SCROLL and
    is abandoned. This is why the drag surfaces must NOT carry `touch-action:
    none` — the tab bar pans horizontally and the file listing vertically. The
    scroll veto arrives with the lift instead, as a non-passive `touchmove`
    `preventDefault`, which also suppresses the native drag on iPadOS.
  - **A finger has no Shift**, so anything a modifier decided has to be asked
    instead: a touch drop in Files puts copy-or-move through `promptChoice`, and a
    dismissed question is a drop that never happened. `via` on every event is what
    lets a consumer tell the two apart; nothing else should need it.
- **A TERMINAL IS A TRANSFER DESTINATION**, since the BFF reports where each session
  is standing (OSC 7, or the foreground process's `/proc/<tpgid>/cwd`). "Into the
  folder this shell is in" is a virtual path like any other — `virtualPathOf(workload,
  cwd)`, the same conversion the follow pairing uses — so a terminal pane both takes a
  drop and appears as a chooser row for F5 / F6. Three rules hold it together:
  - **The transfer itself belongs to neither pane** (`views/transfer.ts`): the
    client-side refusals, the preflight, the overwrite question, the batch and the
    report are functions of two paths and nothing else. What a file pane adds — ghost
    rows, the arrival flash, a listing to re-fetch — is passed in as hooks, and a
    terminal supplies none of them, so the toast is the whole of its feedback.
  - **The destination is read at the last moment, never captured.** A drag or a walk
    to a chooser row takes as long as it takes, and the shell may `cd` during it. The
    pane's own `dir` is not an answer at all: it is where the session was TOLD to
    start, so using it would silently send files somewhere the user has left.
  - **The refusal names which fact refused it.** A terminal used to be refused by
    kind ("a terminal"); now it is offered unless it has no workload, no session, or
    a shell that never reports a directory — the last being both the common case and
    not a fault, since OSC 7 is absent from a stock image. The follow chooser still
    refuses terminals by kind, and that is a different question.
- **A pane's KIND lives on its payload, not on the tree.** `PaneData = FileData |
  TermData`, each tagged with `kind`, because `tiling/layout.ts` is generic in the
  payload and deliberately knows nothing about it. Every kind-dependent seam is
  already a host-supplied callback — `tabTitle` / `body` / `subHeader`, the
  `isValidData` predicate, `fresh` / `inherit` in `createTileDrag`, and the
  `freshData` factory `closePane` uses for the replacement pane.
- **The empty pane is a file browser at the virtual root.** That is what the
  screen opens as, what `New pane…` makes, and what replaces the last pane when it
  is closed. A pane's kind is therefore always settled by the command that created
  it — a split inherits the source's kind, `Open in a terminal…` makes terminals —
  so nothing ever has to ask "which kind?".
- **Creating a pane asks where it goes by default, and asks by pointing.** The
  wireframe targets (`drag.beginPlace`) are the workspace's one question, and
  every command that makes a pane uses it — `New pane…`, `Split pane…`, `Open in a
  terminal…`, `Open…`. Opening was the exception, twice over: a file prompted for a
  keyboard open and stacked a tab silently for a mouse one, and a folder always
  stacked. The argument for the silent stack was that the click had said where by
  being on that pane. It had not — a click on a row says which ROW, and the tile it
  landed on is the one already spending its width on the listing. The gesture that
  arrived is not an input to where the pane goes. Note which way the cost falls: the
  question adds one keypress and its centre reproduces the old outcome exactly,
  whereas a silent stack that guessed wrong cost a pane to undo.
  - **Which is what let two commands become one.** "Open in a new tab" (a folder)
    and "Open" (a file) were the same sentence with different objects, held apart
    only by the folder one stacking where the file one asked. Once both asked,
    nothing but the payload was left between them — a folder pane is a listing of
    `path`, a file pane is `path` plus its `open` — so `openTarget()` returns
    whichever the selected row calls for, and one entry covers both. A trailing
    slash in the title (`Open "logs/"…`) is what carries the distinction the two
    command names used to: it is now the only thing saying which kind of row is
    about to open, and `logs` beside `logs.txt` is not a judgement to leave to the
    reader's eye for extensions.
  - **What a command aims at depends on whether it acts on a row.** `Open…` and
    `New pane…` read the selection, because a row is their object. `Open in a
    terminal…` reads the folder the pane is SHOWING and ignores the selection
    entirely: a terminal is a place to stand, not something done to a row. It used
    to consult `selectedDir()` like the other two, which made its title flicker
    between folder names as the arrow keys walked down a listing, and offered a
    shell somewhere the user had pointed at but not gone. The published docs had
    described the shown-folder behaviour all along — the code was the odd one out.
  - **`newPaneDisposition` is the question answered in advance.** `"ask"` (the
    default) is the prompt; `"split"` and `"tab"` are the two answers the prompt
    itself offers — the arrow and Space — made standing. Deliberately not new
    behaviours: no layout is reachable one way and not the other, so the setting
    trades a keypress for a guess without narrowing anything. It is applied inside
    `beginPlace` and NOT at the three call sites, so the chokepoint is the whole
    answer to "does this cover command X?" and a fourth creating command cannot
    forget it. The two answers are matched POSITIVELY and everything else falls
    through to asking — `parseSettings` spreads stored JSON over the defaults
    without validating, and the safe reading of an unknown value is the question.
    - **`Split pane…` is outside it, and that is a decision.** Its title already
      names the disposition it makes: under `"tab"` the setting would have to
      contradict the word Split, and under `"split"` it would collapse into
      `prefix %`. What it asks is WHICH EDGE — a finer question than the setting
      answers, whose standing answers are the two directional split commands.
    - **A previous placement setting was deleted for configuring nothing** (see
      JOURNAL, 2026-08-02). The difference is that this one is read on every route
      that creates a pane; the old `newPaneSide` answered half of a modal that no
      longer existed. A setting has to be the whole answer to a question the UI
      actually asks.
  - **The one caller that must NOT ask** is `openTabAt`, used after a typed transfer
    destination resolves. That tab is a consequence of a completed copy, not a pane
    the user asked to create, so interrupting them to point at a tile would be a
    question about somebody else's action.
- **`.tab-name`** wraps the tab's label for BOTH kinds. It reads as decoration on a
  file tab, which has no badges beside it, but the class is what says "this is the
  name, as opposed to the session badges next to it", and a tab bar where half the
  labels answer to it cannot be addressed uniformly.
- **A pane takes DOM focus WHILE it is the focused one** — not "when it mounts as"
  one, which is the weaker rule this used to be and the source of two reported
  defects. `tiling/focusclaim.ts` is the single implementation: `claimFocus(focused,
  holds, take)`, where the module owns the WHEN and each pane answers the other two.
  A terminal takes the xterm, a browser the roving row (selection left alone), an
  editor the document, an image viewer its own `tabindex="-1"` tile.
  - **`holds` is what makes the effect safe to re-run**, and it replaced a
    once-per-mount latch. The latch stopped a refresh from yanking the cursor back
    to the top, but it also meant a pane could be claimed only on the way IN: walking
    back to a listing left the keys in the pane you came from. Asking "is the keyboard
    already somewhere in this pane?" is the honest form of the same guard — true of
    the refresh, false of the walk-back.
  - **Answer `holds` about the whole pane, not the element `take` focuses.**
    Otherwise a toolbar button inside the pane loses the keys to the next re-render.
  - This is also the other half of `tiling/drag.ts`'s focus-before-commit rule: the
    pick's targets vanish the instant it commits, so a pane that does not claim the
    keyboard drops it on `<body>`.
  - **The mode guard (`modeOwnsKeyboard`) is carried but unverified.** No test
    reaches it — every claim depends on data that arrives asynchronously, so by the
    time an effect re-runs the modal that was up has closed. It is kept because
    removing it would be a behaviour change made on the strength of not having found
    the path, not because it is covered.
- **`focusRow` collapses the selection itself, rather than leaving it to `onFocus`.**
  Focusing an ALREADY-focused row fires no focus event, and arriving in a pane now
  parks the cursor on the roving row: without this the very next bare ArrowDown
  resolves back to that same row and looks like a dead key.

- `.workspace` / `.workspace-body` / `.workspace-canvas` — the full-bleed host
  (`position: absolute; inset: 0` inside `main`, which is why the app bar must stay
  `sticky`, not `fixed`; see Layout & brand). `--space-2` of padding, no more: the
  gutter is a dead zone in front of the outermost panes' split overlays. The BODY is
  the scroll container (`overflow: auto`, `overscroll-behavior: contain` so a pan off
  the end is not a back-navigation); the CANVAS is `flex: 0 0 auto` with
  `min-width/height: 100%`, sized inline in percentages from the workspace extent.
- `.split` / `.split-child` / `.divider` — a split and its 6px drag handle
  (`.split.h` side by side, `.split.v` stacked). Children `flex-grow` by ratio.
  The handle carries `touch-action: none`; it widens to 14px under
  `(pointer: coarse)` by growing its `flex-basis`, never by a `::before` overlay
  — a pseudo-element is not an event target, so events over it report `.divider`
  and `ownerOf()` would claim pixels lying inside the neighbours' edge strips.
- `.stack` / `.stack-tabs` / `.tab` / `.tab-close` / `.stack-subheader` /
  `.stack-body` / `.stack-pane` — a tile: tab bar over body. The bar is
  `overflow-x: auto` and is itself the whole-stack drag handle. It carries
  `user-select: none` (a pointer drag suppresses nothing by itself, and a mouse
  drag across it would otherwise paint a text selection over the labels), and
  under `(pointer: coarse)` it and `.tab` carry `-webkit-touch-callout: none`
  — the long press is the drag, and the platform wants that press for its
  selection callout. `html.dnd-dragging` is set for a drag's duration and takes
  the cursor over everything it crosses.
- `.pane-split-zone` (+ `.armed`, `.charging`, `.cooling`, `.pane-split-bar`) — the
  four invisible edge strips. **The strip is the bar**: its thickness comes INLINE
  from `SPLIT_EDGES` and its extent along the edge from `EDGE_INSET`, because those
  are the two numbers `edgeAt()` arms on — a hotzone that is live where nothing is
  drawn is the bug this gate exists to prevent, and running the strips the full
  edge is what used to make a tile's corners split it.
  - **A mouse arms by hovering** for 450ms, and the armed strip is then a real
    button. **A finger cannot hover**, so it gets two touches instead: hold
    (`SPLIT_HOLD_MS`, the bar glowing its way up), let go (the glow remains and
    fades over `SPLIT_HOT_MS`), touch again to split. Both durations are rendered
    inline from the state that drives the timers, so the motion and the deadline
    are one number.
  - **The gesture is expressed in HEAT**, one unclamped accumulator: a hold raises
    it, a release lets it fall, and the critical value (1) only decides what is
    SPENDABLE. A re-hold picks up from where it is — topping up a bar that has
    barely faded is nearly instant, rescuing one that is almost out costs what
    lighting it did. On a lit bar a TAP (`SPLIT_TAP_MS`) spends it and anything
    longer re-heats it; both begin identically, with the bar brightening under the
    finger, so the feedback is honest before the gesture has committed to being
    either.
  - **Holding does not stop at the critical value.** The visual peaks there — a lit
    bar cannot get brighter — so anything past it is banked rather than shown and
    comes back on release as `transition-delay`: time at full strength in front of
    the fade. `SPLIT_HEAT_MAX` caps it at one extra window, which it must, since
    heat rises ~29x faster than it falls.
  - The window is deliberately long (`SPLIT_HOT_MS`, 10s) and the fade **linear**, so
    brightness is heat one-to-one: half lit means half the window left, at every
    moment and from every starting point. Shape and duration are separate jobs and
    it was the duration that needed doing — ten seconds of linear fade dims by a
    tenth per second, unhurried by construction. Three easings were tried and each
    failed differently: a gentle one still ran the early fade faster than the late
    one, an exponential one held the bar at ~99% for four seconds and piled every
    visible frame into the drop at the end (which reads as *faster*), and any easing
    at all makes the rate depend on where the fade started, since a partially
    re-heated bar runs the same curve compressed into a shorter time. `linear` is
    declared explicitly — the base rule's shorthand supplies `ease`, so declaring
    nothing would be eased by inheritance.
  - It is a *transition*, not an animation, and that is load-bearing — a transition
    interpolates from the value the element is currently showing, so a re-hold
    reverses the fade in place instead of jumping to full and restarting.
  - The touch path commits from the PRESS and leaves the strip click-through
    throughout: the top strip lies over the tab bar, and a lit bar that swallowed
    presses would be a collision of its own. `SPLIT_HOLD_MS` must stay under
    `DRAG_LIFT_MS` — one press can be counting down towards both a split and a tab
    drag, and the charge retires the drag's pending press as it completes.
  - The whole edge-split reach stands down while `busy()` — a pick, the chooser, or
    **a drag**. That last one is not optional: a pointer drag emits `pointermove`
    right across the workspace, where the native drag it replaced emitted none, so
    without it a carried tab arms strips under itself.
- `.pane-menu` — the `⋮` in every tab bar: the tap-reachable route to the pane
  operations. It carries **no list of its own** — it focuses its tile and opens the
  command palette seeded with `:pane` (see *Menus are filtered palettes* below).
  `position: sticky; right: 0` so it survives the bar scrolling, and it resets the
  global `button { height: var(--control-h) }` so it stretches to the bar rather
  than setting it.
- `.pane-pick-overlay` / `.pane-pick-zone` / `.pane-pick-scrim` /
  `.pane-pick-hint` / `.pane-pick-keys` — tap-to-pick. Each candidate tile shows
  a centred plus of five 56×44 buttons (grid cells, so they cannot overlap); the
  overlay is `pointer-events: none` so a tap anywhere else reaches the scrim and
  cancels. One overlay serves **three intents** (`drag.picking()` in
  `tiling/drag.ts`): `"move"` relocates the pane it was armed with; `"place"`
  creates a new, empty one where you point (the "New pane…" prompt); `"split"`
  divides the tile you point at, the new pane continuing what that tile showed
  ("Split pane…"). Only the wording, one target, and the number of lit tiles
  differ; the pixels do not.
  - A split withholds the centre — stacking a tab is not a split — which is the
    same four-arm cross a move shows on its own source tile, for an unrelated
    reason (the pane is already a tab there).
  - **The two CREATING intents light only the focused tile; a move lights every
    candidate.** Four identical crosses at once turned "where does this go" into
    "which of these twenty buttons", and creating is something you do where you
    are working: the wireframe is there to name a *direction*, and the focus
    already names the tile. A move's whole subject is a destination somewhere
    else, so for it the other tiles are the answer rather than scenery.
  - When an intent drops a target, drop its key from `.pane-pick-keys` too — and
    the same for `Tab` when only one tile is lit. A named key that does nothing is
    worse than an unnamed one: the reader tries it, nothing happens, and the whole
    line is suspect.
- **The pick overlay's three layering rules**, each learned by getting it wrong:
  the scrim is *below* the overlays (3 vs 7) so a tap reaches a target and not
  the cancel; the hint is therefore a **sibling** of the scrim at z-index 8, not
  its child, because a child cannot climb out of a positioned parent's stacking
  context and inside it the hint is tinted over by the tile beneath; and
  the focused overlay brings its five zones up to solid `--accent` rather than
  wearing the app's own ring, which round the overlay would only retrace the tile's
  existing frame. (It used to vanish there for free — the ring was an
  `--accent-subtle` halo and that is this overlay's backdrop colour — so the
  `box-shadow: none` is now stated rather than inherited; see **Keyboard focus**.)
- `.pane-chooser*` / `.stack.previewed` / `.tab.previewed` — the pane chooser; see
  its own section below.
- `.pane-drop-indicator` — the drop preview; `pointer-events: none`, which is why
  it can be `inset: 0` for the stack zone and the pick zones cannot. The filled
  region shows for a pointer drag *and* a pick, but `.pane-drop-label` is
  suppressed during a pick: label and plus both centre on the same region, so the
  text renders through the middle button, and a pick already has a named button
  under the finger.

**A mode replacing a dialog inherits the dialog's keyboard contract.** A modal is
operable without a pointer by construction — it takes focus, keys drive it, Esc
leaves. An overlay made of buttons is not, and shipping one in a modal's place
quietly trades a dialog anyone can drive for a target only a pointer can reach.
`PickBackdrop` owns that contract for the pick mode:

- **A direction key is the answer, not a way to move towards one.** `↑` places
  the pane above and the mode is over. Aiming-then-confirming would put two steps
  where the pointer has one — and the pointer never had to "select" the top zone
  before pressing it either. Three spellings, because the workspace is
  tmux-shaped: arrows, readline's `Ctrl-P/N/B/F`, and vi's `k/j/h/l`. `Space`
  (and `Enter`) is the centre, "stack here"; on the one tile that has no centre —
  the tile a move came from — it is inert rather than falling through to
  something else.
- **The keyboard's unit of selection is the TILE, not the target.** Focus lives
  on `.pane-pick-overlay` (`tabindex="-1"`, as are the targets), because a ring
  around one button would promise that a confirm commits that button. `Tab`
  chooses the tile and wraps — a trap, since everything behind the scrim is
  covered. It only ever has somewhere to go during a move, since a creating pick
  lights one tile. Which tile is current shows as the whole cross coming up to full
  strength, plus the tile's own `.stack.focused` frame, which comes free:
  focusing the overlay drives the tile's `focusin`.
- **Focus must land somewhere afterwards, and stay there.** A pick's targets
  vanish the instant it commits, so unless the pane that landed claims focus it
  falls to `<body>` and the user is dropped out of the app by their own action.
  Two rules keep it: every commit path in `tiling/drag.ts` sets `focused`
  **before** committing the tree, because a pane grabs focus on mount only if it
  can see that it is the focused one, and committing first mounts it while
  `focused` still names the pane it replaced. And **"did I just mount" is never
  the guard** — a tiled layout rebuilds panes it never touched (a split
  re-parents its sibling), so anything that focuses itself on mount must ask "am
  I the focused pane" instead. `Term` took `autoFocus` for exactly this: an xterm
  focusing itself unconditionally stole the keyboard back from the pane the user
  had just asked for. Cancelling instead hands focus to the opener.
  - `autoFocus` is read **reactively**, not once at mount. A pane can become the
    focused one long after it mounted — the pane chooser below does nothing else —
    and while the answer was sampled at mount, choosing a terminal moved the
    workspace's focus while the keyboard stayed where it was.
  - **`Choose pane…` working is not evidence the other routes do.** Its panel takes
    the keyboard for the duration of the walk, so it gets the keys off whatever you
    were in on its way past — which is a side effect of the mode, not the claim
    doing its job. `Select next pane` and `Select last active pane` have no such
    panel and were both broken while the chooser looked fine. When one route out of
    three works, suspect that route.

**The pane chooser** (`tiling/choose.ts` + `tiling/PaneChooser.tsx`) is tmux's
"choose from a list", and it inverts the usual arrangement: the list is a readout,
and the **arrows walk the workspace**. Classes: `.pane-chooser` (the panel,
anchored to a top corner of `.workspace` at z-index 8, + `.pinned` / `.inert` /
`.left` / `.right`),
`.pane-chooser-scrim` (shares the pick scrim's rule), `.pane-chooser-head` and
`.pane-chooser-pin` (+ `.pinned`), `.pane-chooser-tile` (one group per tile),
`.pane-chooser-list` (the one scrolling part), `.pane-chooser-item` (+ `.selected`),
`.pane-chooser-label`, `.pane-chooser-keys`, the gutter it stands in
(`.workspace.chooser-pinned` / `.chooser-left`), the mini map (`.pane-chooser-map`,
`.pane-chooser-map-tile` + `.current` / `.disabled`, `.pane-chooser-map-view`),
plus `.stack.previewed`, `.tab.previewed`, and the numbering — `.pane-number`
(+ `.current`) with its `.pane-number-tab` and `.pane-number-big`
/ `.pane-number-plate` forms — out on the workspace.

- **Do not make the list the thing being driven.** The panes are already laid out
  in front of the user; asking them to translate "the terminal below this one"
  into "two rows down" is asking them to look up something they can see. The
  arrows step tile to tile (`neighborStack` in `layout.ts`, arithmetic on the
  tree's ratios — never `getBoundingClientRect`, which jsdom cannot answer and
  dividers make approximate), and the list highlight follows.
- **Nothing moves until `Enter`.** The workspace's `focused` is untouched for the
  whole mode, so the focused tile keeps its 1px frame while the previewed one is
  ringed 2px and filled — both on screen at once, which is the state the mode is
  for. Escape discards. That is what makes the walk free: a wrong turn costs
  nothing, so the arrows can be pressed at the speed of looking.
- **The mini map draws the workspace, because the list cannot say WHERE.** A list
  is ordered but not positioned, and once the workspace can be several screens
  wide (the extending layout) "row 5 of 7" stops being an answer to "which pane".
  Opt-in (`paneMiniMap`, off by default) and read at the `<Show>` rather than
  inside the component, so that when it is off nothing mounts and its scroll
  listener does not exist. Off is the default because the map earns its place only
  once the workspace has outgrown the display, and which case a user is in is not
  something the layout can be asked — a three-pane workspace is either, depending
  on the screen.
  One rectangle per tile, placed from `tileRects` — the same arithmetic the arrows
  walk, so the picture cannot disagree with where a direction goes — plus
  `.pane-chooser-map-view`, the frame showing how much of the workspace is on
  screen and where. That frame is the only statement of it anywhere in the app.
  - **Sized in `rem` from JS (`tiling/minimap.ts`), not by `aspect-ratio`.** The
    box must keep the workspace's true proportions while fitting a width budget
    and a height budget at once, and `aspect-ratio` under a binding `max-height`
    drops the ratio — which is the one thing here that carries information. Both
    budgets are met by shrinking: a 6:1 workspace draws as a 6:1 strip.
  - **A cell draws a number per PANE, not per tile, and elides rather than clips.** A tile is
    a stack, so its panes share one place on screen. The numbers WRAP (`flex-wrap: wrap`), so
    a cell's height is room exactly as its width is — `bulletGrid` counts both, and a cell
    that elided while its rows sat empty was throwing away the space the map has to speak
    with. Only past the whole grid does `bulletWindow` replace the rest with an ellipsis,
    sliding to keep the pane the cell stands for visible so a `Tab` walk never moves a number
    off the map. How many fit is ARITHMETIC (`bulletRoom`), because a cell's size is
    `rect * mapBox` and known before anything renders — which holds only while the bullet is sized in rem too. That is
    why `.pane-chooser-map` sets its font in `rem` rather than `--text-xs`: a px bullet in a
    rem box leaves the arithmetic returning the same answers while the glyphs stop matching
    them, and cells overflow instead of eliding. `measure-minimap`'s `spill` check is what
    notices; nothing in vitest can.
  - **Each number is its own click target, and stops the click there.** It is the only way to
    reach a background tab by pointing at it. The `stopPropagation` used to have exactly one
    observable case — a REFUSED number, where the cell underneath would otherwise commit the
    previewed pane and a greyed control would have moved the focus — and that case went when
    refusals left the map. It is kept because a nested target firing its ancestor's handler is
    a coincidence rather than a design, and the coincidence is that the two commits agree.
  - **The frame is measured, not derived from `ext`.** `ext` is the model's
    extent; what the map reports is what the user can actually see, dividers,
    scrollbars and rounding included. Absent when the container has never been
    measured AND when nothing is off screen — two different facts that look
    identical on screen, so `viewportRect` returns `null` for both rather than
    drawing a confident frame around a measurement nobody took.
  - **`aria-hidden`, like `.pane-number-plate`.** Every rectangle is a listbox row
    that is already announced, walkable and labelled with the pane's real name;
    announcing the workspace twice would make the mode harder to use with a screen
    reader, not easier. The click is a pointer affordance over a picture, and the
    keyboard route reaches every pane it draws.
- **PINNED, it stops being a mode** (`paneChooserPinned`, off by default; the resolution
  lives in `tiling/gutter.ts`). The pin in `.pane-chooser-head` stands the panel permanently
  in a gutter: `.workspace` becomes a flex row, the body gives up 22rem, and the tiles are
  laid out in what is left — so the panel covers nothing, which is the whole of what pinning
  buys. `ARMED` — not "is this component mounted" — is then the question almost everything
  in `PaneChooser` turns on.
  - **The scrim, the keyboard, the focus grab, the refusals and the key hint belong to the
    ARMED state alone.** A standing panel that registered the mode's `keydown` would eat
    Escape, Enter, Tab, the digits and `hjkl` from whatever pane the user is typing in; one
    that held the scrim would be a modal that never closes; one that grabbed focus on mount
    would pull the keyboard out of a live shell on every reload. `refuse` is gated the same
    way for a subtler reason: the mode ends by clearing `selected`, NOT by forgetting the
    question, so an ungated read greys rows for a transfer that finished minutes ago.
  - **A click means two different things and the markup cannot tell you which.** Armed it
    commits (and may be refused); idle it is `ctx.activate` — what pointing at a pane means
    everywhere else. For the plain chooser the two paths end in the same call, so only a
    purpose with a `refuse` can distinguish them in a test.
  - **The highlight falls back to the focused pane** (`marked = selected() ?? focused()`).
    Idle, "where am I" is the only thing a list that is not being walked has to say, and it
    is the mark the walk borrows the moment the mode arms — so arming moves the highlight
    rather than making a second kind of mark appear. The map takes the same answer as a prop
    instead of re-deriving it, so the picture and the list cannot disagree.
  - **`.inert` while ANOTHER MODE owns the screen** — a placement, or a walk that belongs to
    the card beside it. The pinned panel is the first thing that can be on screen during someone
    else's mode, and its rows move the focus. `pointer-events: none` rather than a lower
    z-index, so the click reaches that mode's scrim underneath and cancels — which is what
    pressing anywhere but a target already means.
  - **DESKTOP ONLY, gated on the device and not on the setting** (`canPin`: a fine primary
    pointer and a window at least 960px wide, sampled on resize). A 22rem gutter is a fair
    trade on a wide window with a mouse and a bad one anywhere else, and settings sync — so
    the gate is in the reader rather than in the writer, and a blob from a desk is ignored
    on a phone instead of obeyed.
  - **The panel sits at the START of the reading direction** — `startSide` in `tiling/gutter.ts`,
    used both by `auto` and by the FLOATING anchor (`chooserAnchor`), so pinning moves the panel
    outward rather than across the screen. The start because of what the list is OF: the numbers
    are positions in layout order, and layout order begins at the workspace's origin, which is
    the start corner. A named gutter side overrides it; the floating anchor has no setting, since
    `paneChooserSide` is a choice about a permanent strip of screen and a card that lives for one
    keystroke is not that.
    `documentDirection` asks `<html dir>` first (a statement) and the browser's own language
    second (an inference); it does NOT consult `<html lang>` first, because index.html serves
    `lang="en"` unconditionally and "auto" would be a permanent synonym for one side.
    The corner arrives as `.left` / `.right`, NOT as `inset-inline-start`: the logical property
    follows the document's COMPUTED direction, which is a different question — the app never sets
    `dir`, so a browser set to Arabic renders ltr and the floating panel would land opposite the
    gutter the same preference asks for.
  - **A CALLER'S QUESTION DOES NOT GO IN THE GUTTER.** `choose.plain()` is the test (identity
    against the one default purpose, not a title). Pinned, a transfer opens a second
    `ChooserPanel` as a card on the OPPOSITE side and the gutter keeps standing: the gutter is
    furniture, read while working, and putting a modal question in it replaces the thing being
    read with something that must be answered, in the one part of the screen that was supposed to
    stay still. The gutter goes `.inert` for the duration, exactly as under a placement.
    - **The card is a list of destinations and nothing else** (`asking`): no mini map, no focus
      mark, no pin. The map answers "where is that pane", a navigation question, while a transfer
      picks by name, kind and refusal; the focus mark reports a fact no answer can change, since
      a transfer deliberately leaves the focus alone. The refusals themselves stay — they are on
      the rows, with their reasons.
    - **So the map never draws a refusal**, and the code for it is gone: a map is drawn only for
      the mode's own question, and that is the one purpose that refuses nothing.
    - **Row ids name the panel** (`pane-choice-gutter-…` / `pane-choice-card-…`). Two panels can
      list the same panes at once, and duplicate ids make `aria-activedescendant` on the focused
      card resolve to a row in the gutter.
  - **`order: -1` places a left gutter**, not `row-reverse`: the class is a statement about
    one element. `position: relative` on the pinned panel is deliberately redundant today —
    measured, a flex item's `z-index` outranks the scrim whether or not it is positioned —
    and stops being redundant if `.workspace` ever stops being a flex container.
- **`Tab` walks the tabs of the previewed tile.** Stacked tabs share one place on
  screen, so no direction can distinguish them; without this every background tab
  is a row the keyboard can never reach. It is also trapped, like the pick's.
- **The mode carries a PURPOSE, so "which pane?" can be asked for more than one
  reason.** `ChoosePurpose` is `{ title, pick, refuse?, elsewhere? }`: the panel's
  own name (and its `aria-label`), what `Enter` hands the chosen pane to, which
  panes are not answers, and an answer that is not a pane at all. The default is
  `{ title: "Choose a pane", pick: activate }` — an object, not a special case in
  every reader — and `begin()` restores it, so a transfer cannot leave the next
  `prefix s` pointed at a copy. The file transfers' F5 / F6 are the second caller.
  - **`pick` is a callback and not a flag**, because the default purpose moves the
    focus and a transfer must not: it sends files, and taking the user along
    abandons the selection they were working through. The toast reports the result
    from wherever they are.
  - **`refuse` returns a REASON**, like a disabled command's, and the row keeps its
    place in the list wearing it (`.pane-chooser-item.disabled` +
    `.pane-chooser-why`). It is not filtered out: the numbers are positions, so a
    hole would renumber the tiles the walk is named after. `Enter` on one is the
    quiet no-op the greying promises and does NOT end the mode — a wrong turn during
    the walk costs nothing, so being told "not that one" should not cost more.
  - **`begin()` opens on a pane the purpose accepts**, falling forward from the
    focused one to the first unrefused pane. One rule, vacuous for the plain
    chooser; for a transfer, whose source is always where you are standing, it is
    what stops the mode opening on a greyed row.
  - **`elsewhere` is taken by TYPING, and costs the mode `hjkl`.** For a transfer it
    is "an arbitrary location" — a path no pane is showing. Any bare letter leaves
    the list and is passed on as the first character of what the escape asks
    (`caretAtEnd` in `modal.ts` keeps the prompt from selecting it away), so "I want
    somewhere else" and "here is where" are one action. Letters are therefore not
    movement while an escape is offered, and `.pane-chooser-keys` says so — it drops
    `hjkl` and gains `a–z types a path`. Excluding `hjkl` from the escape instead
    would make a destination beginning with h, j, k or l untypeable, which is not a
    rule anyone could guess. The escape renders as `.pane-chooser-other`, a footer
    choice under a rule rather than a row: it has no tile, no number and no place in
    the walk.
- **It does not wrap at a wall**, though tmux does. tmux is moving the focus one
  pane at a time, where a wrap is a shortcut; here a highlight is being watched
  travel, and a `←` at the left wall landing on the far right reads as a bug.
- **Every pane wears its number, and the number is a POSITION.** `numberOf` is the
  1-based index in `allPanes` order, derived on read — closing a pane renumbers what
  follows, and a number stored on a pane would address the wrong row. It appears in
  three places: on each chooser row (read out with the label — "3, project" is how you
  would say it), on each tab, and as a large plate centred in each tile, which is tmux's
  display-panes and the thing that makes a corner row and a tile over there the same
  object. `1`–`9` jump the walk there; past nine there is no key, so the hint names the
  live range (`1–4 jump`) rather than a fixed one.
  - **The numbering is also the walk order.** `prefix o` ("Select next pane",
    `nextPaneId` in `layout.ts`) steps to the next number and wraps, so the digits on the
    tabs are a map of where that key goes next — a walk nobody has to watch. It steps
    through a tile's background TABS as well as between tiles, which is what separates it
    from `prefix C-o` (slots, and the layout moves rather than the focus) and from the
    chooser's arrows (tiles). It is deliberately NOT tagged `pane`, so it is in the palette
    and not in the ⋮: the ⋮ is a pointer surface, where the way to reach a pane is to click
    it, and this is an accelerator through a question "Choose pane…" already asks — the same
    rule that keeps the two directional split binds out of that menu.
  - **Two lifetimes, and the difference is the point.** The tab number is STANDING —
    you aim the digit key with it, so needing the chooser open to read it would make the
    jump useless. The plate and the list belong to the mode: a permanent coin-sized
    number over every pane is not a workspace anyone would keep.
  - **Only the standing copy is switchable** (`settings.paneNumbersInTabs`, on by
    default, under a *Workspace* group because both tiled screens share this chrome).
    The list and the plate are the mode itself — a list of identical rows with nothing
    tying them to the screen is no chooser — so no setting may reach them.
  - **Circled by CSS, never by codepoint.** The Unicode circled digits (U+2460…) stop
    at 20, are missing from many monospace faces (so the browser substitutes a face
    that has them and the digit stops matching the text beside it), cannot be weighted
    or sized against the surrounding type, and cannot take the accent when the row is
    selected. A square box with `border-radius: 50%`, sized in `em` so one rule serves
    the 15px badge and the 38px plate. Not `aspect-ratio`: that is at the mercy of the
    digit's advance width, and a "circle" that is an ellipse on `1` and round on `8` is
    worse than no circle.
  - **The badge must cost the tab bar nothing.** Same constraint the activity badge is
    measured against (see `.tab .badge`), and now permanent: `0.8em` plus
    `vertical-align: middle` keeps a 15px circle inside the bar's 18.59px line box, so
    the bar stays at 27.59px with numbers on or off and no pane's content shifts.
    Measured in a browser, not reasoned about — at the base `--text-xs` it is 28.19px.
  - **The plate carries no ring.** At badge size the border is what makes a circle out
    of a digit; at 38px the fill already is one and a ring reads as a second edge just
    inside the disc's own. The shadow stays — that is what lifts it off the pane.
  - **The filled circle means "the focus is here"** (`.pane-number.current`), which is
    what replaced a separate ● in the list: one glyph for one fact, and it works on the
    tab and the plate where a dot had nowhere to go. It must out-rank the walk's own
    highlight, and cannot on specificity — `.pane-chooser-item.selected .pane-number` is
    (0,3,0) against its (0,2,0) — so the selected rule carries `:not(.current)`.
- **The workspace remembers one pane, not a history** (`tiling/lastpane.ts`,
  `prefix ;` — tmux's last-pane). One, because the key's whole use is alternating
  between two panes without looking; a stack would make the second press mean
  something the first did not. It is DERIVED from `focused` by an effect that keeps
  the previous value, never recorded at the dozen call sites that move the focus —
  a memory updated by hand is wrong the first time someone adds a thirteenth. And
  its gate is neither a pane count nor a tile count but "is there a pane to go back
  to": a reloaded workspace has panes and no memory, and a remembered pane can die
  while the focus sits still (closing a background tab of the tile you are on), so
  the liveness check belongs in the read.
- **Two takeover modes may not be up at once**, and neither knows about the other
  by default. The chooser owns both directions: `begin()` ends a pick, and an
  effect ends the choose when one arms (the prefix key still works under a pick's
  scrim, which is all it takes to ask for the other one).
- **A mode swallows the keys it claims and passes on the rest** — the prefix
  included, or it could not be left except by its own keys.
- **A takeover mode must stand the edge-split gesture down, and disarm what is already
  armed.** The strips are z-index 5 and both modes' scrims are 3, so a scrim does not
  cover them; and the dwell reads *geometry*, not the event target (its reach extends
  into the gutter, which belongs to no tile), so pointer moves over a scrim go on arming
  strips. Standing the dwell down is only half: a strip whose countdown finished before
  the mode opened stays visible and hit-testable, and its own handler asks nothing but
  "am I armed" — so the click meant to answer the mode splits a tile instead. `StackView`
  disarms on entering either mode, which makes the two `modal()` guards belt and braces.

**Menus are filtered palettes, not second lists.** A `Command`
(`command-center.ts`) carries any number of `tags`, and the palette filter reads
`:name` as "must have this tag exactly" — so a menu is a seeded query, not a
parallel copy of the same actions. The tile `⋮` is the worked example: it calls
`openPalette(":pane ")` and that IS the pane menu. Prefer this to a bespoke menu
whenever the entries are things the palette already offers:

- One list, so the two cannot drift. The `⋮` used to hold a modal with its own
  copy of split / new-tab / move / close, and every change had to be made twice.
- Each entry shows its tmux bind, which is how anyone discovers the binds exist.
- A `bind` comes in two spellings and they are matched differently. **Plain**
  (`"%"`, `"c"`, `"C"`) compares `e.key` exactly, requires Ctrl/Alt/Meta absent
  and ignores Shift — the shifted character is already in `e.key`, which is what
  lets `c` and `C` be two commands. **Chord** (`"Ctrl+O"`, for tmux's
  `prefix C-o`) is parsed and matched like the app prefix itself, modifiers and
  all. Adding chords is why the second-key lookup no longer bails on `e.ctrlKey`
  before it looks; what still sends `prefix Ctrl+C` to the browser is simply that
  no command claims it. Both `App.tsx` and `Term.tsx` `preventDefault()` on
  `"swallow"`, so a chord that collides with a browser shortcut (Ctrl+O opens a
  file dialog) is safe — verified in Chromium, not assumed.
- **`bind` may be an ARRAY**, and every spelling is shown (`bindsOf` normalizes the
  two shapes; the palette renders one `<kbd>` each). Uniqueness is per spelling,
  not per command — the lookup takes the first match, so a spelling claimed twice
  silently disables the later claimant, which is what `views.test.tsx` flattens
  through `bindsOf` to check.
- **`direct` is the other kind of key: no prefix at all.** The file transfers' `F5` / `F6` are
  the orthodox file manager's copy and move — keys a whole genre already taught,
  which a prefix in front of them would leave right in every detail except the one
  that makes them a habit. Two rules keep the field from being a free-for-all: it
  must be a key nothing in the page wants to TYPE (a function key; `dispatchAppKey`
  stands aside for text entry regardless), and it takes the key from the BROWSER,
  so claim one only where the command is genuinely the better meaning there.
  - **Do not claim a key the focused thing already owns; vary it instead.**
    `files:open` is the listing's Open, made a command so the palette can name,
    search and run it. It carries no prefix `bind` at all — a prefix is for
    something the focused thing cannot hear, and this listing hears Enter perfectly
    well (same shape and same reason as `files:save`). And its `direct` is
    `Ctrl+Enter`, never plain `Enter`: the lookup runs on a DOCUMENT keydown against
    anything that is not a text field, so claiming the unmodified key takes it from
    every button, link and updir row at once — a test pins plain `Enter` as
    unclaimed for exactly that reason. Inventing a spare function key was the other
    way out and was worse: it teaches a second habit for one action, on a screen
    whose `F5` / `F6` are already spoken for.
  - **Sequencing lives in `dispatchAppKey`, not in `App.tsx`**, which keeps only the
    "who holds the keyboard" exclusions (no palette, no modal, not inside
    `.xterm`). The prefix machine goes first; the direct lookup is then skipped for
    the keystroke that FOLLOWED a prefix, because an unclaimed key there is
    deliberately handed to the browser — that is the whole "pass browser shortcuts"
    path, and a direct bind catching it would take back the shortcut the sequence
    exists to emit.
  - **A disabled direct command still swallows its key**, as a disabled `bind`
    does, and the reasoning is stronger rather than weaker: under `F5` is Reload, so
    falling through would answer "this cannot run just now" by discarding every
    unsaved editor draft in the workspace (`files/drafts.ts` is memory-only). That
    is also why the Workspace registers the two transfers throughout a mount and disables
    them, rather than letting them come and go with the selection — one key must not
    mean "copy" or "reload" depending on something the user is not looking at.
  - A direct cap is drawn differently (`.cmd-item kbd.direct`, accent, plus a
    tooltip). Every cap in that column has meant "after the prefix"; an unmarked one
    would be a wrong instruction, not merely an incomplete one.
  - **Register the platform's spelling, not both.** Open takes `Meta+Enter` on a Mac
    and `Ctrl+Enter` elsewhere, chosen once at module load from `isMacPlatform`.
    Advertising both would name a key that does nothing on the machine reading it.
  - **Check the browser's reserved list before choosing.** `Ctrl+T` / `Cmd+T` — the
    obvious spelling for what was then "open in a new tab" — never reaches the page
    in Chrome or Firefox: it is delivered to the browser chrome, and
    `preventDefault` has no say. No test of `dispatchAppKey` can tell you that; it
    will pass on a key no user can press. `Ctrl+Enter` was chosen instead, which
    arrives and reads as "Enter, but elsewhere" — a modified form of the gesture it
    varies.
  - **A chord does not claim its unmodified key.** `Ctrl+Enter` leaves plain `Enter`
    to the listing, which is that screen's primary gesture. Worth asserting: the
    failure is silent and total.
- The seed is a starting point, not a cage — deleting it widens to everything, and
  the caret is placed past it so typing narrows instead.
- Tag against the exported constant (`PANE_TAG`), never a spelling. The menu is
  assembled by matching, so a typo is an entry that silently vanishes.
- A menu built this way acts on the FOCUSED thing, so whatever opens it must first
  make its own thing focused. Do not leave that to the click: pressing a `<button>`
  does not move focus in every browser, and "close focused pane" hitting the wrong
  tile is not a forgivable defect.
- **Gate an entry by disabling it, not by omitting it.** `Command.disabled` holds
  the REASON and its presence is what disables; the palette greys the row, prints
  the reason where the accelerator would go, and refuses the press without
  closing. A menu that changes shape as the workspace does teaches nothing, and an
  absent entry reads as "this screen cannot do that" rather than "not just now" —
  which is exactly how "Move pane…" was reported missing when it was merely hidden
  at one pane. The field is a string so that a reason is compulsory: if you cannot
  name one, omit the command instead, because a grey row with no explanation only
  moves the question from "where is it" to "why is it like that".
  - Use `aria-disabled`, never the `disabled` attribute: a disabled `<button>`
    takes no mouse events, so hover-select stops working on it, and some readers
    drop it from the accessibility tree — the opposite of the point. (Playwright
    honours `aria-disabled` too, and will refuse to `.click()` such a row.)
  - The selection may still land on it. It is there to be read, so a keyboard user
    must be able to reach it; `Enter` is then a no-op with its reason on screen.
  - A disabled command still OWNS its bind: the key is swallowed and nothing runs.
    Dropping it from the bind lookup instead would let the key fall through as a
    browser shortcut, so `prefix x` would close a browser tab because a pane
    command happened to be unavailable.

**Gate ergonomics, never capability.** Pointer media queries answer for the
*primary* pointer and re-evaluate live, so a touchscreen laptop reports `fine`
while it still cannot hover, and folding a convertible flips the answer
mid-session. Anything that is the ONLY way to reach an operation therefore ships
unconditionally — the `⋮` is on every device — and only comfort (hit-area sizes,
`touch-action`) goes behind `(pointer: coarse)` / `(hover: none)`. Corollary
learned here: **the tab bar's height is not a touch knob.** It stays at its
27.59px on every pointer; reachability is bought with the menu, not with padding
that would cost every screen a row of terminal.

## Brand assets

- **Logo / favicon**: `web/public/cornus-logo.svg`. It is a **copy** of the
  canonical `assets/cornus-logo.svg` — treat `assets/` as the source; if the logo
  changes, re-copy it into `web/public/`. The same file serves as the header mark
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

## Sections, not tabs

A screen puts everything that belongs to it on the page at once, as stacked
`.section` bands. The Overview does this per project; the workload detail page does
it for instances, spec, metrics, and logs. Tabs are what these pages used to be, and
what they were traded for: on the page a reader opens BECAUSE something looks wrong,
the instance that just died and the log line saying why were never visible together.

The exception is a section whose mere presence does something. Rendering is
arrival — there is no click to gate it — so anything with a side effect stays a
control instead. Exec is the standing example: a terminal section would spawn a
shell in the container on page load, so the detail page carries an **Exec** CTA into
the Workspace (`/workspace?workload=<name>`, consumed on arrival) and mounts no
terminal of its own. The Workspace opens as a file browser, so the CTA adds a
terminal TAB beside it rather than retargeting the pane it lands on — a pane
already holding something is not a slot to be reused.

## Extending the system

- **New token** → add under `:root`, add the dark override, then reference it.
- **New component** → add a rule in the Components layer using existing tokens;
  add the class to the consuming `.tsx`. Avoid inline `style` for anything
  themeable.
- **New form field** → just use a native `<input>`/`<select>`/`<textarea>`; the
  shared rule styles it automatically. Wrap label+control in `.field` if you need
  a stacked group.
- **New screen** → compose from `h1` + `.cards`/`table.grid`/`.row`; register the
  route in `web/src/index.tsx` and the header nav link in `App.tsx` (the `NAV`
  array, which also feeds the command palette's "Go to" group). Every
  `table.grid` goes inside a `.table-scroll`.
- **Charts on an existing screen** → mount `MetricPanel` (or `MetricsStrip` for
  the two-panel summary) and drive it from ONE `createMetricsClock` per page. A
  per-panel timer or a per-section range control lets two charts on one screen
  describe two different moments while looking like one view.
- **New chart** → reuse `TimeSeriesChart`; do not hand-roll a second SVG chart.
  Anything it cannot draw is a change to that component, so every chart keeps one
  set of mark specs, one hover layer, and one palette. Two rules it exists to
  enforce: **one value axis** (a second y-scale invents a correlation the data
  does not contain — use a second panel), and **a table view beside every chart**.

## Formatting

`web/` has **no formatter**: Prettier is not a dependency, there is no config, and
there is no `format` script. Match the surrounding code by hand — the tree is wider
than Prettier's default `printWidth: 80`, so `npx prettier --write` on any file here
reformats hundreds of lines of untouched code (~480 in `views.test.tsx` alone) and
buries the actual change. There is no cheap undo either: `git checkout` / `git
restore` against the working tree are forbidden (another agent may be working in it),
so recovery means rebuilding each file from `git show HEAD:<path>` plus the intended
edits, which only works when the file had no other uncommitted work.

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
4. **Measurement pass** — the same headless browser, used for numbers rather than
   pictures. **Reach for this whenever the change is about something the test suite
   cannot see**: motion, geometry, contrast, anything that depends on layout. jsdom
   does none of those, and the CSSOM is empty for `styles.css`, so a vitest
   "Stylesheet" test can only assert that a RULE says what you meant — never that the
   result is visible.

   The gap between those is not theoretical. The activity badge shipped with its
   gradient keyframed on `background-image` (a discrete property: it never animated at
   all) and the pane bullet shipped animating correctly through a luminance range too
   narrow to perceive. Both passed a neutralized rule-text test; both reported the same
   way — "it doesn't animate" — from opposite causes. One 30-line script settled it.

   The technique: load `dev:mock`, inject the exact markup the component emits (so the
   question is only "does the stylesheet do this?"), then sample `getComputedStyle` on
   a timer and reduce to a number — distinct values seen, min/max, a luminance ratio,
   a contrast ratio. Assert the NUMBER, in both colour schemes. Record what you measured
   in the token's or rule's comment, as `--warn-deep` does, so the next person retuning
   it knows which quantity the value is answerable to.

## File map

| File | Role |
|------|------|
| `web/src/styles.css` | The entire design system (tokens + components) |
| `web/src/App.tsx` | Page-header shell + brand lockup + `NAV`, and the single mount point for `ModalHost` / `Toaster` |
| `web/src/toast.ts`, `web/src/views/Toaster.tsx` | Transient-message service + its host |
| `web/src/dnd.ts` | Drag-and-drop facade: one source/target vocabulary over the native and emulated (pointer) transports |
| `web/index.html` | Favicon, `theme-color`, `<title>` |
| `web/public/cornus-logo.svg` | Logo/favicon (copy of `assets/cornus-logo.svg`) |
| `web/src/views/*.tsx`, `web/src/components/*.tsx` | Consumers of the class vocabulary |
| `web/src/components/TimeSeriesChart.tsx` | The only chart primitive; owns the mark specs and the hover/keyboard layer |
| `web/src/views/metrics/MetricPanel.tsx` | One panel: query, empty states, the eight-color cap, the table view. Every metrics surface mounts this |
| `web/src/views/metrics/MetricsStrip.tsx` | The two-panel summary a project/workload section carries |
| `web/src/views/metrics/series.ts` | Series shaping and value formatting, including the palette-slot assignment |
| `web/src/views/metrics/clock.ts` | The one refetch beat per page — never a timer per panel |
| `web/src/mock/metrics.ts` | Generated store answers for `npm run dev:mock` and the component tests |
| `pkg/webui/webui.go` | Embeds the built `dist/` and serves the SPA |
