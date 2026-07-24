import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The built UI is embedded into the Go binary from web/dist. During dev, API
// calls proxy to the local kubeside server on :7654.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, assetsDir: "assets" },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:7654",
      "/healthz": "http://127.0.0.1:7654",
    },
  },
});
