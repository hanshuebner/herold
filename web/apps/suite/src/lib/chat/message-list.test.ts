/**
 * Component test: MessageList renders text messages, system messages,
 * reactions, and the typing indicator.
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import type { Conversation, Membership } from './types';

vi.mock('../auth/auth.svelte', () => ({
  auth: { principalId: 'p1' },
}));

vi.mock('./store.svelte', () => ({
  chat: {
    messages: [
      {
        id: 'm1',
        conversationId: 'c1',
        senderId: 'p2',
        type: 'text',
        body: { html: '<p>Hello from Alice</p>', text: 'Hello from Alice' },
        inlineImages: [],
        reactions: { '\u{1F44D}': ['p1', 'p2'] },
        createdAt: '2024-01-01T10:00:00Z',
        deleted: false,
      },
      {
        id: 'm2',
        conversationId: 'c1',
        senderId: 'system',
        type: 'system',
        body: {
          html: '<p>Alice joined the space</p>',
          text: 'Alice joined the space',
        },
        inlineImages: [],
        reactions: {},
        createdAt: '2024-01-01T10:01:00Z',
        deleted: false,
      },
    ],
    messagesStatus: 'ready',
    hasMoreMessages: false,
    typing: new Map(),
    memberships: new Map(),
    loadMoreMessages: vi.fn(),
    markRead: vi.fn(),
    toggleReaction: vi.fn(),
  },
}));

vi.mock('../mail/EmojiPicker.svelte', () => ({
  default: { render: () => {} },
}));

import MessageList from './MessageList.svelte';

const memberAlice: Membership = {
  id: 'mem1',
  conversationId: 'c1',
  principalId: 'p1',
  role: 'member',
  joinedAt: '2024-01-01T10:00:00Z',
  notificationsMuted: false,
};
const memberBob: Membership = {
  id: 'mem2',
  conversationId: 'c1',
  principalId: 'p2',
  role: 'member',
  joinedAt: '2024-01-01T10:00:00Z',
  notificationsMuted: false,
};

const baseConversation: Conversation = {
  id: 'c1',
  type: 'dm',
  name: 'Alice',
  members: [memberAlice, memberBob],
  createdAt: '2024-01-01T10:00:00Z',
  pinned: false,
  muted: false,
  unreadCount: 0,
};

describe('MessageList', () => {
  it('renders message body HTML', () => {
    render(MessageList, {
      props: { conversationId: 'c1', conversation: baseConversation },
    });
    expect(screen.getByText('Hello from Alice')).toBeInTheDocument();
  });

  it('renders system message text', () => {
    render(MessageList, {
      props: { conversationId: 'c1', conversation: baseConversation },
    });
    expect(screen.getByText('Alice joined the space')).toBeInTheDocument();
  });

  it('renders reaction chip with count', () => {
    render(MessageList, {
      props: { conversationId: 'c1', conversation: baseConversation },
    });
    // The chip shows the emoji + count 2.
    const chip = screen.getByRole('button', { name: /2 reactions/i });
    expect(chip).toBeInTheDocument();
  });

  it('marks own reaction chip with mine class', () => {
    render(MessageList, {
      props: { conversationId: 'c1', conversation: baseConversation },
    });
    const chip = screen.getByRole('button', { name: /2 reactions/i });
    expect(chip.classList.contains('mine')).toBe(true);
  });

  it('shows typing indicator when someone is typing', async () => {
    const { chat } = await import('./store.svelte');
    (chat.typing as Map<string, Set<string>>).set('c1', new Set(['p2']));

    render(MessageList, {
      props: { conversationId: 'c1', conversation: baseConversation },
    });
    expect(screen.getByText(/is typing/)).toBeInTheDocument();
    chat.typing.clear();
  });
});
