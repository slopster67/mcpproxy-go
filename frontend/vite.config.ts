import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { dashboardSession } from "./dev-session.ts";
import { fileURLToPath } from "node:url";

const backendURL = "http://127.0.0.1:8085";

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [dashboardSession(backendURL), vue()],
  base: "/ui/",
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: undefined,
      },
    },
  },
  server: {
    port: 3000,
    proxy: {
      "/oauth/callback": { target: backendURL, changeOrigin: true },
      "/auth": { target: backendURL, changeOrigin: true },
      "/server-icons": { target: backendURL, changeOrigin: true },
      // Includes the /api/v1/events change stream (SSE streams through the
      // plain HTTP proxy).
      "/api": {
        target: backendURL,
        changeOrigin: true,
      },
    },
  },
});
