# Cornus documentation site

This directory holds the [VitePress](https://vitepress.dev/) source for the Cornus
documentation site published at <https://cornus.dev/>.

## Local preview

You need Node.js 18+ and npm.

```sh
cd docs
npm install
npm run docs:dev      # local dev server with hot reload
npm run docs:build    # production build into .vitepress/dist (also the link check)
npm run docs:preview  # serve the production build locally
```

`npm run docs:build` fails on dead internal links, so a clean build is the primary
correctness signal after editing cross-page links.

## Layout

- `index.md` — home page (hero + feature cards).
- `introduction/` — narrative introduction, install, quick start.
- `guides/` — one page per feature: a **How it works** section explaining the model,
  then the copy-paste recipes for it (building, deploying, networking and conduits,
  the hub, tunnels, ingress, egress, credentials, registry, security, ...).
- `cookbook/` — end-to-end scenario walkthroughs that combine several features.
- `cli/` — one page per CLI command group; `cli/index.md` covers global flags.
- `reference/` — deploy spec, connection config, server env vars, storage/deploy backends.
- `public/topics/*.html` — meta-refresh stubs preserving the retired `/topics/*` URLs.
- `architecture/` — reader-facing architecture section (overview + one page per
  subsystem), adapted from the canonical root [`ARCHITECTURE.md`](../ARCHITECTURE.md).
- `.vitepress/config.mts` — site config: nav, sidebar, local search, `base: '/cornus/'`.

## Deployment

`.github/workflows/docs.yml` builds the site and deploys it to GitHub Pages on every
push to `main` that touches `docs/**`. Enabling Pages (repo Settings → Pages → Source:
GitHub Actions) is a one-time repository setting.
