import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const heroldURL = process.env.HEROLD_URL ?? 'http://localhost:8080';

// Read the suite's own package.json to surface the version in the
// settings panel's About section.
const pkg = JSON.parse(
  readFileSync(fileURLToPath(new URL('./package.json', import.meta.url)), 'utf8'),
) as { version: string };

const sha = process.env.GITHUB_SHA?.slice(0, 7);
const versionString = sha ? `${pkg.version} (${sha})` : pkg.version;

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
  define: {
    __TABARD_VERSION__: JSON.stringify(versionString),
  },
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
