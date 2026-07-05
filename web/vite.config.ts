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
