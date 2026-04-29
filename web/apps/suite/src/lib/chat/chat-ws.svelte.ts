/**
 * Chat ephemeral WebSocket client per docs/design/web/architecture/07-chat-protocol.md.
 *
 * Single persistent connection at wss://<origin>/chat/ws, authenticated by
 * the session cookie (attaches automatically -- same-origin deployment).
 *
 * Wire envelope (both directions):
 *   { "type": "<token>", "payload": {...}, "ack": "...", "error": {...} }
 *
 * Server token names are the source of truth (internal/protochat/protocol.go).
 * Inbound tokens: typing, presence, read, call.signal, error, ack, pong.
 * Outbound tokens: typing.start, typing.stop, presence.set, subscribe,
 *   unsubscribe, call.signal, ping.
 *
 * PrincipalID arrives as uint64 (JSON number) on the wire. The boundary
 * coerces it to string so downstream Map<string, ...> keys are unaffected.
 *
 * Lifecycle:
 *   - connect() opens the socket; idempotent while OPEN/CONNECTING.
 *   - Reconnects on close with exponential backoff (1s, 2s, 4s, 8s, max 30s).
 *   - Server sends { "type": "ping" } periodically; we respond { "type": "pong" }.
 *   - disconnect() closes cleanly and suppresses reconnect (call on logout).
 *
 * The shell mounts the WS once per browser tab (after auth), so it outlives
 * route changes per the persistent-panel requirement.
 *
 * Consumers register handlers via on(type, handler) which returns an
 * unregister function.
 */

import type { InboundFrame, OutboundFrame, WireEnvelope } from './types';

type InboundType = InboundFrame['type'];

type PayloadByType<T extends InboundFrame, Type extends InboundType> = T extends {
  type: Type;
  payload: infer P;
}
  ? P
  : T extends { type: Type }
    ? undefined
    : never;

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyHandler = (payload: any) => void;

export type ChatWsState =
  | 'idle'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'disconnected';

/** Backoff schedule: 1s, 2s, 4s, 8s, 16s, 30s (capped). */
const BACKOFF_MS = [1000, 2000, 4000, 8000, 16000, 30000] as const;

class ChatWebSocket {
  state = $state<ChatWsState>('idle');

  #ws: WebSocket | null = null;
  #handlers = new Map<string, Set<AnyHandler>>();
  #backoffIndex = 0;
  #reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  /** When true, reconnect is suppressed (clean logout). */
  #stopped = false;

  /**
   * Open the WebSocket. Idempotent: if already OPEN or CONNECTING, does
   * nothing. The path /chat/ws is the herold ephemeral endpoint; the cookie
   * authenticates automatically because we are same-origin.
   */
  connect(): void {
    this.#stopped = false;
    if (
      this.#ws &&
      (this.#ws.readyState === WebSocket.OPEN ||
        this.#ws.readyState === WebSocket.CONNECTING)
    ) {
      return;
    }
    this.#open();
  }

  /**
   * Close the WebSocket and suppress reconnect. Call on logout.
   */
  disconnect(): void {
    this.#stopped = true;
    this.#clearReconnectTimer();
    this.#ws?.close(1000, 'clean shutdown');
    this.#ws = null;
    this.state = 'idle';
  }

  /**
   * Send a frame. Wraps the outbound payload in the wire envelope
   * { type, payload }. If the socket is not open, the frame is dropped
   * silently (ephemeral signals are best-effort; the caller should not
   * queue them).
   */
  send(frame: OutboundFrame): void {
    if (!this.#ws || this.#ws.readyState !== WebSocket.OPEN) return;
    const envelope: WireEnvelope =
      frame.type === 'ping'
        ? { type: 'ping' }
        : { type: frame.type, payload: (frame as { payload: unknown }).payload };
    this.#ws.send(JSON.stringify(envelope));
  }

  /**
   * Register a handler for a specific inbound type token. The handler
   * receives the parsed payload (not the envelope). Returns an unregister
   * function.
   *
   * Usage:
   *   const off = chatWs.on('presence', frame => { ... });
   *   // later:
   *   off();
   */
  on<Type extends InboundType>(
    type: Type,
    handler: (payload: PayloadByType<InboundFrame, Type>) => void,
  ): () => void {
    let set = this.#handlers.get(type);
    if (!set) {
      set = new Set();
      this.#handlers.set(type, set);
    }
    set.add(handler as AnyHandler);
    return () => {
      this.#handlers.get(type)?.delete(handler as AnyHandler);
    };
  }

  #open(): void {
    if (this.#stopped) return;

    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = `${protocol}//${location.host}/chat/ws`;

    this.state = 'connecting';
    const ws = new WebSocket(url);
    this.#ws = ws;

    ws.addEventListener('open', () => {
      this.#backoffIndex = 0;
      this.state = 'connected';
      // Announce presence as online.
      this.send({ type: 'presence.set', payload: { state: 'online' } });
    });

    ws.addEventListener('message', (event: MessageEvent) => {
      this.#handleMessage(event.data as string);
    });

    ws.addEventListener('close', (event) => {
      // 1000 = normal closure (our own disconnect()); don't reconnect.
      if (event.code === 1000 || this.#stopped) {
        this.state = 'idle';
        this.#ws = null;
        return;
      }
      this.#ws = null;
      this.state = 'reconnecting';
      this.#scheduleReconnect();
    });

    ws.addEventListener('error', () => {
      // The 'close' event fires after 'error'; handle reconnect there.
    });
  }

  #handleMessage(raw: string): void {
    let envelope: WireEnvelope;
    try {
      envelope = JSON.parse(raw) as WireEnvelope;
    } catch {
      return;
    }
    const type = envelope.type;

    // Handle ping at the transport layer without requiring consumers.
    // The server periodically sends { "type": "ping" }; respond with pong
    // directly without going through the typed send() path.
    if (type === 'ping') {
      if (this.#ws && this.#ws.readyState === WebSocket.OPEN) {
        this.#ws.send(JSON.stringify({ type: 'pong' }));
      }
      return;
    }

    // Coerce uint64 principalId fields to string at the boundary so
    // downstream Map<string, ...> keys work without changes elsewhere.
    const payload = this.#coercePrincipalIds(type, envelope.payload);

    const set = this.#handlers.get(type);
    if (!set) return;
    for (const handler of set) {
      try {
        handler(payload);
      } catch (err) {
        console.error('chatWs handler threw', err);
      }
    }
  }

  /**
   * Coerce uint64 principalId fields to string for known inbound types.
   * The server sends PrincipalID as a JSON number; client-side Maps key by
   * string, so we normalise once at the wire boundary.
   */
  #coercePrincipalIds(type: string, payload: unknown): unknown {
    if (payload === null || typeof payload !== 'object') return payload;
    const p = payload as Record<string, unknown>;

    if (type === 'presence' || type === 'typing' || type === 'read') {
      if (typeof p['principalId'] === 'number') {
        return { ...p, principalId: String(p['principalId']) };
      }
    }
    // call.signal fromPrincipalId stays as a number on the payload; callers
    // that need string keys must coerce themselves (it is not used as a Map key).
    return payload;
  }

  #scheduleReconnect(): void {
    this.#clearReconnectTimer();
    const delayMs = BACKOFF_MS[this.#backoffIndex] ?? 30000;
    this.#backoffIndex = Math.min(
      this.#backoffIndex + 1,
      BACKOFF_MS.length - 1,
    );
    this.#reconnectTimer = setTimeout(() => {
      this.#reconnectTimer = null;
      if (!this.#stopped) this.#open();
    }, delayMs);
  }

  #clearReconnectTimer(): void {
    if (this.#reconnectTimer !== null) {
      clearTimeout(this.#reconnectTimer);
      this.#reconnectTimer = null;
    }
  }
}

/** Module-level singleton -- one WebSocket connection per browser tab. */
export const chatWs = new ChatWebSocket();
