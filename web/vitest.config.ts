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
    setupFiles: ["src/test-setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
