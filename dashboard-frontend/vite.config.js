import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [
    svelte({
      compilerOptions: {
        runes: true,
      },
    }),
  ],
  resolve: {
    conditions: ['browser'],
  },
  build: {
    outDir: '../internal/dashboard/static',
    emptyOutDir: true,
    target: 'es2020',
    rollupOptions: {
      input: 'index.html',
    },
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8090',
      '/ws': { target: 'ws://localhost:8090', ws: true },
    }
  }
})