import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath } from 'node:url'

// The console is served by the Go binary under /admin/. In development, Vite
// serves the app and proxies the API to a running `serve --admin` instance.
const api = process.env.SMCP_ADMIN_API ?? 'http://127.0.0.1:18090'

export default defineConfig({
  base: '/admin/',
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../../x/admin/ui/dist/app',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '^/admin/(graphql|login|logout|session)$': { target: api, changeOrigin: false },
    },
  },
  test: {
    environment: 'jsdom',
  },
})
