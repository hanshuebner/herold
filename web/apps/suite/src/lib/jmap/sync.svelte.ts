/**
 * EventSource-driven sync per docs/architecture/03-sync-and-state.md.
 *
 * Subscribes to herold's `/jmap/eventsource` endpoint (templated URL from
 * the session descriptor); listens for `event: state` SSE messages
 * carrying StateChange payloads (RFC 8620 §7.3); dispatches per-type
 * handlers so cache stores can issue `Foo/changes` and refresh.
 *
 * Connection lifecycle is tracked in `connectionState` for UI indication.
 * Browser EventSource auto-reconnects on transient failure; this layer
 * tracks status and re-opens on CLOSED.
 */

import { auth } from '../auth/auth.svelte';

export type ConnectionState =
  | 'idle'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'disconnected';

export type StateChangeHandler = (newState: string, accountId: string) => void;

interface StateChangePayload {
  '@type': 'StateChange';
  changed: Record<string, Record<string, string>>;
}

class Sync {
  connectionState = $state<ConnectionState>('idle');

  #es: EventSource | null = null;
  #handlers = new Map<string, Set<StateChangeHandler>>();
  #subscribedTypes: string[] = [];

  /**
   * Register a handler for state changes of a JMAP type ('Email',
   * 'Mailbox', 'Thread', etc.). Returns an unregister function.
   */
  on(type: string, handler: StateChangeHandler): () => void {
    let set = this.#handlers.get(type);
    if (!set) {
      set = new Set();
      this.#handlers.set(type, set);
    }
    set.add(handler);
    return () => {
      const cur = this.#handlers.get(type);
      cur?.delete(handler);
    };
  }

  /**
   * Open the EventSource subscription for the given types. Idempotent —
   * calling again with the same types is a no-op; calling with different
   * types reopens the connection.
   */
  start(types: string[]): void {
    if (this.#es && sameTypes(types, this.#subscribedTypes)) return;
    this.stop();
    this.#subscribedTypes = [...types];
    this.#open();
  }

  stop(): void {
    this.#es?.close();
    this.#es = null;
    this.connectionState = 'idle';
  }

  #open(): void {
    const session = auth.session;
    if (!session) {
      this.connectionState = 'disconnected';
      return;
    }

    const url = buildEventSourceUrl(session.eventSourceUrl, this.#subscribedTypes);
    this.connectionState = 'connecting';
    const es = new EventSource(url, { withCredentials: true });
    this.#es = es;

    es.addEventListener('open', () => {
      this.connectionState = 'connected';
    });

    es.addEventListener('state', (event) => {
      this.#handlePayload((event as MessageEvent).data);
    });

    // Some servers may emit StateChange via the default 'message' event.
    es.addEventListener('message', (event) => {
      this.#handlePayload(event.data);
    });

    es.addEventListener('error', () => {
      if (es.readyState === EventSource.CLOSED) {
        this.connectionState = 'disconnected';
        this.#es = null;
        // Re-open after a short delay; EventSource doesn't reconnect from
        // CLOSED on its own.
        setTimeout(() => {
          if (this.#subscribedTypes.length > 0 && !this.#es) {
            this.connectionState = 'reconnecting';
            this.#open();
          }
        }, 1500);
      } else {
        this.connectionState = 'reconnecting';
      }
    });
  }

  #handlePayload(raw: unknown): void {
    if (typeof raw !== 'string') return;
    let payload: unknown;
    try {
      payload = JSON.parse(raw);
    } catch {
      return;
    }
    if (!isStateChange(payload)) return;
    for (const accountId of Object.keys(payload.changed)) {
      const typeMap = payload.changed[accountId];
      if (!typeMap) continue;
      for (const type of Object.keys(typeMap)) {
        const newState = typeMap[type];
        if (typeof newState !== 'string') continue;
        const set = this.#handlers.get(type);
        if (!set) continue;
        for (const handler of set) {
          try {
            handler(newState, accountId);
          } catch (err) {
            console.error('sync handler threw', err);
          }
        }
      }
    }
  }
}

function isStateChange(value: unknown): value is StateChangePayload {
  if (!value || typeof value !== 'object') return false;
  const v = value as { '@type'?: unknown; changed?: unknown };
  return v['@type'] === 'StateChange' && typeof v.changed === 'object';
}

function sameTypes(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

/**
 * Substitute the {types} / {closeafter} / {ping} placeholders in
 * the session descriptor's `eventSourceUrl` (RFC 8620 §7.3 templated URL).
 */
function buildEventSourceUrl(template: string, types: string[]): string {
  return template
    .replace('{types}', encodeURIComponent(types.join(',')))
    .replace('{closeafter}', 'no')
    .replace('{ping}', '30');
}

export const sync = new Sync();
