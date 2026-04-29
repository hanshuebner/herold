/**
 * Component tests for ChatOverlayWindow.
 *
 * Tests: title bar renders, minimize/expand toggle, close, Escape key,
 * aria attributes.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ---- mocks ----
// vi.mock factories are hoisted before variable declarations; all data must
// be defined inline.  Spy handles are retrieved via vi.mocked() after import.

vi.mock('./store.svelte', () => ({
  chat: {
    conversations: new Map([
      [
        'c1',
        {
          id: 'c1',
          type: 'dm',
          name: 'Alice',
          members: [
            { id: 'mem1', conversationId: 'c1', principalId: 'self', role: 'member', joinedAt: '2024-01-01T00:00:00Z', notificationsMuted: false },
            { id: 'mem2', conversationId: 'c1', principalId: 'p2',   role: 'member', joinedAt: '2024-01-01T00:00:00Z', notificationsMuted: false },
          ],
          createdAt: '2024-01-01T00:00:00Z',
          pinned: false,
          muted: false,
          unreadCount: 1,
        },
      ],
    ]),
    overlayMessages: new Map([
      [
        'c1',
        {
          messages: [
            {
              id: 'm1',
              conversationId: 'c1',
              senderId: 'p2',
              type: 'text',
              body: { html: '<p>Hey there</p>', text: 'Hey there' },
              inlineImages: [],
              reactions: {},
              createdAt: '2024-01-01T10:00:00Z',
              deleted: false,
            },
          ],
          status: 'ready',
          hasMore: false,
        },
      ],
    ]),
    presence: new Map([['p2', 'online']]),
    typing: new Map(),
    memberships: new Map(),
    messagesStatus: 'ready',
    messages: [],
    hasMoreMessages: false,
    closeOverlayMessages: vi.fn(),
    loadOverlayMessages: vi.fn().mockResolvedValue(undefined),
    loadMoreOverlayMessages: vi.fn(),
    markRead: vi.fn(),
    toggleReaction: vi.fn(),
  },
}));

vi.mock('./overlay-store.svelte', () => ({
  chatOverlay: {
    closeWindow: vi.fn(),
    minimizeWindow: vi.fn(),
    expandWindow: vi.fn(),
    toggleMinimize: vi.fn(),
  },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: { principalId: 'self' },
}));

vi.mock('../mail/EmojiPicker.svelte', () => ({
  default: { render: () => {} },
}));

// ChatCompose uses ProseMirror which requires a real editing host element.
// Stub it with a no-op function so Svelte can instantiate it without errors.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
vi.mock('./ChatCompose.svelte', () => ({
  default: function StubChatCompose() {
    return { c: () => {}, m: (target: Element) => { target.appendChild(document.createComment('ChatCompose stub')); }, d: () => {}, p: () => {} };
  },
}));

import ChatOverlayWindow from './ChatOverlayWindow.svelte';
import { chat } from './store.svelte';
import { chatOverlay } from './overlay-store.svelte';

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(chat.loadOverlayMessages).mockResolvedValue(undefined);
});

describe('ChatOverlayWindow', () => {
  it('renders the conversation name in the title bar', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const titleBar = container.querySelector('.title-bar');
    expect(titleBar?.textContent).toContain('Alice');
  });

  it('has an accessible aria-label on the section', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const section = container.querySelector('section');
    expect(section).toHaveAttribute('aria-label', 'Chat: Alice');
  });

  it('calls closeWindow and closeOverlayMessages when Close is clicked', async () => {
    render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const closeBtn = screen.getByRole('button', { name: /Close/i });
    await fireEvent.click(closeBtn);
    expect(chatOverlay.closeWindow).toHaveBeenCalledWith('ow-1');
    expect(chat.closeOverlayMessages).toHaveBeenCalledWith('c1');
  });

  it('calls toggleMinimize when the minimize button is clicked', async () => {
    render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const minBtn = screen.getByRole('button', { name: /Minimize/i });
    await fireEvent.click(minBtn);
    expect(chatOverlay.toggleMinimize).toHaveBeenCalledWith('ow-1');
  });

  it('calls toggleMinimize when the expand icon button is clicked (minimized state)', async () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: true },
    });
    // Use the icon-btn directly (aria-label "Expand"), not the title-bar div.
    const expandBtn = container.querySelector('.icon-btn[aria-label="Expand"]')!;
    expect(expandBtn).toBeInTheDocument();
    await fireEvent.click(expandBtn);
    expect(chatOverlay.toggleMinimize).toHaveBeenCalledWith('ow-1');
  });

  it('calls closeWindow on Escape key from within the window', async () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const section = container.querySelector('section')!;
    await fireEvent.keyDown(section, { key: 'Escape' });
    expect(chatOverlay.closeWindow).toHaveBeenCalledWith('ow-1');
  });

  it('shows the window-body when expanded', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    expect(container.querySelector('.window-body')).toBeInTheDocument();
  });

  it('hides the window-body when minimized', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: true },
    });
    expect(container.querySelector('.window-body')).not.toBeInTheDocument();
  });

  it('applies the minimized CSS class when minimized', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: true },
    });
    expect(container.querySelector('section')).toHaveClass('minimized');
  });
});
