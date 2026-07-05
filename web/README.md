# cornus web UI

The SolidJS single-page app served by `cornus web`. It is built with Vite into
`../pkg/webui/dist` and embedded into the cornus binary (`//go:embed`), then
served alongside its backend-for-frontend at `/.cornus/web/*`.

The visual design system (tokens, theming, and the component class vocabulary in
`src/styles.css`) is documented in
[../.agents/docs/DESIGN_SYSTEM.md](../.agents/docs/DESIGN_SYSTEM.md) — read it
before restyling the UI or adding a screen.

## Layout

- `src/api.ts` — typed client for the `/.cornus/web/*` BFF.
- `src/views/` — one file per screen (Overview, Workloads, WorkloadDetail,
  Projects, Mounts, Tunnels, Files, Terminal).
- `src/components/` — `Editor` (CodeMirror 6), `Term` (xterm.js), and
  `DependencyGraph` (dagre layout + SVG).
- `src/mock/` — canned BFF fixtures + a `fetch` stub, shared by the component
  tests and the standalone mock server.
- `mock/server.ts` — a zero-backend mock BFF for manual UI development (TypeScript,
  run directly via Node's type-stripping), with `mock/ws.ts` (a tiny dependency-free
  WebSocket server) and `mock/faketerm.ts` (the bogus exec shell, persistent
  terminal sessions, and log stream) behind the exec/logs/terminals panes.

## Develop

Two debug loops, depending on whether you have a running cornus stack.

### A. Against a real `cornus web` (zero-config HMR)

Run the real server + UI, then Vite on its own port — Vite serves the page (so
HMR is native) and proxies `/.cornus` to the running `cornus web`:

```sh
cornus serve &                       # a real cornus server
cornus web --addr 127.0.0.1:5080 &   # the real BFF on a fixed port
CORNUS_WEB_PROXY=http://127.0.0.1:5080 npm run dev   # Vite on :5173, proxying /.cornus
```

Open Vite's URL (http://localhost:5173). Editing a `.tsx` hot-reloads instantly;
BFF calls hit the real `cornus web`.

### B. Detached frontend — one origin, real BFF (`cornus web --frontend`)

The inverse: `cornus web` serves the real BFF and reverse-proxies everything else
(including Vite's HMR WebSocket) to a separately-run dev server. You browse
`cornus web`'s origin and still get hot-reload:

```sh
cornus serve &
npm run dev &                                         # Vite on :5173
cornus web --addr 127.0.0.1:5080 --frontend http://localhost:5173
```

Open http://127.0.0.1:5080. Useful when you want the UI and BFF to share one
origin (matching production) while iterating on the frontend.

### C. No backend at all (mock BFF)

Develop the non-streaming views with no cornus server or Docker — a mock BFF
answers `/.cornus/web/*` from `src/mock/fixtures.ts`:

```sh
npm run dev:mock            # mock BFF on :5080 + Vite proxying to it
# or, separately:
npm run mock &              # mock BFF on :5080
CORNUS_WEB_PROXY=http://127.0.0.1:5080 npm run dev
```

The mock also serves the exec and logs WebSocket panes: the terminal auto-plays a
looping, scripted shell session (a ready-made "interaction loop" for demos and
screenshots) and hands control to a real interactive prompt as soon as you press a
key, while the logs pane streams plausible lines. Only the stats pane still needs a
real `cornus web` (loops A/B).

## Build & test

```sh
npm run build     # tsc --noEmit + vite build -> ../pkg/webui/dist
npm test          # vitest: render the views against the mocked BFF (jsdom)
```

`make web` (from the repo root) runs the production build and is a prerequisite
of `make build`, so `bin/cornus` embeds fresh assets. When `npm` is absent the
`make web` step skips gracefully and the binary serves a "run make web" notice.
