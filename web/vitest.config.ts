import { defineConfig } from "vitest/config";
import solid from "vite-plugin-solid";

// Component tests render Solid views in jsdom. The solid plugin must transform
// with the browser/development conditions so reactivity works under the test
// runtime (@solidjs/testing-library).
export default defineConfig({
  plugins: [solid()],
  resolve: { conditions: ["development", "browser"] },
  test: {
    environment: "jsdom",
    globals: true,
    // CSS imports are stubbed with an empty module by default — including
    // `styles.css?raw`, which one test reads to assert a rule the DOM cannot
    // show (jsdom does no layout). Scoped to that one file so every other
    // stylesheet stays stubbed and no test starts depending on real styles.
    css: { include: [/styles\.css/] },
    setupFiles: ["src/test-setup.ts"],
    // `mock/` is the standalone dev server, which is written for Node's type stripping and
    // has no @types/node — tsconfig deliberately includes only `src`, so a test importing
    // it from there would drag the whole untyped tree into the type-check gate. Its tests
    // therefore live beside it, on its own side of that boundary; vitest transpiles rather
    // than type-checks, so they run either way.
    include: ["src/**/*.test.{ts,tsx}", "mock/**/*.test.ts"],
  },
});
