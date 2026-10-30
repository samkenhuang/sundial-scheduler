import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The production build is embedded by Go from internal/web/dist. During local
// development, Vite serves the same base path and proxies the API.
export default defineConfig({
  base: "/ui/",
  plugins: [react()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/v1": "http://127.0.0.1:8080",
    },
  },
});
