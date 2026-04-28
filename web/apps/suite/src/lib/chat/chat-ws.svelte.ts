/**
 * Chat ephemeral WebSocket client per docs/design/web/architecture/07-chat-protocol.md.
 *
 * Single persistent connection at wss://<origin>/chat/ws, authenticated by
 * the session cookie (attaches automatically — same-origin deployment).
 *
 * Lifecycle:
 *   - connect() opens the socket; idempotent while OPEN/CONNECTING.
 *   - Reconnects on close with exponential backoff (1s, 2s, 4s, 8s, max 30s).
 *   - Server sends { "op": "ping" } every 30s; we respond { "op": "pong" }.
 *   - disconnect() closes cleanly and suppresses reconnect (call on logout).
 *
 * The shell mounts the WS once per browser tab (after auth), so it outlives
 * route changes per the persistent-panel requirement.
 *
 * Consumers register handlers via on(op, handler) which returns an
 * unregister function.
 */

import type { InboundFrame, OutboundFrame } from './types';

type InboundOp = InboundFrame['op'];

type FrameByOp<T extends InboundFrame, Op extends InboundOp> = T extends {
  op: Op;
}
  ? T
  : never;

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyHandler = (frame: any) => void;

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
   * Send a frame. If the socket is not open, the frame is dropped silently
   * (ephemeral signals are best-effort; the caller should not queue them).
   */
  send(frame: OutboundFrame): void {
    if (!this.#ws || this.#ws.readyState !== WebSocket.OPEN) return;
    this.#ws.send(JSON.stringify(frame));
  }

  /**
   * Register a handler for a specific op. Returns an unregister function.
   *
   * Usage:
   *   const off = chatWs.on('presence-update', frame => { ... });
   *   // later:
   *   off();
   */
  on<Op extends InboundOp>(
    op: Op,
    handler: (frame: FrameByOp<InboundFrame, Op>) => void,
  ): () => void {
    let set = this.#handlers.get(op);
    if (!set) {
      set = new Set();
      this.#handlers.set(op, set);
    }
    set.add(handler as AnyHandler);
    return () => {
      this.#handlers.get(op)?.delete(handler as AnyHandler);
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
      this.send({ op: 'presence', state: 'online' });
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
    let frame: InboundFrame;
    try {
      frame = JSON.parse(raw) as InboundFrame;
    } catch {
      return;
    }
    const op = frame.op;

    // Handle ping/pong at the transport layer without requiring consumers.
    if (op === 'ping') {
      this.send({ op: 'pong' });
      return;
    }

    const set = this.#handlers.get(op);
    if (!set) return;
    for (const handler of set) {
      try {
        handler(frame);
      } catch (err) {
        console.error('chatWs handler threw', err);
      }
    }
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

/** Module-level singleton — one WebSocket connection per browser tab. */
export const chatWs = new ChatWebSocket();
