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
        senderPrincipalId: 'p2',
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
        senderPrincipalId: 'system',
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
  displayName: 'Alice',
  joinedAt: '2024-01-01T10:00:00Z',
  notificationsMuted: false,
};
const memberBob: Membership = {
  id: 'mem2',
  conversationId: 'c1',
  principalId: 'p2',
  role: 'member',
  displayName: 'Bob',
  joinedAt: '2024-01-01T10:00:00Z',
  notificationsMuted: false,
};

const baseConversation: Conversation = {
  id: 'c1',
  kind: 'dm',
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

  it('labels a sender by member.displayName, not the literal "Member"', () => {
    const { container } = render(MessageList, {
      props: { conversationId: 'c1', conversation: baseConversation },
    });
    const senderLabels = container.querySelectorAll('.sender-name');
    // m1 was sent by p2 (Bob); the label must read "Bob", not "Member".
    const labelTexts = Array.from(senderLabels).map((n) => n.textContent?.trim());
    expect(labelTexts).toContain('Bob');
    expect(labelTexts).not.toContain('Member');
  });

  it('falls back to "Member" only when the sender is unknown to the member list', () => {
    const stranger: Conversation = {
      ...baseConversation,
      kind: 'space',
      // Members list does not include p2, so the senderId resolution
      // for m1 has nothing to look up.
      members: [memberAlice],
    };
    const { container } = render(MessageList, {
      props: { conversationId: 'c1', conversation: stranger },
    });
    const senderLabels = Array.from(
      container.querySelectorAll('.sender-name'),
    ).map((n) => n.textContent?.trim());
    expect(senderLabels).toContain('Member');
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
