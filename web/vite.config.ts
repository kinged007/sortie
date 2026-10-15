import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  base: "/ui/",
  publicDir: false,
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/server/ui/dist",
    emptyOutDir: true,
    sourcemap: false,
    assetsDir: "assets",
    rolldownOptions: {
      output: {
        // Libraries change less often than the app, so an upgrade that keeps
        // their versions leaves these chunks cached in the browser.
        codeSplitting: {
          groups: [
            {
              name: "react",
              test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/,
              priority: 40,
            },
            { name: "router", test: /node_modules[\\/]react-router[\\/]/, priority: 30 },
            { name: "ui", test: /node_modules[\\/](@base-ui|@floating-ui)[\\/]/, priority: 20 },
            { name: "vendor", test: /node_modules[\\/]/, priority: 10 },
          ],
        },
      },
    },
  },
});
