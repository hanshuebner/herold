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
          kind: 'dm',
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
      [
        'c2',
        {
          id: 'c2',
          kind: 'space',
          name: '',
          members: [
            { id: 'mem3', conversationId: 'c2', principalId: 'self', role: 'member', joinedAt: '2024-01-01T00:00:00Z', notificationsMuted: false },
          ],
          createdAt: '2024-01-01T00:00:00Z',
          pinned: false,
          muted: false,
          unreadCount: 0,
        },
      ],
      [
        'c3',
        {
          id: 'c3',
          kind: 'space',
          name: 'Engineering',
          members: [
            { id: 'mem4', conversationId: 'c3', principalId: 'self', role: 'member', joinedAt: '2024-01-01T00:00:00Z', notificationsMuted: false },
          ],
          createdAt: '2024-01-01T00:00:00Z',
          pinned: false,
          muted: false,
          unreadCount: 0,
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
              senderPrincipalId: 'p2',
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
      ['c2', { messages: [], status: 'ready', hasMore: false }],
      ['c3', { messages: [], status: 'ready', hasMore: false }],
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

  // --- Sub-issue: title click to expand (issue #44) ---

  it('calls expandWindow when the title bar is clicked in minimized state', async () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: true },
    });
    const titleBar = container.querySelector('.title-bar')!;
    expect(titleBar).toBeInTheDocument();
    await fireEvent.click(titleBar);
    expect(chatOverlay.expandWindow).toHaveBeenCalledWith('ow-1');
  });

  it('does not call expandWindow when the title bar is clicked in expanded state', async () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const titleBar = container.querySelector('.title-bar')!;
    await fireEvent.click(titleBar);
    expect(chatOverlay.expandWindow).not.toHaveBeenCalled();
  });

  // --- Sub-issue: fallback name for space with empty name (issue #44) ---

  it('shows "Untitled space" in the title bar when a space has an empty name', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-2', conversationId: 'c2', minimized: false },
    });
    const titleName = container.querySelector('.title-name');
    expect(titleName?.textContent).toBe('Untitled space');
  });

  it('shows "Untitled space" in the section aria-label when a space has an empty name', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-2', conversationId: 'c2', minimized: false },
    });
    const section = container.querySelector('section');
    expect(section).toHaveAttribute('aria-label', 'Chat: Untitled space');
  });

  it('shows the space name when the space has a non-empty name', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-3', conversationId: 'c3', minimized: false },
    });
    const titleName = container.querySelector('.title-name');
    expect(titleName?.textContent).toBe('Engineering');
  });

  // --- Sub-issue: title must never expose the raw conversation id (issue #47) ---

  it('does not display the raw conversation id when the conversation is not yet cached', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-x', conversationId: 'cid-not-in-cache', minimized: false },
    });
    const titleName = container.querySelector('.title-name');
    expect(titleName?.textContent).not.toContain('cid-not-in-cache');
    expect(titleName?.textContent?.trim()).toBe('Loading...');
    const section = container.querySelector('section');
    expect(section?.getAttribute('aria-label')).not.toContain('cid-not-in-cache');
  });

  // --- Sub-issue: icon characters not rendered as HTML entities (issue #44) ---

  it('renders the minimize button as a plain dash character, not an HTML entity string', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const minBtn = container.querySelector('.icon-btn[aria-label="Minimize"]')!;
    // The inner text must be the Unicode en dash (U+2013), not the literal
    // string "&#x2013;" which would indicate the entity was not parsed.
    expect(minBtn.textContent?.trim()).toBe('–');
    expect(minBtn.textContent).not.toContain('&');
  });

  it('renders the close button as a plain times character, not an HTML entity string', () => {
    const { container } = render(ChatOverlayWindow, {
      props: { windowKey: 'ow-1', conversationId: 'c1', minimized: false },
    });
    const closeBtn = screen.getByRole('button', { name: /Close/i });
    // Must be U+00D7 (multiplication sign), not the literal "&#x00D7;" string.
    expect(closeBtn.textContent?.trim()).toBe('×');
    expect(closeBtn.textContent).not.toContain('&');
  });
});
