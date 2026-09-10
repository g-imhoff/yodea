import path from "path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"
import { viteSingleFile } from "vite-plugin-singlefile"

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss(), viteSingleFile()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    // yodead embeds only web/dist/index.html (see
    // internal/server/assets.go): inline every JS/CSS/font asset so the
    // dashboard is one self-contained file. The central host serves
    // index.html for /login and / but has no static-asset route, so a
    // multi-file bundle would 404 its chunks.
    assetsInlineLimit: 100 * 1024 * 1024,
    chunkSizeWarningLimit: 2000,
  },
})
