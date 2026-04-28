/**
 * Component test: ChatRail renders conversation list and triggers overlay
 * on click.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// -- mocks before component import --
// vi.mock factories are hoisted before variable declarations; all data
// must be defined inside the factory or via vi.fn() inline.

vi.mock('./store.svelte', () => ({
  chat: {
    conversationIds: ['c1', 'c2'],
    conversations: new Map([
      [
        'c1',
        {
          id: 'c1',
          type: 'dm',
          name: 'Alice',
          members: [
            { id: 'mem1', conversationId: 'c1', principalId: 'self', role: 'member', joinedAt: '2024-01-01T10:00:00Z', notificationsMuted: false },
            { id: 'mem2', conversationId: 'c1', principalId: 'p2',   role: 'member', joinedAt: '2024-01-01T10:00:00Z', notificationsMuted: false },
          ],
          createdAt: '2024-01-01T10:00:00Z',
          lastMessageAt: '2024-01-02T10:00:00Z',
          lastMessagePreview: 'Hello',
          pinned: false,
          muted: false,
          unreadCount: 2,
        },
      ],
      [
        'c2',
        {
          id: 'c2',
          type: 'space',
          name: 'General',
          members: [],
          createdAt: '2024-01-01T10:00:00Z',
          pinned: false,
          muted: false,
          unreadCount: 0,
        },
      ],
    ]),
    presence: new Map([['p2', 'online']]),
    conversationsStatus: 'ready',
  },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: { principalId: 'self' },
}));

vi.mock('./overlay-store.svelte', () => ({
  chatOverlay: {
    openWindow: vi.fn(),
    isOpen: vi.fn(() => false),
  },
}));

import ChatRail from './ChatRail.svelte';
import { chatOverlay } from './overlay-store.svelte';

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(chatOverlay.isOpen).mockReturnValue(false);
});

describe('ChatRail', () => {
  it('renders in collapsed state by default', () => {
    const { container } = render(ChatRail);
    const aside = container.querySelector('.chat-rail');
    expect(aside).not.toHaveClass('expanded');
  });

  it('shows avatars for all conversations in collapsed state', () => {
    render(ChatRail);
    // In collapsed state the conv-name text is not rendered; buttons have
    // aria-label containing the name.
    const aliceBtn = screen.getByRole('button', { name: /Alice/ });
    const generalBtn = screen.getByRole('button', { name: /General/ });
    expect(aliceBtn).toBeInTheDocument();
    expect(generalBtn).toBeInTheDocument();
  });

  it('expands when the toggle button is clicked', async () => {
    const { container } = render(ChatRail);
    const toggle = screen.getByRole('button', { name: /Expand chat rail/i });
    await fireEvent.click(toggle);
    const aside = container.querySelector('.chat-rail');
    expect(aside).toHaveClass('expanded');
  });

  it('shows conversation names when expanded', async () => {
    render(ChatRail);
    const toggle = screen.getByRole('button', { name: /Expand chat rail/i });
    await fireEvent.click(toggle);
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('General')).toBeInTheDocument();
  });

  it('shows unread badge for conversation with unread count', () => {
    render(ChatRail);
    const aliceBtn = screen.getByRole('button', { name: /2 unread/i });
    expect(aliceBtn).toBeInTheDocument();
  });

  it('calls chatOverlay.openWindow on click', async () => {
    render(ChatRail);
    const aliceBtn = screen.getByRole('button', { name: /Alice/ });
    await fireEvent.click(aliceBtn);
    expect(chatOverlay.openWindow).toHaveBeenCalledWith('c1');
  });

  it('marks the button active when the conversation is already in an overlay', () => {
    vi.mocked(chatOverlay.isOpen).mockImplementation((id) => id === 'c1');
    const { container } = render(ChatRail);
    const items = container.querySelectorAll('.conv-item');
    // First item is Alice (c1) — should have .active class.
    expect(items[0]).toHaveClass('active');
    // Second item is General (c2) — should not.
    expect(items[1]).not.toHaveClass('active');
  });
});
