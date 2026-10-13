import { mount } from 'svelte';
import App from './App.svelte';
import AppHandoffView from './lib/apps/AppHandoffView.svelte';
import { isHandoffHash } from './lib/apps/app-actions';
import { keyboard } from './lib/keyboard/engine.svelte';
import { initClientlog } from './lib/clientlog/clientlog.svelte';
import { initDiagnosticsProvider } from './lib/diagnostics/provider';
import { wrapFetch } from '@herold/clientlog';
import './app.css';

// Install the clientlog wrapper FIRST, before anything else, so a crash
// during JMAP setup or keyboard init is captured (REQ-CLOG-01).
// wrapFetch wraps globalThis.fetch so every JMAP / REST / chat outbound
// request carries X-Request-Id for cross-source correlation (REQ-CLOG-20).
const clientlog = initClientlog();
globalThis.fetch = wrapFetch(globalThis.fetch.bind(globalThis));

// Install the webapp-diagnostics/1 provider so an external bug-reporter
// browser extension can pull app state (route, auth, debug ring) into a
// bug report. Same-origin postMessage handshake only; see lib/diagnostics.
initDiagnosticsProvider();

// Expose a test-only throw shim so the e2e spec can force a runtime error
// without reaching into internals. Guarded by import.meta.env.DEV so it is
// dead-code-eliminated in production bundles (REQ-CLOG-13).
if (import.meta.env.DEV) {
  (globalThis as unknown as Record<string, unknown>)['__TEST_THROW__'] = () => {
    throw new Error('clientlog-e2e-test-error');
  };
}

const target = document.getElementById('app');
if (!target) {
  void clientlog.logFatal(new Error('#app element missing from index.html'), {
    synchronous: true,
  });
  throw new Error('#app element missing from index.html');
}

// The tab an app action opens renders only the handoff placeholder
// (REQ-APPS-10): no session bootstrap, no shell, no keyboard engine.
const app = isHandoffHash(window.location.hash)
  ? mount(AppHandoffView, { target, props: { hash: window.location.hash } })
  : mountSuite(target);

function mountSuite(el: HTMLElement): ReturnType<typeof mount> {
  // One global keydown listener for the whole suite shell.
  keyboard.init();
  return mount(App, { target: el });
}

export default app;
