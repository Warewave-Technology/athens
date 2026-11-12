/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";

// dist/ is embedded by Go (web/embed.go); the placeholder keeps `go build`
// working on a fresh checkout before the first web build.
const keepPlaceholder = {
  name: "athens-keep-placeholder",
  closeBundle() {
    writeFileSync("dist/.gitkeep", "");
  },
};

// In development the Go backend (--dev) serves on :8080 and proxies every
// non-API path to this server, so the browser only ever talks to one origin.
export default defineConfig({
  plugins: [react(), keepPlaceholder],
  server: { port: 5173, strictPort: true, hmr: { clientPort: 8080 } },
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 2000 },
  test: { environment: "jsdom" },
});
