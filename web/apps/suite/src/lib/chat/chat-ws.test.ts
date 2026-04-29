/**
 * Tests for the chat WebSocket client.
 *
 * Uses a mock WebSocket so no real network is needed. We install the mock
 * on globalThis before each test and restore it after.
 *
 * Wire protocol: { "type": "<token>", "payload": {...} }
 * (internal/protochat/protocol.go is the source of truth)
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// -----------------------------------------------------------------------
// Minimal WebSocket mock
// -----------------------------------------------------------------------

type WSListener = (event: Event | MessageEvent | CloseEvent) => void;

class MockWebSocket {
  static OPEN = 1;
  static CONNECTING = 0;
  static CLOSED = 3;

  readyState: number;
  url: string;
  sent: string[] = [];

  private listeners = new Map<string, Set<WSListener>>();

  constructor(url: string) {
    this.url = url;
    this.readyState = MockWebSocket.CONNECTING;
    // Schedule open asynchronously so tests can register listeners first.
    queueMicrotask(() => this.simulateOpen());
  }

  addEventListener(type: string, listener: WSListener): void {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(listener);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(code?: number): void {
    this.readyState = MockWebSocket.CLOSED;
    const ev = new CloseEvent('close', { code: code ?? 1005, wasClean: true });
    const set = this.listeners.get('close');
    if (set) for (const h of set) h(ev);
  }

  simulateOpen(): void {
    this.readyState = MockWebSocket.OPEN;
    const set = this.listeners.get('open');
    if (set) for (const h of set) h(new Event('open'));
  }

  simulateMessage(data: string): void {
    const ev = new MessageEvent('message', { data });
    const set = this.listeners.get('message');
    if (set) for (const h of set) h(ev);
  }

  simulateClose(code = 1006): void {
    this.readyState = MockWebSocket.CLOSED;
    const ev = new CloseEvent('close', { code, wasClean: false });
    const set = this.listeners.get('close');
    if (set) for (const h of set) h(ev);
  }
}

let lastWs: MockWebSocket | null = null;
const originalWS = (globalThis as unknown as { WebSocket?: unknown }).WebSocket;

describe('ChatWebSocket', () => {
  beforeEach(() => {
    lastWs = null;
    class TestWS extends MockWebSocket {
      constructor(url: string) {
        super(url);
        lastWs = this;
      }
      static override OPEN = MockWebSocket.OPEN;
      static override CONNECTING = MockWebSocket.CONNECTING;
      static override CLOSED = MockWebSocket.CLOSED;
    }
    (globalThis as unknown as { WebSocket: unknown }).WebSocket = TestWS;

    // Simulate a browser URL.
    Object.defineProperty(globalThis, 'location', {
      value: { protocol: 'http:', host: 'localhost:5173' },
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    if (originalWS !== undefined) {
      (globalThis as unknown as { WebSocket: unknown }).WebSocket = originalWS;
    }
  });

  it('opens a WS connection on connect()', async () => {
    const { chatWs } = await import('./chat-ws.svelte');
    // Reset the singleton state between tests by disconnecting first.
    chatWs.disconnect();
    chatWs.connect();
    expect(lastWs).not.toBeNull();
    expect(lastWs!.url).toBe('ws://localhost:5173/chat/ws');
    chatWs.disconnect();
  });

  it('responds to ping with pong using the wire envelope', async () => {
    const { chatWs } = await import('./chat-ws.svelte');
    chatWs.disconnect();
    chatWs.connect();
    // Wait for the open microtask.
    await new Promise<void>((r) => queueMicrotask(r));
    // Server sends ping as { "type": "ping" } per protocol.go.
    lastWs!.simulateMessage(JSON.stringify({ type: 'ping' }));
    expect(lastWs!.sent).toContain(JSON.stringify({ type: 'pong' }));
    chatWs.disconnect();
  });

  it('calls registered handlers with the parsed payload', async () => {
    const { chatWs } = await import('./chat-ws.svelte');
    chatWs.disconnect();
    chatWs.connect();
    await new Promise<void>((r) => queueMicrotask(r));

    const received: unknown[] = [];
    const off = chatWs.on('presence', (payload) => received.push(payload));

    // Presence payload: principalId as number on wire (uint64), coerced to string.
    const envelope = {
      type: 'presence',
      payload: { principalId: 42, state: 'online', lastSeenAt: 1700000000 },
    };
    lastWs!.simulateMessage(JSON.stringify(envelope));

    expect(received).toHaveLength(1);
    // principalId must be coerced to string at the WS boundary.
    expect(received[0]).toMatchObject({ principalId: '42', state: 'online', lastSeenAt: 1700000000 });

    off();
    lastWs!.simulateMessage(JSON.stringify(envelope));
    expect(received).toHaveLength(1); // handler removed

    chatWs.disconnect();
  });

  it('coerces numeric principalId to string for typing frames', async () => {
    const { chatWs } = await import('./chat-ws.svelte');
    chatWs.disconnect();
    chatWs.connect();
    await new Promise<void>((r) => queueMicrotask(r));

    const received: unknown[] = [];
    const off = chatWs.on('typing', (payload) => received.push(payload));

    const envelope = {
      type: 'typing',
      payload: { conversationId: 'c1', principalId: 99, state: 'start' },
    };
    lastWs!.simulateMessage(JSON.stringify(envelope));

    expect(received).toHaveLength(1);
    expect(received[0]).toMatchObject({ conversationId: 'c1', principalId: '99', state: 'start' });

    off();
    chatWs.disconnect();
  });

  it('send() wraps outbound payload in the wire envelope', async () => {
    const { chatWs } = await import('./chat-ws.svelte');
    chatWs.disconnect();
    chatWs.connect();
    await new Promise<void>((r) => queueMicrotask(r));

    chatWs.send({ type: 'typing.start', payload: { conversationId: 'c1' } });
    expect(lastWs!.sent).toContain(
      JSON.stringify({ type: 'typing.start', payload: { conversationId: 'c1' } }),
    );

    chatWs.disconnect();
  });

  it('send() wraps presence.set payload in the wire envelope', async () => {
    const { chatWs } = await import('./chat-ws.svelte');
    chatWs.disconnect();
    chatWs.connect();
    await new Promise<void>((r) => queueMicrotask(r));

    // Consume the presence.set that is auto-sent on open.
    const presenceSet = JSON.stringify({ type: 'presence.set', payload: { state: 'online' } });
    expect(lastWs!.sent).toContain(presenceSet);

    chatWs.disconnect();
  });

  it('disconnect() suppresses reconnect', async () => {
    const { chatWs } = await import('./chat-ws.svelte');
    chatWs.disconnect();
    chatWs.connect();
    await new Promise<void>((r) => queueMicrotask(r));

    // Disconnect before an abnormal close.
    chatWs.disconnect();

    // After disconnect, reconnecting should be suppressed even if the
    // backoff timer fires. We verify by checking that no new WS was created.
    const wsCreatedAfter = vi.fn();
    const prevCtor = (globalThis as unknown as { WebSocket: unknown }).WebSocket;
    (globalThis as unknown as { WebSocket: unknown }).WebSocket = class {
      constructor() {
        wsCreatedAfter();
      }
    };

    // Allow any queued microtasks / timers to run.
    await new Promise<void>((r) => setTimeout(r, 20));
    expect(wsCreatedAfter).not.toHaveBeenCalled();
    (globalThis as unknown as { WebSocket: unknown }).WebSocket = prevCtor;
  });
});
