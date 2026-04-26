import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

const heroldURL = process.env.HEROLD_URL ?? 'http://localhost:8080';

// Proxy paths that must reach herold during development. The browser sees
// tabard at localhost:5173; the proxy makes herold appear at the same
// origin so cookies attach to JMAP / chat-WS / login requests.
//
// Same-origin deployment is the production posture (`docs/00-scope.md` defaults,
// `docs/architecture/01-system-overview.md` § Bootstrap). The dev proxy
// emulates that.
const heroldPaths = [
  '/.well-known/jmap',
  '/jmap',
  '/jmap/eventsource',
  '/jmap/upload',
  '/jmap/download',
  '/login',
  '/logout',
  '/auth',
  '/proxy',
  '/api',
];

const proxy = Object.fromEntries(
  heroldPaths.map((path) => [
    path,
    {
      target: heroldURL,
      changeOrigin: false,
      ws: false,
    },
  ]),
);

// Chat WebSocket needs ws: true for the upgrade handshake.
proxy['/chat/ws'] = {
  target: heroldURL,
  changeOrigin: false,
  ws: true,
};

export default defineConfig({
  plugins: [svelte()],
  server: {
    port: 5173,
    strictPort: true,
    proxy,
  },
  build: {
    target: 'es2022',
    sourcemap: true,
  },
});
