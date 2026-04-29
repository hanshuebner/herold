/**
 * Component test: ConversationList renders DMs and Spaces with unread badges.
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// Mock the chat store before importing the component.
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
              principalId: 'p1',
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
          lastMessagePreview: 'hey',
          pinned: false,
          muted: false,
          unreadCount: 3,
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
  },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: { principalId: 'p1' },
}));

import ConversationList from './ConversationList.svelte';

describe('ConversationList', () => {
  it('renders DM and Space entries', () => {
    render(ConversationList, {
      props: { onSelect: vi.fn() },
    });
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('General')).toBeInTheDocument();
  });

  it('shows unread badge for conversations with unread count', () => {
    render(ConversationList, {
      props: { onSelect: vi.fn() },
    });
    expect(screen.getByLabelText('3 unread')).toBeInTheDocument();
  });

  it('calls onSelect with conversation id on click', async () => {
    const onSelect = vi.fn();
    render(ConversationList, {
      props: { onSelect },
    });
    await fireEvent.click(screen.getByText('Alice'));
    expect(onSelect).toHaveBeenCalledWith('c1');
  });

  it('marks active conversation with aria-current', () => {
    render(ConversationList, {
      props: { onSelect: vi.fn(), activeId: 'c2' },
    });
    const generalBtn = screen.getByRole('button', { name: /General/i });
    expect(generalBtn).toHaveAttribute('aria-current', 'true');
  });
});
