import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The built UI is embedded into the Go binary from web/dist. During dev, API
// calls proxy to the local kubeside server on :7654.
export default defineConfig({
  // Two test runners live in this package and must not sweep each other up:
  // vitest owns the unit tests beside the source, Playwright owns e2e/. The
  // default vitest glob matches *.spec.ts anywhere, which is exactly the
  // Playwright naming convention.
  test: {
    // .mjs beside the source too: a test that reads a file off disk needs node
    // APIs, which the app's tsconfig does not carry. Leaving the pattern out
    // does not fail, it silently collects nothing.
    include: ["src/**/*.test.ts", "src/**/*.test.mjs", "site/**/*.test.mjs"],
    exclude: ["e2e/**", "node_modules/**", "dist/**"],
  },
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, assetsDir: "assets" },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:7654",
      "/healthz": "http://127.0.0.1:7654",
    },
  },
});
