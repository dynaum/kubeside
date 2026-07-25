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
    include: ["src/**/*.test.ts"],
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
