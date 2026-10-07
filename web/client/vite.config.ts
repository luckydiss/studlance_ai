import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Client SPA builds into internal/web/dist/client and is served by the Go
// server via go:embed. The dev proxy keeps Host unchanged so the server's
// Origin check passes for same-origin dev requests; SSE streams through.
export default defineConfig({
  base: "/",
  plugins: [react()],
  build: {
    outDir: resolve(__dirname, "../../internal/web/dist/client"),
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/demo": { target: "http://127.0.0.1:8080", changeOrigin: false },
    },
  },
});
