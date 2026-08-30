import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const resolve = (p: string) => fileURLToPath(new URL(p, import.meta.url));

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      // Plugin UI may import this and its own directory. Nothing else.
      "@cc/ui": resolve("./src/ui/index.ts"),
    },
  },
  server: {
    port: 5173,
    // The dev server proxies to the Go process so cookies, CSRF, and SSE behave exactly
    // as they do in production.
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
