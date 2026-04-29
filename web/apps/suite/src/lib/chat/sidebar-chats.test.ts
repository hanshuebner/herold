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
          kind: 'dm',
          name: 'Alice',
          members: [
            {
              id: 'mem1',
              conversationId: 'c1',
              principalId: 'self',
              role: 'member',
              joinedAt: '2024-01-01T10:00:00Z',
              notificationsSetting: 'all',
            },
            {
              id: 'mem2',
              conversationId: 'c1',
              principalId: 'p2',
              role: 'member',
              joinedAt: '2024-01-01T10:00:00Z',
              notificationsSetting: 'all',
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
          kind: 'space',
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
    requestComposeFocus: vi.fn(),
    destroyConversation: vi.fn(),
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

vi.mock('./new-chat-picker.svelte', () => ({
  newChatPicker: { open: vi.fn() },
}));

import SidebarChats from './SidebarChats.svelte';
import { chat } from './store.svelte';
import { chatOverlay } from './overlay-store.svelte';
import { newChatPicker } from './new-chat-picker.svelte';

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
    const aliceBtn = screen.getByRole('button', { name: 'Alice, 2 unread' });
    await fireEvent.click(aliceBtn);
    expect(chatOverlay.openWindow).toHaveBeenCalledWith('c1');
    expect(chat.requestComposeFocus).toHaveBeenCalledWith('c1');
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

  it('"+" button opens the new-chat picker in DM mode', async () => {
    render(SidebarChats);
    const newChatBtn = screen.getByRole('button', { name: /New chat/i });
    await fireEvent.click(newChatBtn);
    expect(newChatPicker.open).toHaveBeenCalledWith({ mode: 'dm' });
  });
});
