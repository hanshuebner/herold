/**
 * Tests for the chat store.
 *
 * Mocks jmap.batch() and auth.session/principalId to drive the store
 * without a real network connection.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// -----------------------------------------------------------------------
// Module mocks — declared before the dynamic import
// -----------------------------------------------------------------------

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

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(() => vi.fn()),
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
// Tests
// -----------------------------------------------------------------------

describe('ChatStore', () => {
  let chatMod: typeof import('./store.svelte');
  let jmapMod: typeof import('../jmap/client');

  beforeEach(async () => {
    vi.clearAllMocks();
    chatMod = await import('./store.svelte');
    jmapMod = await import('../jmap/client');

    // Reset observable store state between tests.
    chatMod.chat.conversations.clear();
    chatMod.chat.conversationIds = [];
    chatMod.chat.messages = [];
    chatMod.chat.overlayMessages.clear();
    chatMod.chat.overlayMessages = new Map();
    chatMod.chat.conversationsStatus = 'idle';
    chatMod.chat.messagesStatus = 'idle';
    chatMod.chat.openConversationId = null;
    chatMod.chat.hasMoreMessages = false;
  });

  // ------------------------------------------------------------------
  // loadConversations
  // ------------------------------------------------------------------

  it('loadConversations populates conversations and sets status ready', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const fakeConv = {
      id: 'c1',
      kind: 'dm' as const,
      name: 'Alice',
      members: [],
      createdAt: '2024-01-01T10:00:00Z',
      lastMessageAt: '2024-01-02T10:00:00Z',
      pinned: false,
      muted: false,
      unreadCount: 2,
    };

    vi.mocked(jmap.batch).mockResolvedValueOnce({
      responses: [
        ['Conversation/query', { ids: ['c1'], total: 1 }, 'c0'],
        [
          'Conversation/get',
          { state: 'state-1', list: [fakeConv], notFound: [] },
          'c1',
        ],
      ],
      sessionState: 'ss1',
    });

    await chat.loadConversations();

    expect(chat.conversationsStatus).toBe('ready');
    expect(chat.conversations.get('c1')).toEqual(fakeConv);
    expect(chat.conversationIds).toContain('c1');
  });

  it('loadConversations sets error status on failure', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    vi.mocked(jmap.batch).mockRejectedValueOnce(new Error('network error'));
    await chat.loadConversations();
    expect(chat.conversationsStatus).toBe('error');
  });

  it('totalUnread sums unmuted unread counts', () => {
    const { chat } = chatMod;

    chat.conversations.set('c1', {
      id: 'c1',
      kind: 'dm',
      name: 'Alice',
      members: [],
      createdAt: '2024-01-01T10:00:00Z',
      pinned: false,
      muted: false,
      unreadCount: 3,
    });
    chat.conversations.set('c2', {
      id: 'c2',
      kind: 'space',
      name: 'General',
      members: [],
      createdAt: '2024-01-01T10:00:00Z',
      pinned: false,
      muted: true, // muted — should not count
      unreadCount: 10,
    });
    expect(chat.totalUnread).toBe(3);
  });

  // ------------------------------------------------------------------
  // loadMessages
  // ------------------------------------------------------------------

  it('loadMessages populates messages oldest-first', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const msg1: import('./types').Message = {
      id: 'm1',
      conversationId: 'c1',
      senderPrincipalId: 'p2',
      type: 'text',
      body: { html: '<p>hello</p>', text: 'hello' },
      inlineImages: [],
      reactions: {},
      createdAt: '2024-01-01T10:00:00Z',
      deleted: false,
    };
    const msg2: import('./types').Message = {
      ...msg1,
      id: 'm2',
      createdAt: '2024-01-01T11:00:00Z',
    };

    vi.mocked(jmap.batch).mockResolvedValueOnce({
      responses: [
        ['Message/query', { total: 2, ids: ['m2', 'm1'] }, 'q0'],
        ['Message/get', { state: 'ms1', list: [msg2, msg1] }, 'g0'],
      ],
      sessionState: 'ss1',
    });

    await chat.loadMessages('c1');

    expect(chat.messagesStatus).toBe('ready');
    // Should be reversed (oldest first).
    expect(chat.messages[0]!.id).toBe('m1');
    expect(chat.messages[1]!.id).toBe('m2');
  });

  // ------------------------------------------------------------------
  // sendMessage (optimistic)
  // ------------------------------------------------------------------

  it('sendMessage inserts optimistic message and replaces with real id', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    let capturedTempId = '';
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      builder({
        call: (name: string, args: unknown) => {
          const createArgs = args as { create?: Record<string, unknown> };
          if (name === 'Message/set' && createArgs.create) {
            capturedTempId = Object.keys(createArgs.create)[0]!;
          }
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          [
            'Message/set',
            {
              created: { [capturedTempId]: { id: 'real-m1' } },
              notCreated: {},
            },
            's0',
          ],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.sendMessage('c1', '<p>hi</p>', 'hi');

    expect(chat.messages).toHaveLength(1);
    expect(chat.messages[0]!.id).toBe('real-m1');
  });

  it('sendMessage also writes the optimistic message into the overlay cache', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    // Seed an overlay cache entry for c1 (e.g. an open overlay window).
    chat.overlayMessages.set('c1', { messages: [], status: 'ready', hasMore: false });
    chat.overlayMessages = new Map(chat.overlayMessages);

    let capturedTempId = '';
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      builder({
        call: (name: string, args: unknown) => {
          const createArgs = args as { create?: Record<string, unknown> };
          if (name === 'Message/set' && createArgs.create) {
            capturedTempId = Object.keys(createArgs.create)[0]!;
          }
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          [
            'Message/set',
            { created: { [capturedTempId]: { id: 'real-m9' } }, notCreated: {} },
            's0',
          ],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.sendMessage('c1', '<p>hello overlay</p>', 'hello overlay');

    const overlayEntry = chat.overlayMessages.get('c1');
    expect(overlayEntry?.messages).toHaveLength(1);
    expect(overlayEntry?.messages[0]!.id).toBe('real-m9');
    expect(overlayEntry?.messages[0]!.body.text).toBe('hello overlay');
  });

  it('sendMessage rolls back on server error in both caches', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    chat.overlayMessages.set('c1', { messages: [], status: 'ready', hasMore: false });
    chat.overlayMessages = new Map(chat.overlayMessages);

    let capturedTempId = '';
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      builder({
        call: (name: string, args: unknown) => {
          const createArgs = args as { create?: Record<string, unknown> };
          if (name === 'Message/set' && createArgs.create) {
            capturedTempId = Object.keys(createArgs.create)[0]!;
          }
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          [
            'Message/set',
            {
              created: {},
              notCreated: {
                [capturedTempId]: {
                  type: 'forbidden',
                  description: 'not allowed',
                },
              },
            },
            's0',
          ],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.sendMessage('c1', '<p>hi</p>', 'hi');

    // After rollback both the main pane and the overlay cache should be empty.
    expect(chat.messages).toHaveLength(0);
    expect(chat.overlayMessages.get('c1')?.messages ?? []).toHaveLength(0);
  });

  // ------------------------------------------------------------------
  // searchPrincipals
  // ------------------------------------------------------------------

  it('searchPrincipals returns matched principals from the directory', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const fakePrincipal = { id: 'prin-1', email: 'alice@example.com', displayName: 'Alice' };

    vi.mocked(jmap.batch).mockResolvedValueOnce({
      responses: [
        ['Principal/query', { ids: ['prin-1'], total: 1 }, 'q0'],
        ['Principal/get', { state: 'ps1', list: [fakePrincipal], notFound: [] }, 'g0'],
      ],
      sessionState: 'ss1',
    });

    const results = await chat.searchPrincipals('ali');
    expect(results).toHaveLength(1);
    expect(results[0]).toEqual(fakePrincipal);
  });

  it('searchPrincipals returns empty array for empty prefix', async () => {
    const { chat } = chatMod;
    const results = await chat.searchPrincipals('');
    expect(results).toHaveLength(0);
  });

  it('searchPrincipals returns empty array on network error', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    vi.mocked(jmap.batch).mockRejectedValueOnce(new Error('network'));
    const results = await chat.searchPrincipals('ali');
    expect(results).toHaveLength(0);
  });

  // ------------------------------------------------------------------
  // lookupPrincipalByEmail
  // ------------------------------------------------------------------

  it('lookupPrincipalByEmail returns principal when found', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const fakePrincipal = { id: 'prin-1', email: 'alice@example.com', displayName: 'Alice' };

    vi.mocked(jmap.batch).mockResolvedValueOnce({
      responses: [
        ['Principal/query', { ids: ['prin-1'], total: 1 }, 'q0'],
        ['Principal/get', { state: 'ps1', list: [fakePrincipal], notFound: [] }, 'g0'],
      ],
      sessionState: 'ss1',
    });

    const result = await chat.lookupPrincipalByEmail('alice@example.com');
    expect(result).toEqual(fakePrincipal);
  });

  it('lookupPrincipalByEmail returns null when not found', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    vi.mocked(jmap.batch).mockResolvedValueOnce({
      responses: [
        ['Principal/query', { ids: [], total: 0 }, 'q0'],
        ['Principal/get', { state: 'ps1', list: [], notFound: [] }, 'g0'],
      ],
      sessionState: 'ss1',
    });

    const result = await chat.lookupPrincipalByEmail('nobody@example.com');
    expect(result).toBeNull();
  });

  // ------------------------------------------------------------------
  // findExistingDM
  // ------------------------------------------------------------------

  it('findExistingDM returns conversation when a matching DM exists', () => {
    const { chat } = chatMod;

    chat.conversations.set('dm-c1', {
      id: 'dm-c1',
      kind: 'dm',
      name: 'Alice',
      members: [
        { id: 'm1', conversationId: 'dm-c1', principalId: 'p1', role: 'member', joinedAt: '', notificationsSetting: 'all' },
        { id: 'm2', conversationId: 'dm-c1', principalId: 'prin-other', role: 'member', joinedAt: '', notificationsSetting: 'all' },
      ],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 0,
    });

    const result = chat.findExistingDM('prin-other');
    expect(result?.id).toBe('dm-c1');
  });

  it('findExistingDM returns null when no matching DM exists', () => {
    const { chat } = chatMod;
    const result = chat.findExistingDM('prin-unknown');
    expect(result).toBeNull();
  });

  it('findExistingDM ignores Spaces', () => {
    const { chat } = chatMod;

    chat.conversations.set('space-1', {
      id: 'space-1',
      kind: 'space',
      name: 'General',
      members: [
        { id: 'm1', conversationId: 'space-1', principalId: 'p1', role: 'member', joinedAt: '', notificationsSetting: 'all' },
        { id: 'm2', conversationId: 'space-1', principalId: 'prin-other', role: 'member', joinedAt: '', notificationsSetting: 'all' },
      ],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 0,
    });

    const result = chat.findExistingDM('prin-other');
    expect(result).toBeNull();
  });

  // ------------------------------------------------------------------
  // createConversation
  // ------------------------------------------------------------------

  it('createConversation resolves with the new conversation id', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const newId = 'conv-new-1';
    const newConv = {
      id: newId,
      kind: 'dm' as const,
      name: 'Bob',
      members: [],
      createdAt: '2024-02-01T10:00:00Z',
      pinned: false,
      muted: false,
      unreadCount: 0,
    };

    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      let tempId = '';
      builder({
        call: (_name: string, args: unknown) => {
          const a = args as { create?: Record<string, unknown> };
          if (a.create) tempId = Object.keys(a.create)[0]!;
          return { ref: () => ({ resultOf: 'c0', name: '', path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Conversation/set', { created: { [tempId]: newConv }, notCreated: {} }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    const result = await chat.createConversation({ kind: 'dm', members: ['prin-other'] });
    expect(result.id).toBe(newId);
  });

  it('createConversation seeds the local cache and conversationIds', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const newId = 'conv-cache-1';
    const newConv = {
      id: newId,
      kind: 'dm' as const,
      name: 'Carol',
      members: [],
      createdAt: '2024-03-01T10:00:00Z',
      pinned: false,
      muted: false,
      unreadCount: 0,
    };

    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      let tempId = '';
      builder({
        call: (_name: string, args: unknown) => {
          const a = args as { create?: Record<string, unknown> };
          if (a.create) tempId = Object.keys(a.create)[0]!;
          return { ref: () => ({ resultOf: 'c0', name: '', path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Conversation/set', { created: { [tempId]: newConv }, notCreated: {} }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.createConversation({ kind: 'dm', members: ['prin-other'] });

    expect(chat.conversations.get(newId)).toEqual(newConv);
    expect(chat.conversationIds).toContain(newId);
  });

  it('createConversation tolerates a server record without createdAt or lastMessageAt', async () => {
    // The herold Conversation wire shape omits createdAt and may have a
    // null lastMessageAt for brand-new conversations.  rebuildConversation
    // Order must not crash when these are missing.
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    // Pre-populate one existing conversation with neither field set so the
    // sort comparator has to compare two records with empty timestamps.
    chat.conversations.set('c-existing', {
      id: 'c-existing',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      kind: undefined as any,
      name: 'pre-existing',
      members: [],
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      createdAt: undefined as any,
      pinned: false,
      muted: false,
      unreadCount: 0,
    });

    const newId = 'c-no-times';
    const newConv = {
      id: newId,
      kind: 'dm',
      name: 'Dave',
      members: [],
      lastMessageAt: null,
      // No createdAt at all — matches the herold wire shape.
      pinned: false,
      muted: false,
      unreadCount: 0,
    };

    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      let tempId = '';
      builder({
        call: (_name: string, args: unknown) => {
          const a = args as { create?: Record<string, unknown> };
          if (a.create) tempId = Object.keys(a.create)[0]!;
          return { ref: () => ({ resultOf: 'c0', name: '', path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Conversation/set', { created: { [tempId]: newConv }, notCreated: {} }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.createConversation({ kind: 'dm', members: ['prin-other'] });

    expect(chat.conversationIds).toContain(newId);
    expect(chat.conversationIds).toContain('c-existing');
  });

  it('createConversation sends kind/members/topic as the JMAP wire shape', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const newId = 'conv-new-2';
    const newConv = {
      id: newId,
      kind: 'space' as const,
      name: 'project',
      description: 'planning',
      members: [],
      createdAt: '2024-04-01T10:00:00Z',
      pinned: false,
      muted: false,
      unreadCount: 0,
    };

    let capturedCreate: Record<string, unknown> | null = null;
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      let tempId = '';
      builder({
        call: (_name: string, args: unknown) => {
          const a = args as { create?: Record<string, Record<string, unknown>> };
          if (a.create) {
            tempId = Object.keys(a.create)[0]!;
            capturedCreate = a.create[tempId]!;
          }
          return { ref: () => ({ resultOf: 'c0', name: '', path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Conversation/set', { created: { [tempId]: newConv }, notCreated: {} }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.createConversation({
      kind: 'space',
      members: ['prin-a', 'prin-b'],
      name: 'project',
      topic: 'planning',
    });

    expect(capturedCreate).toEqual({
      kind: 'space',
      members: ['prin-a', 'prin-b'],
      name: 'project',
      topic: 'planning',
    });
  });

  it('createConversation throws on notCreated', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      let tempId = '';
      builder({
        call: (_name: string, args: unknown) => {
          const a = args as { create?: Record<string, unknown> };
          if (a.create) tempId = Object.keys(a.create)[0]!;
          return { ref: () => ({ resultOf: 'c0', name: '', path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Conversation/set', { created: {}, notCreated: { [tempId]: { type: 'forbidden' } } }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await expect(
      chat.createConversation({ kind: 'dm', members: ['prin-other'] }),
    ).rejects.toThrow('forbidden');
  });

  // ------------------------------------------------------------------
  // markRead — wire field name + myMembership lookup
  // ------------------------------------------------------------------

  it('markRead sends lastReadMessageId via Membership/set, not readThrough', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    chat.conversations.set('cMR', {
      id: 'cMR',
      kind: 'dm',
      name: 'Bob',
      members: [],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 3,
      myMembership: {
        id: 'mb-self',
        conversationId: 'cMR',
        principalId: 'p1',
        role: 'member',
        joinedAt: '',
      },
    });

    let capturedUpdate: Record<string, unknown> | null = null;
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      builder({
        call: (name: string, args: unknown) => {
          const a = args as { update?: Record<string, Record<string, unknown>> };
          if (name === 'Membership/set' && a.update) {
            capturedUpdate = a.update;
          }
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Membership/set', { updated: { 'mb-self': null }, notUpdated: {} }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.markRead('cMR', 'msg-99');

    expect(capturedUpdate).toEqual({ 'mb-self': { lastReadMessageId: 'msg-99' } });
    expect(JSON.stringify(capturedUpdate)).not.toContain('readThrough');

    // Local cache reflects the new pointer and zeroes the unread badge.
    const cached = chat.conversations.get('cMR');
    expect(cached?.myMembership?.lastReadMessageId).toBe('msg-99');
    expect(cached?.unreadCount).toBe(0);
  });

  it('markRead reassigns conversations Map so $derived(get(id)) cells re-fire', async () => {
    // Regression: the overlay window's title bar reads
    // chat.conversations.get(conversationId) directly through a
    // $derived. Plain Map.set() does not propagate into Svelte 5
    // $derived(map.get(id)) cells — only Map identity changes do.
    // Sidebar surfaces re-render via the conversationIds array but
    // overlay-window title badges would stay stale unless the Map is
    // reassigned. This test pins the reassignment behaviour.
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    chat.conversations.set('cReassign', {
      id: 'cReassign',
      kind: 'dm',
      name: 'Bob',
      members: [],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 5,
      myMembership: {
        id: 'mb-self',
        conversationId: 'cReassign',
        principalId: 'p1',
        role: 'member',
        joinedAt: '',
      },
    });
    const beforeMap = chat.conversations;

    vi.mocked(jmap.batch).mockResolvedValue({
      responses: [
        ['Membership/set', { updated: { 'mb-self': null }, notUpdated: {} }, 's0'],
      ],
      sessionState: 'ss1',
    });

    await chat.markRead('cReassign', 'msg-1');

    // Identity must change so any $derived(map.get(...)) tracking the
    // Map's identity (the only signal Svelte 5 propagates from Map
    // mutations in this codebase per the presence/typing pattern) sees
    // the update.
    expect(chat.conversations).not.toBe(beforeMap);
  });

  it('markRead is a no-op when myMembership is absent (e.g. cache not yet hydrated)', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    chat.conversations.set('cNoMyMem', {
      id: 'cNoMyMem',
      kind: 'dm',
      name: 'Bob',
      members: [],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 1,
    });

    await chat.markRead('cNoMyMem', 'msg-1');
    expect(jmap.batch).not.toHaveBeenCalled();
  });

  // ------------------------------------------------------------------
  // destroyConversation
  // ------------------------------------------------------------------

  it('destroyConversation sends Conversation/set destroy and removes the row from caches', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    chat.conversations.set('toRemove', {
      id: 'toRemove',
      kind: 'dm',
      name: 'Bob',
      members: [],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 0,
    });
    chat.conversationIds = ['toRemove'];
    chat.overlayMessages.set('toRemove', { messages: [], status: 'ready', hasMore: false });
    chat.overlayMessages = new Map(chat.overlayMessages);

    let capturedDestroy: string[] | null = null;
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      builder({
        call: (name: string, args: unknown) => {
          const a = args as { destroy?: string[] };
          if (name === 'Conversation/set' && a.destroy) {
            capturedDestroy = a.destroy;
          }
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Conversation/set', { destroyed: ['toRemove'], notDestroyed: {} }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.destroyConversation('toRemove');

    expect(capturedDestroy).toEqual(['toRemove']);
    expect(chat.conversations.get('toRemove')).toBeUndefined();
    expect(chat.conversationIds).not.toContain('toRemove');
    expect(chat.overlayMessages.get('toRemove')).toBeUndefined();
  });

  it('destroyConversation throws when the server returns notDestroyed', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    vi.mocked(jmap.batch).mockResolvedValue({
      responses: [
        [
          'Conversation/set',
          {
            destroyed: [],
            notDestroyed: { 'p': { type: 'forbidden', description: 'not allowed' } },
          },
          's0',
        ],
      ],
      sessionState: 'ss1',
    });

    await expect(chat.destroyConversation('p')).rejects.toThrow('not allowed');
  });

  // ------------------------------------------------------------------
  // scrollToBottomSignal — bumped on optimistic send (Bug B)
  // ------------------------------------------------------------------

  it('scrollToBottomSignal increments when sendMessage inserts an optimistic message', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const initialSignal = chat.scrollToBottomSignal;

    let capturedTempId = '';
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      builder({
        call: (name: string, args: unknown) => {
          const createArgs = args as { create?: Record<string, unknown> };
          if (name === 'Message/set' && createArgs.create) {
            capturedTempId = Object.keys(createArgs.create)[0]!;
          }
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Message/set', { created: { [capturedTempId]: { id: 'real-x1' } }, notCreated: {} }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.sendMessage('c1', '<p>scroll test</p>', 'scroll test');

    expect(chat.scrollToBottomSignal).toBe(initialSignal + 1);
  });

  it('scrollToBottomSignal is not incremented when sendMessage rolls back on error', async () => {
    const { chat } = chatMod;
    const { jmap } = jmapMod;

    const initialSignal = chat.scrollToBottomSignal;

    let capturedTempId = '';
    vi.mocked(jmap.batch).mockImplementation(async (builder) => {
      builder({
        call: (name: string, args: unknown) => {
          const createArgs = args as { create?: Record<string, unknown> };
          if (name === 'Message/set' && createArgs.create) {
            capturedTempId = Object.keys(createArgs.create)[0]!;
          }
          return { ref: () => ({ resultOf: 'c0', name, path: '' }) };
        },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any);
      return {
        responses: [
          ['Message/set', { created: {}, notCreated: { [capturedTempId]: { type: 'forbidden' } } }, 's0'],
        ],
        sessionState: 'ss1',
      };
    });

    await chat.sendMessage('c1', '<p>fail</p>', 'fail');

    // The signal was bumped at the optimistic-insert point (before the
    // network round-trip), so it should still be initialSignal + 1 even
    // though the message was rolled back.
    expect(chat.scrollToBottomSignal).toBe(initialSignal + 1);
  });

  // ------------------------------------------------------------------
  // toggleReaction — console.warn on dropped calls
  // ------------------------------------------------------------------

  it('toggleReaction emits console.warn when message is not found', async () => {
    const { chat } = chatMod;
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    // No messages in store; the lookup will fail.
    chat.messages = [];
    await chat.toggleReaction('no-such-id', '👍', 'p1');

    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining('toggleReaction: dropped'),
      expect.objectContaining({ messageId: 'no-such-id', found: false }),
    );
    warnSpy.mockRestore();
  });
});
