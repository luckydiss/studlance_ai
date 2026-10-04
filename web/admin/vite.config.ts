import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Admin SPA builds into internal/web/dist/admin; served under /admin.
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: {
    outDir: resolve(__dirname, "../../internal/web/dist/admin"),
    emptyOutDir: true,
  },
  server: {
    port: 5174,
    proxy: {
      "/api": "http://127.0.0.1:8080",
    },
  },
});
