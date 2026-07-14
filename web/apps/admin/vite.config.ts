import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// scripts/build-web.sh stamps public/sw.js's `__SW_BUILD__` placeholder into
// internal/webspa/dist/admin/sw.js: the short GITHUB_SHA in CI/production, or
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
        // The public dir is served relative to `base` in dev (Vite serves
        // public/sw.js at /admin/sw.js here, matching how the compiled
        // binary mounts the admin SPA at /admin/).
        const swPath = `${server.config.base}sw.js`;
        if (req.url !== swPath) {
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

// HEROLD_API_BASE overrides the proxy target for /api/v1/* specifically.
// Used by the Playwright e2e suite to point the proxy at a stub server.
// Falls back to HEROLD_URL (the full herold backend URL) if not set.
const heroldURL = process.env.HEROLD_URL ?? 'http://localhost:8080';
const apiBase = process.env.HEROLD_API_BASE ?? heroldURL;

// Read the admin package.json to surface the version at runtime.
const pkg = JSON.parse(
  readFileSync(fileURLToPath(new URL('./package.json', import.meta.url)), 'utf8'),
) as { version: string };

const sha = process.env.GITHUB_SHA?.slice(0, 7);
const versionString = sha ? `${pkg.version} (${sha})` : pkg.version;

// Proxy paths that must reach the herold admin backend during development.
// The browser sees the admin SPA at localhost:5174; the proxy makes the
// herold admin listener appear at the same origin so cookies attach to
// /api/v1/* and login requests.
//
// Same-origin deployment is the production posture
// (docs/design/web/00-scope.md). The dev proxy emulates that.
// Proxy /api/v1/* to apiBase (stub server in tests, real backend in dev).
// Other paths (/login, /logout, /oidc) proxy to the full herold backend.
const proxy: Record<string, { target: string; changeOrigin: boolean; ws: boolean }> = {
  '/api/v1': {
    target: apiBase,
    changeOrigin: false,
    ws: false,
  },
  '/api': {
    target: heroldURL,
    changeOrigin: false,
    ws: false,
  },
  '/login': {
    target: heroldURL,
    changeOrigin: false,
    ws: false,
  },
  '/logout': {
    target: heroldURL,
    changeOrigin: false,
    ws: false,
  },
  '/oidc': {
    target: heroldURL,
    changeOrigin: false,
    ws: false,
  },
};

export default defineConfig({
  plugins: [svelte(), swBuildStampPlugin()],
  base: '/admin/',
  define: {
    __HEROLD_VERSION__: JSON.stringify(versionString),
  },
  server: {
    port: 5174,
    strictPort: true,
    proxy,
  },
  build: {
    target: 'es2022',
    sourcemap: true,
  },
});
