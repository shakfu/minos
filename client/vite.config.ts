import {defineConfig} from 'vitest/config'

// dist/ is shared with the OS.js reference build, which writes flat files at
// the root plus apps/, themes/, icons/ and fonts/. Vite keeps to assets/, so
// the two do not collide -- but emptying the directory would wipe the other
// build, and the manifest and packages along with it.
export default defineConfig({
  base: '/',
  build: {
    outDir: '../dist',
    emptyOutDir: false,
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
