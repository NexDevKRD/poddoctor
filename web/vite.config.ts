import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'

// Builds straight into internal/webui/dist so it's embeddable via go:embed
// by both cmd/main.go (per-cluster dashboard) and cmd/hub (fleet hub) —
// same SPA, different API responses.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: path.resolve(__dirname, '../internal/webui/dist'),
    emptyOutDir: true,
  },
})
