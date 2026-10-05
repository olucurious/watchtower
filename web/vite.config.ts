import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// The build lands in internal/ui/dist, which the Go binary embeds.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(__dirname, "./src") } },
  build: { outDir: "../internal/ui/dist", emptyOutDir: true, chunkSizeWarningLimit: 700 },
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
})
