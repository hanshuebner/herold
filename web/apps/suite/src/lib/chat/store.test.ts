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
      type: 'dm' as const,
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
      type: 'dm',
      name: 'Alice',
      members: [],
      createdAt: '2024-01-01T10:00:00Z',
      pinned: false,
      muted: false,
      unreadCount: 3,
    });
    chat.conversations.set('c2', {
      id: 'c2',
      type: 'space',
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
      senderId: 'p2',
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

  it('sendMessage rolls back on server error', async () => {
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

    // After rollback the message list should be empty.
    expect(chat.messages).toHaveLength(0);
  });
});
