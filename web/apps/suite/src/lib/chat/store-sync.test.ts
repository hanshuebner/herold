/**
 * Regression tests for the chat-store EventSource sync handlers.
 *
 * The three #syncFooChanges paths issue a Foo/changes call followed
 * by Foo/get with a JSON-pointer back-reference into the changes
 * response. A path that does not exist in the response shape (e.g.
 * `/changed`, which is not a real RFC 8620 §5.2 field) raises
 * `invalidResultReference` server-side and the cache update is lost.
 * The unit-test layer used to mock jmap.batch with canned responses
 * that did not validate the paths, so a bad ref looked indistinguishable
 * from a good one.
 *
 * These tests capture the actual request body the store sends and
 * assert that every back-reference path resolves to a key that exists
 * in the fabricated /changes response — exactly mirroring what the
 * server's resolveBackReferences does. They will fail loudly the next
 * time the wire shape drifts.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// Capture handlers registered via sync.on so the test can drive the
// store as if an EventSource state advance had fired.
const syncHandlers = new Map<
  string,
  (newState: string, accountId: string) => void
>();

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(
      (
        type: string,
        handler: (newState: string, accountId: string) => void,
      ) => {
        syncHandlers.set(type, handler);
        return vi.fn();
      },
    ),
  },
}));

vi.mock('../jmap/client', () => ({
  jmap: {
    batch: vi.fn(),
    session: null,
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn(),
  },
  strict: (r: unknown[]) => r,
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: {
      capabilities: { 'https://netzhansa.com/jmap/chat': {} },
      primaryAccounts: {
        'https://netzhansa.com/jmap/chat': 'acc1',
        'urn:ietf:params:jmap:core': 'acc1',
      },
    },
    principalId: 'p1',
  },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('./chat-ws.svelte', () => ({
  chatWs: {
    connect: vi.fn(),
    disconnect: vi.fn(),
    send: vi.fn(),
    on: vi.fn(() => vi.fn()),
    state: 'connected',
  },
}));

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

interface CapturedCall {
  name: string;
  args: Record<string, unknown>;
  callId: string;
}

/**
 * Replace jmap.batch with an implementation that runs the builder,
 * captures each call's name+args, and returns canned method
 * responses keyed by name. The capture array is shared back to the
 * caller so assertions can inspect the on-the-wire request shape.
 */
function captureNextBatch(
  jmap: { batch: ReturnType<typeof vi.fn> },
  responsesByName: Record<string, Array<unknown>>,
): { calls: CapturedCall[] } {
  const captured: { calls: CapturedCall[] } = { calls: [] };
  vi.mocked(jmap.batch).mockImplementationOnce(async (builder: unknown) => {
    let counter = 0;
    const using = new Set<string>();
    const builderApi = {
      call: (name: string, args: Record<string, unknown>, u: string[] = []) => {
        const id = `c${counter++}`;
        captured.calls.push({ name, args, callId: id });
        for (const cap of u) using.add(cap);
        return {
          ref: (path: string) => ({ resultOf: id, name, path }),
        };
      },
      calls: () => [],
      usingSet: () => using,
    };
    (builder as (b: unknown) => void)(builderApi);

    // Build response invocations in the order the test specified.
    // For methods used multiple times, pop responses one-by-one so a
    // batch with two Foo/get calls receives two distinct responses.
    const responseQueue: Record<string, Array<unknown>> = {};
    for (const k of Object.keys(responsesByName)) {
      responseQueue[k] = [...responsesByName[k]!];
    }
    const responses: Array<[string, unknown, string]> = [];
    for (const c of captured.calls) {
      const queue = responseQueue[c.name];
      if (!queue || queue.length === 0) {
        throw new Error(
          `test setup: no canned response for ${c.name} (callId ${c.callId})`,
        );
      }
      const body = queue.shift();
      responses.push([c.name, body, c.callId]);
    }
    return { responses, sessionState: 'ss-test' };
  });
  return captured;
}

/**
 * Walk a JSON pointer (RFC 6901, restricted to the subset JMAP uses
 * for back-references: leading "/", "/" segments, no "~0/~1" escapes
 * needed for top-level field names like "created" / "updated").
 */
function evalPointer(target: unknown, pointer: string): unknown {
  if (pointer === '') return target;
  if (pointer[0] !== '/') {
    throw new Error(`invalid JSON pointer: ${pointer}`);
  }
  const segments = pointer.slice(1).split('/');
  let cur: unknown = target;
  for (const seg of segments) {
    if (cur === null || typeof cur !== 'object') {
      throw new Error(`pointer ${pointer}: cannot descend into ${typeof cur}`);
    }
    cur = (cur as Record<string, unknown>)[seg];
    if (cur === undefined) {
      throw new Error(`pointer ${pointer}: missing key "${seg}"`);
    }
  }
  return cur;
}

/**
 * For each captured call that carries a `#ids` back-reference,
 * resolve that reference against the canned /changes response in the
 * batch and assert the path exists. This is the same shape check the
 * server performs in protojmap/envelope.go's resolveBackReferences.
 */
function assertBackReferencesResolve(
  calls: CapturedCall[],
  changesResponse: Record<string, unknown>,
  changesCallName: string,
): { resolvedPaths: string[] } {
  const resolved: string[] = [];
  for (const c of calls) {
    const idsRef = (c.args['#ids'] as
      | { resultOf: string; name: string; path: string }
      | undefined);
    if (!idsRef) continue;
    expect(idsRef.name).toBe(changesCallName);
    expect(['created', 'updated']).toContain(idsRef.path.replace(/^\//, ''));
    const value = evalPointer(changesResponse, idsRef.path);
    expect(Array.isArray(value)).toBe(true);
    resolved.push(idsRef.path);
  }
  return { resolvedPaths: resolved };
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

describe('ChatStore EventSource sync — back-reference shape', () => {
  let chatMod: typeof import('./store.svelte');
  let jmapMod: typeof import('../jmap/client');

  beforeEach(async () => {
    vi.clearAllMocks();
    // syncHandlers is intentionally NOT cleared: the chat singleton is
    // constructed once at module import and registers its sync.on
    // handlers at that point. Clearing them between tests would leave
    // every test after the first without a handler to drive.
    chatMod = await import('./store.svelte');
    jmapMod = await import('../jmap/client');

    chatMod.chat.conversations.clear();
    chatMod.chat.conversationIds = [];
    chatMod.chat.messages = [];
    chatMod.chat.overlayMessages.clear();
    chatMod.chat.overlayMessages = new Map();
    chatMod.chat.conversationsStatus = 'idle';
    chatMod.chat.messagesStatus = 'idle';
    chatMod.chat.openConversationId = null;
  });

  it('Conversation/changes back-reference paths resolve to keys in the response', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    // Seed #conversationState by issuing a successful loadConversations.
    captureNextBatch(jmap as never, {
      'Conversation/query': [{ ids: [], total: 0 }],
      'Conversation/get': [{ state: '5', list: [], notFound: [] }],
    });
    await chat.loadConversations();

    // Now drive the EventSource handler with a fabricated state.
    const changesBody = {
      accountId: 'acc1',
      oldState: '5',
      newState: '6',
      hasMoreChanges: false,
      created: ['c1'],
      updated: ['c2'],
      destroyed: [],
    };
    const captured = captureNextBatch(jmap as never, {
      'Conversation/changes': [changesBody],
      'Conversation/get': [
        { state: '6', list: [], notFound: [] },
        { state: '6', list: [], notFound: [] },
      ],
    });

    const handler = syncHandlers.get('Conversation');
    expect(handler).toBeDefined();
    await handler!('6', 'acc1');

    // Wait for the async chain to settle.
    await Promise.resolve();
    await Promise.resolve();

    // The batch must contain exactly one /changes and at least one /get.
    const changesCalls = captured.calls.filter(
      (c) => c.name === 'Conversation/changes',
    );
    const getCalls = captured.calls.filter(
      (c) => c.name === 'Conversation/get',
    );
    expect(changesCalls).toHaveLength(1);
    expect(getCalls.length).toBeGreaterThanOrEqual(1);

    const { resolvedPaths } = assertBackReferencesResolve(
      getCalls,
      changesBody,
      'Conversation/changes',
    );
    // Both buckets must be fetched so created and updated land in cache.
    expect(resolvedPaths).toContain('/created');
    expect(resolvedPaths).toContain('/updated');
  });

  it('Message/changes back-reference paths resolve to keys in the response', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    // Seed #messageState by issuing a successful loadMessages.
    captureNextBatch(jmap as never, {
      'Message/query': [{ ids: [], total: 0 }],
      'Message/get': [{ state: '11', list: [], notFound: [] }],
    });
    await chat.loadMessages('conv-1');

    const changesBody = {
      accountId: 'acc1',
      oldState: '11',
      newState: '12',
      hasMoreChanges: false,
      created: ['m1'],
      updated: ['m2'],
      destroyed: ['m3'],
    };
    const captured = captureNextBatch(jmap as never, {
      'Message/changes': [changesBody],
      'Message/get': [
        { state: '12', list: [], notFound: [] },
        { state: '12', list: [], notFound: [] },
      ],
    });

    const handler = syncHandlers.get('Message');
    expect(handler).toBeDefined();
    await handler!('12', 'acc1');
    await Promise.resolve();
    await Promise.resolve();

    const getCalls = captured.calls.filter(
      (c) => c.name === 'Message/get',
    );
    expect(getCalls.length).toBeGreaterThanOrEqual(1);
    const { resolvedPaths } = assertBackReferencesResolve(
      getCalls,
      changesBody,
      'Message/changes',
    );
    expect(resolvedPaths).toContain('/created');
    expect(resolvedPaths).toContain('/updated');
  });

  it('Membership push seeds #membershipState, then resolves /changes back-refs', async () => {
    const { jmap } = jmapMod;

    // First push: state is null. The handler must issue Membership/get
    // (no /changes yet) to seed the baseline.
    const seedCaptured = captureNextBatch(jmap as never, {
      'Membership/get': [{ state: '20', list: [] }],
    });

    const handler = syncHandlers.get('Membership');
    expect(handler).toBeDefined();
    await handler!('20', 'acc1');
    await Promise.resolve();

    expect(seedCaptured.calls).toHaveLength(1);
    expect(seedCaptured.calls[0]!.name).toBe('Membership/get');

    // Second push: now there's a baseline; the handler runs the
    // /changes + /get-bucket pattern.
    const changesBody = {
      accountId: 'acc1',
      oldState: '20',
      newState: '21',
      hasMoreChanges: false,
      created: [],
      updated: ['mem1'],
      destroyed: [],
    };
    const captured = captureNextBatch(jmap as never, {
      'Membership/changes': [changesBody],
      'Membership/get': [
        { state: '21', list: [] },
        { state: '21', list: [] },
      ],
    });

    await handler!('21', 'acc1');
    await Promise.resolve();
    await Promise.resolve();

    const getCalls = captured.calls.filter(
      (c) => c.name === 'Membership/get',
    );
    expect(getCalls.length).toBeGreaterThanOrEqual(1);
    const { resolvedPaths } = assertBackReferencesResolve(
      getCalls,
      changesBody,
      'Membership/changes',
    );
    expect(resolvedPaths).toContain('/created');
    expect(resolvedPaths).toContain('/updated');
  });

  it('rejects /changed as a back-reference path (regression for issue #47)', async () => {
    // This test would have caught the bug where the client referenced
    // `/changed` — a path that does not exist in the JMAP /changes
    // response. We assert that the actual store does NOT use that path.
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    captureNextBatch(jmap as never, {
      'Conversation/query': [{ ids: [], total: 0 }],
      'Conversation/get': [{ state: '5', list: [], notFound: [] }],
    });
    await chat.loadConversations();

    const captured = captureNextBatch(jmap as never, {
      'Conversation/changes': [
        {
          oldState: '5',
          newState: '6',
          hasMoreChanges: false,
          created: [],
          updated: [],
          destroyed: [],
        },
      ],
      'Conversation/get': [
        { state: '6', list: [], notFound: [] },
        { state: '6', list: [], notFound: [] },
      ],
    });

    const handler = syncHandlers.get('Conversation');
    await handler!('6', 'acc1');
    await Promise.resolve();
    await Promise.resolve();

    for (const c of captured.calls) {
      const idsRef = c.args['#ids'] as { path?: string } | undefined;
      if (idsRef?.path !== undefined) {
        expect(idsRef.path).not.toBe('/changed');
      }
    }
  });
});
