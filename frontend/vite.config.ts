import { defineConfig } from "vite";

// Trace-propagation demo frontend. All runtime configuration is supplied via
// build-time VITE_* env vars (see .env.example) so the same static bundle
// can be re-pointed at different backend/collector/Grafana hosts per
// environment without a rebuild-from-source of application logic.
export default defineConfig({
  server: {
    port: 5173,
  },
  preview: {
    port: 5173,
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
