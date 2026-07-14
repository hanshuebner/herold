import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const heroldURL = process.env.HEROLD_URL ?? 'http://localhost:8080';

// scripts/build-web.sh stamps public/sw.js's `__SW_BUILD__` placeholder into
// internal/webspa/dist/suite/sw.js: the short GITHUB_SHA in CI/production, or
// the literal "dev" fallback for a local `make build-web`. The Vite dev
// server serves public/ verbatim, so without this plugin the dev-server
// bytes and the compiled-binary bytes differ for the identical source tree.
// The SW update lifecycle is a byte comparison of sw.js, so that divergence
// alone -- with no real deploy -- fires a spurious "new version available"
// banner whenever a browser profile sees the same origin served first by
// one path and then by the other (re #232). Stamping the dev server
// identically closes the gap while still changing the served bytes on a
// genuine sw.js content edit or a real GITHUB_SHA change.
const SW_BUILD = process.env.GITHUB_SHA?.slice(0, 7) ?? 'dev';

function swBuildStampPlugin(): Plugin {
  return {
    name: 'herold-sw-build-stamp',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.url !== '/sw.js') {
          next();
          return;
        }
        const raw = readFileSync(
          fileURLToPath(new URL('./public/sw.js', import.meta.url)),
          'utf8',
        );
        res.setHeader('Content-Type', 'application/javascript; charset=utf-8');
        res.end(raw.replace('__SW_BUILD__', SW_BUILD));
      });
    },
  };
}

// Read the suite's own package.json to surface the version in the
// settings panel's About section.
const pkg = JSON.parse(
  readFileSync(fileURLToPath(new URL('./package.json', import.meta.url)), 'utf8'),
) as { version: string };

const sha = process.env.GITHUB_SHA?.slice(0, 7);
const versionString = sha ? `${pkg.version} (${sha})` : pkg.version;

// Proxy paths that must reach the herold backend during development.
// The browser sees the suite SPA at localhost:5173; the proxy makes
// herold appear at the same origin so cookies attach to JMAP /
// chat-WS / login requests.
//
// Same-origin deployment is the production posture
// (`docs/design/web/00-scope.md` defaults,
// `docs/design/web/architecture/01-system-overview.md` § Bootstrap).
// The dev proxy emulates that.
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
  plugins: [svelte(), swBuildStampPlugin()],
  define: {
    __HEROLD_VERSION__: JSON.stringify(versionString),
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
