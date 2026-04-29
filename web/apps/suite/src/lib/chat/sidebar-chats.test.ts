/**
 * Component tests for SidebarChats — the sidebar chats section that
 * replaced ChatRail.  Tests are structured analogously to the old
 * chat-rail.test.ts; the component renders a capped conversation list,
 * opens overlays on click, and provides a "+" new-chat affordance.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── mocks (hoisted) ─────────────────────────────────────────────────────────

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
            {
              id: 'mem1',
              conversationId: 'c1',
              principalId: 'self',
              role: 'member',
              joinedAt: '2024-01-01T10:00:00Z',
              notificationsMuted: false,
            },
            {
              id: 'mem2',
              conversationId: 'c1',
              principalId: 'p2',
              role: 'member',
              joinedAt: '2024-01-01T10:00:00Z',
              notificationsMuted: false,
            },
          ],
          createdAt: '2024-01-01T10:00:00Z',
          lastMessageAt: '2024-01-02T10:00:00Z',
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

vi.mock('../dialog/prompt.svelte', () => ({
  prompt: { ask: vi.fn() },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

import SidebarChats from './SidebarChats.svelte';
import { chatOverlay } from './overlay-store.svelte';
import { toast } from '../toast/toast.svelte';
import { prompt } from '../dialog/prompt.svelte';

// ── tests ─────────────────────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(chatOverlay.isOpen).mockReturnValue(false);
});

describe('SidebarChats', () => {
  it('renders the Chats heading', () => {
    render(SidebarChats);
    expect(screen.getByText('Chats')).toBeInTheDocument();
  });

  it('renders conversation names from the chat store', () => {
    render(SidebarChats);
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('General')).toBeInTheDocument();
  });

  it('shows an unread badge for conversations with unread count', () => {
    render(SidebarChats);
    expect(screen.getByLabelText('2 unread')).toBeInTheDocument();
  });

  it('calls chatOverlay.openWindow when a conversation row is clicked', async () => {
    render(SidebarChats);
    const aliceBtn = screen.getByRole('button', { name: /Alice/ });
    await fireEvent.click(aliceBtn);
    expect(chatOverlay.openWindow).toHaveBeenCalledWith('c1');
  });

  it('marks the conv-item active when the overlay is open', () => {
    vi.mocked(chatOverlay.isOpen).mockImplementation((id) => id === 'c1');
    const { container } = render(SidebarChats);
    const items = container.querySelectorAll('.conv-item');
    expect(items[0]).toHaveClass('active');
    expect(items[1]).not.toHaveClass('active');
  });

  it('renders a "+" new-chat button', () => {
    render(SidebarChats);
    expect(screen.getByRole('button', { name: /New chat/i })).toBeInTheDocument();
  });

  it('"+" button shows a toast when there is no create implementation', async () => {
    vi.mocked(prompt.ask).mockResolvedValue('alice@example.com');
    render(SidebarChats);
    const newChatBtn = screen.getByRole('button', { name: /New chat/i });
    await fireEvent.click(newChatBtn);
    await new Promise((r) => setTimeout(r, 0));
    expect(toast.show).toHaveBeenCalledWith(
      expect.objectContaining({ message: 'Starting new chats is not yet supported' }),
    );
  });
});
