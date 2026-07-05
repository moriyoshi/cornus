import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

// The production build emits into pkg/webui/dist, the //go:embed root of the
// cornus binary. `npm run dev` proxies the BFF to a locally running
// `cornus web` instance (CORNUS_WEB_PROXY overrides the default target).
export default defineConfig({
  plugins: [solid()],
  build: {
    outDir: "../pkg/webui/dist",
    emptyOutDir: true,
    // Never inline a font, whatever its size. The terminal font faces are declared for all
    // five bundled families up front but fetched only when a family is actually selected
    // (see src/components/termFont.ts) — and an inlined face breaks exactly that: base64 in
    // the stylesheet is downloaded by everyone, on the render-blocking path, including the
    // majority who stay on the browser's own monospace. Two of Fira Code's subsets are under
    // the 4 kB default and were being inlined for no gain.
    assetsInlineLimit: (filePath) => (filePath.endsWith(".woff2") ? false : undefined),
  },
  server: {
    proxy: {
      "/.cornus": {
        target: process.env.CORNUS_WEB_PROXY || "http://127.0.0.1:5080",
        ws: true,
      },
    },
  },
});
