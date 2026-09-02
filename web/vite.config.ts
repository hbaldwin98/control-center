/// <reference types="vitest/config" />
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
  test: {
    environment: "happy-dom",
    coverage: {
      provider: "v8",
      // Cobertura is what the CRAP analyzer reads; text is for a person at the
      // terminal. The HTML report would be a third copy of the same numbers.
      reporter: ["text-summary", "cobertura"],
      reportsDirectory: "coverage",
      include: ["src/**/*.{ts,tsx}"],
      // Only the entry point is excluded: it has no behaviour of its own. The
      // "types" modules are not type-only -- they carry real functions like
      // pulseOf and verdictOf -- so they are measured with everything else.
      exclude: ["src/main.tsx", "src/**/*.test.{ts,tsx}"],
    },
  },
});
