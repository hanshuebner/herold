/**
 * webapp-diagnostics/1 provider for the Suite SPA.
 *
 * Implements the page side of a same-origin postMessage contract that lets
 * an external bug-reporter browser extension pull application-specific
 * state for a bug report. The extension knows nothing about herold; it
 * posts a generic request and this module answers with a Descriptor built
 * from whatever the SPA already tracks (auth, route, debug ring).
 *
 * Full contract: PROTOCOL.md in the bug-reporter extension repo. Summary:
 *   1. Extension posts { __webappDiagnostics: 'request', v: 1, nonce }.
 *   2. A page-side provider replies with the same nonce:
 *      { __webappDiagnostics: 'response', v: 1, nonce, payload: Descriptor }.
 *   3. The reply may be asynchronous; the extension waits ~900ms.
 *
 * Security: a reply is only sent when event.source === window (rules out
 * iframes/other windows), event.origin === location.origin (rules out
 * cross-origin senders), and the message carries the exact
 * __webappDiagnostics / v markers. The listener is installed exactly once.
 *
 * Content-blindness / redaction: `app`, `principal`, `context`, and `logs`
 * land in a PUBLIC ticket, per the protocol's zone table. `context` is a
 * plain route/UI snapshot -- never message bodies or addresses. `logs` come
 * from the device-local debug ring (lib/debug-ring); payloads are only
 * forwarded when small and free of anything that looks like a credential,
 * otherwise omitted (never the whole record). `private` is null and
 * `captureCookies` names the two herold cookies the extension must grab
 * itself -- this page never reads HttpOnly cookies.
 */

import { auth } from '../auth/auth.svelte';
import { router } from '../router/router.svelte';
import { compose } from '../compose/compose.svelte';
import { composeStack } from '../compose/compose-stack.svelte';
import { readAll, type DebugRecord } from '../debug-ring/debug-ring';

/** Protocol major version this provider speaks. Must match the extension's `v`. */
const PROTOCOL_VERSION = 1;
const APP_ID = 'herold-suite';
const APP_NAME = 'Herold Suite';

/** Most recent debug-ring records forwarded in a single response. */
const MAX_LOGS = 300;
/** Debug-ring payloads longer than this (as JSON text) are omitted, not truncated. */
const MAX_PAYLOAD_CHARS = 500;
/** Crude secret-shaped-string filter for debug-ring payloads (see module doc). */
const SENSITIVE_PAYLOAD_PATTERN = /token|password|secret|cookie|authoriz|bearer|totp/i;

interface Descriptor {
  app: { id: string; name: string; version: string };
  principal?: { id?: string; label?: string };
  context?: Record<string, unknown>;
  logs?: DiagnosticsLog[];
  private?: Record<string, unknown> | null;
  captureCookies?: string[];
}

interface DiagnosticsLog {
  ts: number;
  level: string;
  msg: string;
  ctx?: string;
  payload?: unknown;
}

interface DiagnosticsRequest {
  __webappDiagnostics: 'request';
  v: number;
  nonce: string;
}

let installed = false;

/**
 * Install the webapp-diagnostics/1 message listener. Idempotent: only the
 * first call registers the handler. Call once from the app bootstrap
 * (main.ts), alongside the other early singleton installs.
 */
export function initDiagnosticsProvider(): void {
  if (installed) return;
  installed = true;
  window.addEventListener('message', handleMessage);
}

function handleMessage(event: MessageEvent): void {
  // Reject anything not posted by this exact window at this exact origin --
  // rules out iframes, popups, and cross-origin senders (PROTOCOL.md
  // "Providers MUST verify...").
  if (event.source !== window) return;
  if (event.origin !== location.origin) return;

  const data = event.data as Partial<DiagnosticsRequest> | null | undefined;
  if (!data || typeof data !== 'object') return;
  if (data.__webappDiagnostics !== 'request') return;
  if (data.v !== PROTOCOL_VERSION) return;
  if (typeof data.nonce !== 'string' || data.nonce === '') return;

  const nonce = data.nonce;
  void buildDescriptor().then((payload) => {
    window.postMessage(
      { __webappDiagnostics: 'response', v: PROTOCOL_VERSION, nonce, payload },
      location.origin,
    );
  });
}

async function buildDescriptor(): Promise<Descriptor> {
  const principal = principalInfo();
  return {
    app: { id: APP_ID, name: APP_NAME, version: appVersion() },
    ...(principal ? { principal } : {}),
    context: buildContext(),
    logs: await buildLogs(),
    // The extension performs privileged HttpOnly-cookie capture itself; the
    // page cannot and must not try to read these.
    private: null,
    captureCookies: ['herold_session', 'herold_public_csrf'],
  };
}

/** Injected by vite.config.ts's `define`; falls back to 'dev' outside a Vite build (e.g. plain node). */
function appVersion(): string {
  return typeof __HEROLD_VERSION__ !== 'undefined' ? __HEROLD_VERSION__ : 'dev';
}

function principalInfo(): Descriptor['principal'] {
  if (auth.status !== 'ready' || !auth.principalId) return undefined;
  return { id: auth.principalId, label: auth.session?.username };
}

/**
 * Compact, JSON-serializable snapshot of current app state useful for
 * repro: route, selected mailbox/thread (both are encoded directly in the
 * hash-router URL, per lib/router), and whether a compose window is open.
 * Never throws -- a store read failure just narrows the snapshot.
 */
function buildContext(): Record<string, unknown> {
  const context: Record<string, unknown> = {};
  try {
    context.route = router.current;
    const parts = router.parts;
    if (parts[0] === 'mail' && parts[1] === 'folder' && parts[2]) {
      context.mailboxId = parts[2];
    }
    if (parts[0] === 'mail' && parts[1] === 'thread' && parts[2]) {
      context.threadId = parts[2];
    }
    if (parts[0] === 'thread-window' && parts[1]) {
      context.threadId = parts[1];
    }
  } catch {
    /* router unavailable; leave route/ids out */
  }
  try {
    context.composeOpen = compose.isOpen;
  } catch {
    /* compose store unavailable */
  }
  try {
    context.composeMinimizedCount = composeStack.minimized.length;
  } catch {
    /* compose-stack store unavailable */
  }
  return context;
}

/**
 * Read the device-local debug ring and map each DebugRecord to the
 * protocol's DiagnosticsLog shape. Never throws -- an IDB failure yields an
 * empty log list rather than dropping the whole response.
 */
async function buildLogs(): Promise<DiagnosticsLog[]> {
  let records: DebugRecord[];
  try {
    records = await readAll();
  } catch {
    return [];
  }
  return records.slice(-MAX_LOGS).map(toDiagnosticsLog);
}

function toDiagnosticsLog(record: DebugRecord): DiagnosticsLog {
  const ts = Date.parse(record.ts);
  const log: DiagnosticsLog = {
    ts: Number.isNaN(ts) ? 0 : ts,
    level: record.level,
    msg: record.msg,
    ctx: record.ctx,
  };
  const payload = redactedPayload(record.payload);
  if (payload !== undefined) log.payload = payload;
  return log;
}

/**
 * Forward a debug-ring payload only when it is small and does not look
 * like it carries a credential. `logs` lands in a public ticket
 * (PROTOCOL.md), so this errs toward omitting rather than guessing.
 */
function redactedPayload(raw: string | undefined): unknown {
  if (raw === undefined) return undefined;
  if (raw.length > MAX_PAYLOAD_CHARS) return undefined;
  if (SENSITIVE_PAYLOAD_PATTERN.test(raw)) return undefined;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

/** Exported for vitest; never called from production code. */
export const _internals_forTest = {
  buildDescriptor,
  resetInstalled: (): void => {
    if (installed) {
      window.removeEventListener('message', handleMessage);
      installed = false;
    }
  },
};
