#!/usr/bin/env node
/**
 * scripts/dev.mjs - standalone manual dev server with live reload.
 *
 * Builds the manual to standalone SSR HTML (via bundle.mjs), serves it
 * on localhost, watches the .mdoc sources and manifest.toml, rebuilds
 * on every change, and pushes a reload event to connected browsers
 * over Server-Sent Events. Pure Node: no vite, no pnpm/SPA build, no
 * herold binary -- just the manual.
 *
 * Usage:
 *   node scripts/dev.mjs                 # http://localhost:8000/
 *   MANUAL_PORT=9000 node scripts/dev.mjs
 *
 * Prerequisite: the manual package's dependencies must be installed
 * once (`pnpm install` at the repo root); bundle.mjs needs Markdoc.
 */

import { spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { readFileSync, existsSync, watch } from 'node:fs';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(scriptDir, '..'); // web/packages/manual
const repoRoot = resolve(pkgDir, '../../..'); // repo root
const manifest = join(repoRoot, 'docs', 'manual', 'manifest.toml');
const contentRoot = join(repoRoot, 'docs', 'manual');
const bundleScript = join(scriptDir, 'bundle.mjs');
const outDir = join(pkgDir, 'dist-dev');
const ssrDir = join(outDir, 'ssr');
const jsonDir = join(outDir, 'json');
const port = Number(process.env.MANUAL_PORT || 8000);

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.json': 'application/json; charset=utf-8',
};

// Injected into every served HTML page; reloads the tab when the
// watcher reports a successful rebuild.
const RELOAD_SNIPPET =
  '<script>new EventSource("/__manual_reload").onmessage=function(){location.reload()}</script>';

/** Re-run the SSR bundler. Returns true on success. */
function build() {
  const started = Date.now();
  const r = spawnSync(
    process.execPath,
    [
      bundleScript,
      '--manifest', manifest,
      '--content-root', contentRoot,
      '--out-json', jsonDir,
      '--out-ssr', ssrDir,
      '--ssr',
    ],
    { encoding: 'utf8' },
  );
  if (r.status !== 0) {
    process.stderr.write(
      `[manual-dev] build FAILED:\n${(r.stderr || r.stdout || '').trim()}\n` +
        '[manual-dev] keeping the last good build; fix the error and save again.\n',
    );
    return false;
  }
  process.stdout.write(`[manual-dev] built in ${Date.now() - started}ms\n`);
  return true;
}

/**
 * Map a request path to a file inside ssrDir. The SSR pages link with
 * absolute /manual/... and /manual.(css|js) URLs, so the mapping is
 * not a plain static mount.
 */
function resolvePath(urlPath) {
  if (urlPath === '/manual.css' || urlPath === '/manual.js') {
    return join(ssrDir, urlPath.slice(1));
  }
  if (urlPath === '/manual' || urlPath === '/manual/') {
    return join(ssrDir, 'manual', 'index.html');
  }
  if (urlPath.startsWith('/manual/')) {
    let rel = urlPath.slice('/manual/'.length);
    if (rel === '' || rel.endsWith('/')) rel += 'index.html';
    const full = join(ssrDir, rel);
    // Path-traversal guard: the resolved file must stay under ssrDir.
    if (full !== ssrDir && !full.startsWith(ssrDir + '/')) return null;
    return full;
  }
  return null;
}

const reloadClients = new Set();

const server = createServer((req, res) => {
  const urlPath = decodeURIComponent((req.url || '/').split('?')[0]);

  if (urlPath === '/__manual_reload') {
    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    });
    res.write('retry: 1000\n\n');
    reloadClients.add(res);
    req.on('close', () => reloadClients.delete(res));
    return;
  }

  if (urlPath === '/') {
    res.writeHead(302, { Location: '/manual/' });
    res.end();
    return;
  }

  const file = resolvePath(urlPath);
  if (!file || !existsSync(file)) {
    res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
    res.end(`not found: ${urlPath}\n`);
    return;
  }

  const ext = file.slice(file.lastIndexOf('.'));
  let body;
  try {
    body = readFileSync(file);
  } catch {
    res.writeHead(500, { 'Content-Type': 'text/plain; charset=utf-8' });
    res.end('read error\n');
    return;
  }
  if (ext === '.html') {
    const html = body.toString('utf8');
    body = Buffer.from(
      html.includes('</body>')
        ? html.replace('</body>', `${RELOAD_SNIPPET}</body>`)
        : html + RELOAD_SNIPPET,
    );
  }
  res.writeHead(200, { 'Content-Type': MIME[ext] || 'application/octet-stream' });
  res.end(body);
});

let debounce = null;
function scheduleRebuild() {
  clearTimeout(debounce);
  debounce = setTimeout(() => {
    if (build()) {
      for (const client of reloadClients) {
        try {
          client.write('data: reload\n\n');
        } catch {
          reloadClients.delete(client);
        }
      }
    }
  }, 120);
}

// Initial build must succeed -- otherwise there is nothing to serve.
if (!build()) {
  process.stderr.write('[manual-dev] initial build failed; not starting the server.\n');
  process.exit(1);
}

watch(contentRoot, { recursive: true }, (_event, filename) => {
  if (!filename) return;
  const f = String(filename).replaceAll('\\', '/');
  if (f.endsWith('.mdoc') || f.endsWith('manifest.toml')) {
    scheduleRebuild();
  }
});

server.listen(port, () => {
  process.stdout.write(
    `[manual-dev] serving http://localhost:${port}/  ` +
      '(watching docs/manual for .mdoc changes; Ctrl-C to stop)\n',
  );
});
