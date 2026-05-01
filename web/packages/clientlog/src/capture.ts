/**
 * Capture surfaces (REQ-CLOG-01, REQ-CLOG-13).
 *
 * Installs handlers for:
 *   - window.onerror
 *   - window.addEventListener('unhandledrejection')
 *   - console.error, console.warn  (always)
 *   - console.info, console.log, console.debug  (conditional on livetail
 *     active or telemetryEnabled())
 *
 * Console wrapping replaces globalThis.console properties with proxies
 * that call the original AND enqueue an event. The original remains the
 * visible console output. REQ-CLOG-01.
 *
 * Dev-mode echo: in import.meta.env.DEV, every captured event is also
 * logged to the original console under a [clientlog] prefix. Stripped in
 * prod. REQ-CLOG-13.
 */

import type { CapturedEvent, EventKind, EventLevel } from './schema.js';
import { snapshot, recordConsole } from './breadcrumbs.js';
import { RequestIdContext } from './request_id.js';

export type EmitFn = (event: CapturedEvent) => void;

export interface CaptureConfig {
  telemetryEnabled: () => boolean;
  isLivetailActive: () => boolean;
  getSeq: () => number;
  emit: EmitFn;
}

/** Retained originals so shutdown() can restore them. */
export interface OriginalConsole {
  error: typeof console.error;
  warn: typeof console.warn;
  info: typeof console.info;
  log: typeof console.log;
  debug: typeof console.debug;
}

/**
 * Materialise an error-like value into msg + optional stack.
 * Intentionally minimal -- we do not touch error.cause chains to avoid
 * accidentally capturing sensitive data (REQ-CLOG-10).
 */
function extractError(err: unknown): { msg: string; stack?: string } {
  if (err instanceof Error) {
    return {
      msg: err.message.slice(0, 4096),
      stack: err.stack?.slice(0, 16384),
    };
  }
  return { msg: String(err).slice(0, 4096) };
}

function makeEvent(
  kind: EventKind,
  level: EventLevel,
  msg: string,
  getSeq: () => number,
  opts?: { stack?: string; isError?: boolean },
): CapturedEvent {
  const ev: CapturedEvent = {
    kind,
    level,
    msg,
    client_ts: new Date().toISOString(),
    seq: getSeq(),
    request_id: RequestIdContext.current(),
  };
  if (opts?.stack !== undefined) ev.stack = opts.stack;
  // Attach breadcrumb snapshot only for errors (REQ-OPS-202).
  if (opts?.isError) ev.breadcrumbs_snapshot = snapshot();
  return ev;
}

function devEcho(original: typeof console.error, label: string, args: unknown[]): void {
  if (import.meta.env.DEV) {
    original(`[clientlog] ${label}`, ...args);
  }
}

export function installCapture(
  cfg: CaptureConfig,
): {
  originalConsole: OriginalConsole;
  removeHandlers: () => void;
} {
  const { telemetryEnabled, isLivetailActive, getSeq, emit } = cfg;

  // Snapshot originals BEFORE we overwrite them.
  const orig: OriginalConsole = {
    error: console.error.bind(console),
    warn: console.warn.bind(console),
    info: console.info.bind(console),
    log: console.log.bind(console),
    debug: console.debug.bind(console),
  };

  function isConditionalActive(): boolean {
    try {
      return isLivetailActive() || telemetryEnabled();
    } catch {
      return false;
    }
  }

  function argsToMsg(args: unknown[]): string {
    return args
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .join(' ')
      .slice(0, 4096);
  }

  // console.error -- always captured
  console.error = (...args: unknown[]) => {
    orig.error(...args);
    try {
      const msg = argsToMsg(args);
      recordConsole('error', msg);
      const ev = makeEvent('error', 'error', msg, getSeq, { isError: true });
      emit(ev);
      devEcho(orig.error, 'console.error', args);
    } catch { /* never throw */ }
  };

  // console.warn -- always captured
  console.warn = (...args: unknown[]) => {
    orig.warn(...args);
    try {
      const msg = argsToMsg(args);
      recordConsole('warn', msg);
      const ev = makeEvent('log', 'warn', msg, getSeq);
      emit(ev);
      devEcho(orig.error, 'console.warn', args);
    } catch { /* never throw */ }
  };

  // console.info -- conditional
  console.info = (...args: unknown[]) => {
    orig.info(...args);
    try {
      if (!isConditionalActive()) return;
      const msg = argsToMsg(args);
      const ev = makeEvent('log', 'info', msg, getSeq);
      emit(ev);
      devEcho(orig.error, 'console.info', args);
    } catch { /* never throw */ }
  };

  // console.log -- conditional
  console.log = (...args: unknown[]) => {
    orig.log(...args);
    try {
      if (!isConditionalActive()) return;
      const msg = argsToMsg(args);
      const ev = makeEvent('log', 'info', msg, getSeq);
      emit(ev);
      devEcho(orig.error, 'console.log', args);
    } catch { /* never throw */ }
  };

  // console.debug -- conditional
  console.debug = (...args: unknown[]) => {
    orig.debug(...args);
    try {
      if (!isConditionalActive()) return;
      const msg = argsToMsg(args);
      const ev = makeEvent('log', 'debug', msg, getSeq);
      emit(ev);
      devEcho(orig.error, 'console.debug', args);
    } catch { /* never throw */ }
  };

  // window.onerror
  function onError(
    message: string | Event,
    _source?: string,
    _lineno?: number,
    _colno?: number,
    error?: Error,
  ): void {
    try {
      const { msg, stack } = error
        ? extractError(error)
        : { msg: String(message).slice(0, 4096), stack: undefined };
      const ev = makeEvent('error', 'error', msg, getSeq, { stack, isError: true });
      emit(ev);
      devEcho(orig.error, 'window.onerror', [message]);
    } catch { /* never throw */ }
  }
  window.addEventListener('error', onError as EventListener);

  // window.unhandledrejection
  function onUnhandledRejection(e: PromiseRejectionEvent): void {
    try {
      const { msg, stack } = extractError(e.reason);
      const ev = makeEvent('error', 'error', msg, getSeq, { stack, isError: true });
      emit(ev);
      devEcho(orig.error, 'unhandledrejection', [e.reason]);
    } catch { /* never throw */ }
  }
  window.addEventListener('unhandledrejection', onUnhandledRejection);

  function removeHandlers(): void {
    // Restore console
    console.error = orig.error;
    console.warn = orig.warn;
    console.info = orig.info;
    console.log = orig.log;
    console.debug = orig.debug;
    window.removeEventListener('error', onError as EventListener);
    window.removeEventListener('unhandledrejection', onUnhandledRejection);
  }

  return { originalConsole: orig, removeHandlers };
}
