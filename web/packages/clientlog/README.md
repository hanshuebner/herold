# @herold/clientlog

SPA client-log wrapper. Captures errors, console output, and Web Vitals from
the Suite and Admin SPAs and batches them to herold's clientlog ingest endpoint.

## Size

The package itself (excluding `web-vitals`) bundles to approximately 4-5 KB
gzipped when tree-shaken by Vite as part of a consuming application.
`web-vitals` adds ~3 KB gzipped (bundled inline, no CDN).

## Requirements

REQ-CLOG-01 through REQ-CLOG-22. Server-side counterparts: REQ-OPS-200..216.

## Usage

```ts
import { install } from '@herold/clientlog';

const clientlog = install({
  app: 'suite',
  endpoints: {
    authenticated: '/api/v1/clientlog',
    anonymous:     '/api/v1/clientlog/public',
  },
  isAuthenticated: () => jmapStore.isAuthenticated(),
  livetailUntil:   () => jmapStore.sessionDescriptor?.livetail_until ?? null,
  telemetryEnabled: () => settingsStore.telemetryEnabled,
});
```

Call `install()` once, early in the SPA bootstrap (before the JMAP client is
constructed). The wrapper installs `window.onerror`, `unhandledrejection`, and
console proxy handlers automatically.

When `bootstrap.enabled === false` (operator kill switch), `install()` returns
a no-op stub that installs no handlers and emits nothing.

## Correlation

Wrap the JMAP / admin REST fetch with `wrapFetch` to attach `X-Request-Id` and
record in-flight request context:

```ts
import { wrapFetch } from '@herold/clientlog';
const jmapFetch = wrapFetch(globalThis.fetch.bind(globalThis));
```

## Route breadcrumbs

Call `recordRoute(path)` from the router on each navigation:

```ts
import { recordRoute } from '@herold/clientlog';
recordRoute(newPath);
```
