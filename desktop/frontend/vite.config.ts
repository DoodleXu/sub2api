import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

// This build is intentionally independent from frontend/vite.config.ts. The
// relative base works with Wails' asset protocol and also keeps a static build
// previewable from the filesystem.
export default defineConfig({
  base: './',
  plugins: [
    vue(),
    {
      name: 'preserve-dist-marker',
      closeBundle() {
        const distDir = resolve(__dirname, 'dist')
        mkdirSync(distDir, { recursive: true })
        writeFileSync(resolve(distDir, '.gitkeep'), '')
      },
    },
  ],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
    },
  },
  build: {
    outDir: 'dist',
    // The plugin above restores the tracked marker after Vite clears this
    // directory. Keeping cleanup enabled prevents stale hashed assets from
    // being embedded by a subsequent Wails build.
    emptyOutDir: true,
    target: 'es2020',
  },
  server: {
    port: 34115,
    strictPort: true,
  },
})
