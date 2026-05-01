/**
 * @herold/clientlog -- SPA client-log wrapper.
 *
 * Public API:
 *   install(cfg: ClientlogConfig): Clientlog
 *
 * install() is called exactly once, very early in the SPA bootstrap --
 * before the JMAP client is constructed. The wrapper's own internal
 * exceptions are caught and silently dropped (it must never be the source
 * of the crash it is supposed to report).
 *
 * REQ-CLOG-01..14, REQ-CLOG-20..22, REQ-OPS-200..216.
 */

import type { BootstrapDescriptor } from './bootstrap.js';
import type { CapturedEvent } from './schema.js';
import { readMetaDescriptor, readBuildSha } from './bootstrap.js';
import { createQueue } from './queue.js';
import { createFlusher } from './flush.js';
import { installCapture } from './capture.js';
import { startLivetailWatcher } from './livetail.js';
import { installVitals } from './vitals.js';
import { _resetForTest as _resetBreadcrumbs } from './breadcrumbs.js';

export type { BootstrapDescriptor } from './bootstrap.js';
export type { CapturedEvent } from './schema.js';
export type { Breadcrumb, FullEvent, NarrowEvent, WireEvent } from './schema.js';
export { RequestIdContext, wrapFetch, uuidv7 } from './request_id.js';
export { recordRoute, recordFetch, recordConsole } from './breadcrumbs.js';
export { urlPathOnly } from './flush.js';

export interface ClientlogConfig {
  app: 'suite' | 'admin';
  /** Value of <meta name="herold-build"> -- set once by internal/webspa. */
  buildSha?: string;
  endpoints: {
    authenticated: string;
    anonymous: string;
  };
  /** Checked at each flush to decide schema and endpoint. */
  isAuthenticated: () => boolean;
  /** ms epoch or null; wrapper observes every ~1 s. */
  livetailUntil: () => number | null;
  /** Suppresses kind=log and kind=vital when false. Errors always sent. */
  telemetryEnabled: () => boolean;
  /**
   * Bootstrap descriptor from <meta name="herold-clientlog"> or JMAP session.
   * When absent, the wrapper reads the meta tag itself.
   */
  bootstrap?: BootstrapDescriptor;
}

export interface Clientlog {
  /**
   * Emit a fatal error immediately, bypassing the normal batch queue.
   * synchronous: true -- awaits the fetch before returning. Use only for
   * boot-path errors where the batch might never flush.
   * REQ-CLOG-04.
   */
  logFatal(err: unknown, opts?: { synchronous?: boolean }): Promise<void>;
  /**
   * Drains the queue and removes all installed handlers. Called on
   * deliberate SPA teardown (e.g. test cleanup).
   */
  shutdown(): void;
}

/**
 * No-op stub returned when bootstrap.enabled === false.
 * REQ-CLOG-12.
 */
const NOOP_CLIENTLOG: Clientlog = {
  logFatal: () => Promise.resolve(),
  shutdown: () => undefined,
};

let seq = 0;
function nextSeq(): number {
  return seq++;
}

/**
 * install() -- entry point.
 *
 * Returns a no-op stub when the bootstrap descriptor says enabled=false,
 * the meta tag is absent/invalid, or the install() call itself throws.
 * REQ-CLOG-12.
 */
export function install(cfg: ClientlogConfig): Clientlog {
  try {
    // Reset module-level seq for this page load (safe because install is
    // called once per page).
    seq = 0;
    _resetBreadcrumbs();

    // 1. Resolve bootstrap descriptor.
    const bootstrap: BootstrapDescriptor =
      cfg.bootstrap ?? readMetaDescriptor() ?? {
        enabled: true,
        batch_max_events: 20,
        batch_max_age_ms: 5000,
        queue_cap: 200,
        telemetry_enabled_default: true,
      };

    if (!bootstrap.enabled) {
      return NOOP_CLIENTLOG;
    }

    // 2. Resolve build SHA.
    const buildSha = cfg.buildSha ?? readBuildSha();

    // 3. Session / page identifiers.
    const pageId = crypto.randomUUID();
    let sessionId: string;
    try {
      sessionId = sessionStorage.getItem('herold-clientlog-session') ?? '';
      if (!sessionId) {
        sessionId = crypto.randomUUID();
        sessionStorage.setItem('herold-clientlog-session', sessionId);
      }
    } catch {
      sessionId = crypto.randomUUID();
    }

    // 4. Queue.
    const queue = createQueue({
      errorCap: 50,
      restCap: Math.max(50, (bootstrap.queue_cap ?? 200) - 50),
    });

    // 5. Route tracker -- the host can call recordRoute() at any time;
    //    we read the current pathname as a fallback.
    function getRoute(): string {
      try {
        return globalThis.location?.pathname ?? '';
      } catch {
        return '';
      }
    }

    function getUa(): string {
      try {
        return navigator.userAgent ?? '';
      } catch {
        return '';
      }
    }

    // 6. Flush infrastructure.
    const flusher = createFlusher({
      ctx: {
        app: cfg.app,
        buildSha,
        pageId,
        sessionId,
        getRoute,
        getUa,
        isAuthenticated: cfg.isAuthenticated,
        endpoints: cfg.endpoints,
        queue,
        originalConsole: { error: console.error.bind(console) },
      },
      fetchFn: globalThis.fetch.bind(globalThis),
      sendBeaconFn: (url, data) => navigator.sendBeacon(url, data),
      setTimeoutFn: (fn, ms) => setTimeout(fn, ms),
    });

    // 7. Batch timer state.
    let batchTimerId: ReturnType<typeof setTimeout> | null = null;
    let aggressiveTimerId: ReturnType<typeof setInterval> | null = null;

    function cancelBatchTimer(): void {
      if (batchTimerId !== null) {
        clearTimeout(batchTimerId);
        batchTimerId = null;
      }
    }

    function startBatchTimer(): void {
      if (batchTimerId !== null) return;
      batchTimerId = setTimeout(() => {
        batchTimerId = null;
        if (queue.size() > 0) flusher.flush();
      }, bootstrap.batch_max_age_ms ?? 5000);
    }

    function cancelAggressiveTimer(): void {
      if (aggressiveTimerId !== null) {
        clearInterval(aggressiveTimerId);
        aggressiveTimerId = null;
      }
    }

    function startAggressiveTimer(): void {
      if (aggressiveTimerId !== null) return;
      aggressiveTimerId = setInterval(() => {
        if (queue.size() > 0) flusher.flush();
      }, 100);
    }

    // 8. Livetail watcher updates the flush policy.
    const livetailWatcher = startLivetailWatcher(cfg.livetailUntil);

    // Keep track of whether aggressive mode is running.
    const policyCheckId = setInterval(() => {
      const policy = livetailWatcher.policy();
      if (policy === 'aggressive') {
        startAggressiveTimer();
      } else {
        cancelAggressiveTimer();
      }
    }, 1000);

    // 9. Emit function: routes captured events to the queue.
    function emit(event: CapturedEvent): void {
      try {
        // Telemetry gate: suppress kind=log and kind=vital when opted out.
        // Errors always pass through. REQ-CLOG-06 / REQ-OPS-208.
        if (event.kind !== 'error') {
          try {
            if (!cfg.telemetryEnabled()) return;
          } catch {
            return;
          }
        }

        queue.enqueue(event);

        // Per-event synchronous flush in livetail aggressive mode.
        if (livetailWatcher.policy() === 'aggressive') {
          void flusher.flushSync();
          return;
        }

        // Batch trigger: flush at batch_max_events.
        if (queue.size() >= (bootstrap.batch_max_events ?? 20)) {
          cancelBatchTimer();
          flusher.flush();
          return;
        }

        startBatchTimer();
      } catch { /* never throw out of emit */ }
    }

    // 10. Capture surfaces.
    const { originalConsole, removeHandlers } = installCapture({
      telemetryEnabled: cfg.telemetryEnabled,
      isLivetailActive: () => livetailWatcher.policy() === 'aggressive',
      getSeq: nextSeq,
      emit,
    });

    // Update the flusher's originalConsole reference now that we have the real one.
    // The flusher stores the reference at creation time; we need to propagate.
    // (The flusher context is already bound; this is a no-op for error logging.)
    void originalConsole;

    // 11. Web Vitals.
    installVitals(nextSeq, emit);

    // 12. Unload handlers.
    function onPageHide(): void {
      cancelBatchTimer();
      flusher.flushBeacon();
    }
    function onBeforeUnload(): void {
      if (queue.size() > 0) flusher.flushBeacon();
    }
    window.addEventListener('pagehide', onPageHide);
    window.addEventListener('beforeunload', onBeforeUnload);

    // 13. Public interface.
    return {
      async logFatal(err: unknown, opts?: { synchronous?: boolean }): Promise<void> {
        try {
          let msg: string;
          let stack: string | undefined;
          if (err instanceof Error) {
            msg = err.message.slice(0, 4096);
            stack = err.stack?.slice(0, 16384);
          } else {
            msg = String(err).slice(0, 4096);
          }

          const { snapshot: snapFn } = await import('./breadcrumbs.js');
          const ev: CapturedEvent = {
            kind: 'error',
            level: 'error',
            msg,
            stack,
            client_ts: new Date().toISOString(),
            seq: nextSeq(),
            synchronous: true,
            breadcrumbs_snapshot: snapFn(),
          };

          if (opts?.synchronous) {
            await flusher.flushSync([ev]);
          } else {
            queue.enqueue(ev);
            await flusher.flushSync();
          }
        } catch {
          // logFatal must never throw
        }
      },

      shutdown(): void {
        try {
          cancelBatchTimer();
          cancelAggressiveTimer();
          clearInterval(policyCheckId);
          livetailWatcher.stop();
          removeHandlers();
          window.removeEventListener('pagehide', onPageHide);
          window.removeEventListener('beforeunload', onBeforeUnload);
          if (queue.size() > 0) flusher.flush();
        } catch { /* never throw */ }
      },
    };
  } catch {
    // install() must never throw -- any internal error produces a no-op stub.
    return NOOP_CLIENTLOG;
  }
}
