/**
 * Admin SPA entry point.
 *
 * clientlog.install() is called FIRST, before any SPA framework code, so
 * crashes during bootstrap (including Svelte mount failures) are captured.
 *
 * REQ-CLOG-01, REQ-OPS-200, docs/design/web/architecture/08-clientlog.md.
 */

import { install, wrapFetch } from '@herold/clientlog';
import { mount } from 'svelte';
import App from './App.svelte';
import { auth } from './lib/auth/auth.svelte';
import { keyboard } from './lib/keyboard/engine.svelte';
import './app.css';

// ---------------------------------------------------------------------------
// 1. Install the clientlog wrapper before any other SPA code.
// ---------------------------------------------------------------------------

// Build SHA from the <meta name="herold-build"> tag injected by internal/webspa.
function readMeta(name: string): string {
  try {
    return (
      document.querySelector<HTMLMetaElement>(`meta[name="${name}"]`)?.content ?? ''
    );
  } catch {
    return '';
  }
}

// Predicates read from the auth singleton at flush time. The auth singleton
// is imported here; clientlog reads from it via closures so there is no
// circular dependency — auth.svelte.ts does not import from main.ts.
//
// telemetry_enabled and livetail_until come from the admin session response
// (task #7 extended GET /api/v1/server/status with a clientlog block). When
// the server-side extension lands the auth module will expose them; until
// then we fall back to the safe defaults: telemetry on, no live-tail.
const clientlogInstance = install({
  app: 'admin',
  buildSha: readMeta('herold-build'),
  endpoints: {
    authenticated: '/api/v1/clientlog',
    anonymous: '/api/v1/clientlog/public',
  },
  isAuthenticated: () => auth.status === 'ready',
  // livetailUntil and telemetryEnabled will be wired to the session
  // descriptor once the server-side clientlog block ships (task #7).
  // Until then: no live-tail, telemetry always enabled.
  livetailUntil: () => auth.clientlogLivetailUntil,
  telemetryEnabled: () => auth.clientlogTelemetryEnabled,
});

// ---------------------------------------------------------------------------
// 2. Wrap the global fetch so every admin REST call carries X-Request-Id.
//    This enables request_id correlation (REQ-OPS-213).
// ---------------------------------------------------------------------------
globalThis.fetch = wrapFetch(globalThis.fetch.bind(globalThis));

// ---------------------------------------------------------------------------
// 3. Mount the Svelte application.
// ---------------------------------------------------------------------------

const target = document.getElementById('app');
if (!target) {
  // Fatal: the page DOM is wrong. Report synchronously before giving up.
  void clientlogInstance.logFatal(new Error('#app element missing from index.html'), {
    synchronous: true,
  });
  throw new Error('#app element missing from index.html');
}

// One global keydown listener for the whole admin shell.
keyboard.init();

const app = mount(App, { target });

export default app;
