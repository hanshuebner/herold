/**
 * Regression tests for issue #31: brand mark hoisted into GlobalBar.
 *
 * Commit e1f0b32 moved AppSwitcherMenu and the "Herold" wordmark out of
 * Shell.svelte's .brand-row and into a new 240px .brand-area inside
 * GlobalBar.svelte's left section, then hoisted <GlobalBar /> from inside
 * .content to above .middle in Shell.svelte.
 *
 * Assertions:
 *   1. The "Herold" wordmark link is rendered inside GlobalBar.
 *   2. The .brand-area element is present and contains the wordmark.
 *   3. Shell no longer contains a .brand-row element (the pre-#31 container
 *      that was deleted by the fix).
 *   4. Shell renders GlobalBar above .middle in the DOM.
 *
 * re #31.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// ── mocks (hoisted before component imports) ─────────────────────────────────
//
// GlobalBar imports three singletons directly, and renders AppSwitcherMenu
// which itself depends on auth + i18n, plus AdvancedSearchPanel which
// depends on the mail store. Mock all four singleton layers here so the
// component renders in happy-dom without a live backend.

vi.mock('../../lib/jmap/sync.svelte', () => ({
  sync: {
    connectionState: 'connected',
    on: vi.fn(() => () => {}),
    open: vi.fn(),
    close: vi.fn(),
  },
}));

vi.mock('../../lib/router/router.svelte', () => ({
  router: {
    matches: () => false,
    parts: [],
    navigate: vi.fn(),
  },
}));

vi.mock('../help/help.svelte', () => ({
  help: { toggle: vi.fn() },
}));

// AppSwitcherMenu dependencies.
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: { capabilities: {} },
    scopes: [],
  },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
}));

// AdvancedSearchPanel dependency: the mail store.
// The panel is only shown when panelOpen=true (default false), so it is
// imported but never rendered in these tests.
vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailboxes: new Map(),
  },
}));

// Shell.svelte imports several overlay/dialog components that each pull in
// additional singletons. Stub the stores those components depend on so the
// Shell render does not throw.

vi.mock('../toast/toast.svelte', () => ({
  toast: { messages: [] },
}));

vi.mock('../compose/compose.svelte', () => ({
  compose: {
    windows: [],
    minimized: [],
    open: vi.fn(),
    minimize: vi.fn(),
    restore: vi.fn(),
    close: vi.fn(),
    openNew: vi.fn(),
    openReply: vi.fn(),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
  },
}));

vi.mock('../mail/move-picker.svelte', () => ({
  movePicker: { isOpen: false, open: vi.fn(), close: vi.fn() },
  computeMoveCandidates: () => [],
  filterMailboxesByName: () => [],
}));

vi.mock('../mail/label-picker.svelte', () => ({
  labelPicker: { isOpen: false, open: vi.fn(), close: vi.fn() },
}));

vi.mock('../mail/snooze-picker.svelte', () => ({
  snoozePicker: { isOpen: false, open: vi.fn(), close: vi.fn() },
  snoozeQuickOptions: () => [],
}));

vi.mock('../dialog/dialog.svelte', () => ({
  confirmDialog: { isOpen: false, open: vi.fn(), close: vi.fn() },
  promptDialog: { isOpen: false, open: vi.fn(), close: vi.fn() },
}));

vi.mock('../chat/overlay-store.svelte', () => ({
  chatOverlay: { windows: [], isOpen: vi.fn(() => false), openWindow: vi.fn() },
}));

vi.mock('../chat/new-chat-picker.svelte', () => ({
  newChatPicker: { isOpen: false, open: vi.fn() },
}));

vi.mock('../mail/reaction-confirm.svelte', () => ({
  reactionConfirm: { isOpen: false, open: vi.fn(), close: vi.fn() },
}));

vi.mock('../mail/recipient-hover-card.svelte', () => ({
  recipientHoverCard: {
    isOpen: false,
    contact: null,
    anchorEl: null,
    open: vi.fn(),
    close: vi.fn(),
  },
}));

vi.mock('../help/help.svelte', () => ({
  help: { isOpen: false, toggle: vi.fn() },
}));

vi.mock('../coach/coach.svelte', () => ({
  coach: { strips: [], dismiss: vi.fn() },
}));

vi.mock('../jmap/client', () => ({
  jmap: {
    downloadUrl: () => null,
    batch: vi.fn(),
  },
}));

vi.mock('../settings/settings.svelte', () => ({
  settings: {
    isImageAllowed: () => false,
    addImageAllowedSender: vi.fn(),
  },
}));

vi.mock('../mail/avatar-resolver.svelte', () => ({
  resolve: vi.fn().mockResolvedValue(null),
  avatarEmailMetadataEnabled: () => false,
}));

import GlobalBar from './GlobalBar.svelte';
import Shell from './Shell.svelte';

// ── tests ─────────────────────────────────────────────────────────────────────

beforeEach(() => {
  // Nothing to reset for this suite.
});

describe('GlobalBar — brand mark (re #31)', () => {
  it('renders the "Herold" wordmark link', () => {
    render(GlobalBar);
    // The brand link carries aria-label="Herold home" and the text "Herold".
    const brandLink = screen.getByRole('link', { name: /herold home/i });
    expect(brandLink).toBeInTheDocument();
    expect(brandLink.textContent?.trim()).toBe('Herold');
  });

  it('brand link points to the root path', () => {
    render(GlobalBar);
    const brandLink = screen.getByRole('link', { name: /herold home/i });
    expect(brandLink).toHaveAttribute('href', '/');
  });

  it('.brand-area element is present inside the GlobalBar header', () => {
    const { container } = render(GlobalBar);
    const brandArea = container.querySelector('.brand-area');
    expect(brandArea).not.toBeNull();
    expect(brandArea).toBeInTheDocument();
  });

  it('.brand-area contains the wordmark link', () => {
    const { container } = render(GlobalBar);
    const brandArea = container.querySelector('.brand-area');
    expect(brandArea).not.toBeNull();
    const brandLink = brandArea!.querySelector('a.brand');
    expect(brandLink).not.toBeNull();
    expect(brandLink!.textContent?.trim()).toBe('Herold');
  });
});

describe('Shell — no .brand-row after #31 fix', () => {
  it('Shell does not contain a .brand-row element', () => {
    const { container } = render(Shell);
    expect(container.querySelector('.brand-row')).toBeNull();
  });

  it('Shell renders GlobalBar above .middle (correct DOM order)', () => {
    const { container } = render(Shell);
    const shell = container.querySelector('.shell') as HTMLElement;
    expect(shell).not.toBeNull();

    const children = Array.from(shell.children);
    // After the #31 overlay fix the wrapper div was removed; the <header
    // class="global-bar"> is now a direct flex child of .shell.
    const globalBar = shell.querySelector('.global-bar');
    const middle = shell.querySelector('.middle');

    expect(globalBar).not.toBeNull();
    expect(middle).not.toBeNull();

    const barIdx = children.findIndex((el) =>
      el.classList.contains('global-bar'),
    );
    const middleIdx = children.findIndex((el) => el.classList.contains('middle'));

    expect(barIdx).toBeGreaterThanOrEqual(0);
    expect(middleIdx).toBeGreaterThan(barIdx);
  });
});
