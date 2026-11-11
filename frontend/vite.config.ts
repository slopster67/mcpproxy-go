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
    // The backend derives the dashboard's public origin (OIDC redirect URI and
    // the Origin it accepts on writes) from the request's Host, so the proxy
    // keeps the browser's Host (localhost:3000) instead of rewriting it.
    proxy: {
      "/oauth/callback": { target: backendURL, changeOrigin: false },
      "/auth": { target: backendURL, changeOrigin: false },
      "/server-icons": { target: backendURL, changeOrigin: false },
      // Includes the /api/v1/events change stream (SSE streams through the
      // plain HTTP proxy).
      "/api": {
        target: backendURL,
        changeOrigin: false,
      },
    },
  },
});
