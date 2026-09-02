import {defineConfig} from 'vitest/config'

// dist/ is served by Flask as plain static files, and is also the read-only
// osjs:/ mountpoint. This is now the only build that writes there, so the
// directory is emptied on each one.
export default defineConfig({
  base: '/',
  build: {
    outDir: '../dist',
    emptyOutDir: true,
    sourcemap: true
  },
  server: {
    // `npm run dev` serves the UI but proxies the API to Flask, so the session
    // cookie and the websocket work the same as in a build.
    proxy: {
      '/vfs': 'http://127.0.0.1:8000',
      '/login': 'http://127.0.0.1:8000',
      '/logout': 'http://127.0.0.1:8000',
      '/settings': 'http://127.0.0.1:8000',
      '/ping': 'http://127.0.0.1:8000'
    }
  },
  test: {
    environment: 'happy-dom',
    include: ['tests/**/*.test.ts']
  }
})
