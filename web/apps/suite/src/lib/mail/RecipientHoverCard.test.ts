/**
 * Tests for RecipientHoverCard — focussed on the chat action (re #61).
 *
 * Coverage:
 *   - Chat button opens existing DM in the overlay window.
 *   - Chat button creates a new DM directly (no picker dialog) when no
 *     DM with the person exists yet, then opens it in the overlay.
 *   - createConversation failure shows a toast.
 *
 * We use the REAL recipientHover singleton (backed by $state) so that
 * the component's $derived(recipientHover.open) reacts to changes.
 * resolvePerson must return a person (not null) so that the background
 * refresh in the $effect does not overwrite person back to null.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';

// ── mocks ─────────────────────────────────────────────────────────────────────

vi.mock('./person-resolver.svelte', () => ({
  peekPerson: vi.fn(),
  resolvePerson: vi.fn(),
}));

vi.mock('./avatar-resolver.svelte', () => ({
  resolve: vi.fn().mockResolvedValue(null),
  avatarEmailMetadataEnabled: () => false,
  setAvatarEmailMetadataEnabled: vi.fn(),
  clearAvatarCache: vi.fn(),
}));

vi.mock('./store.svelte', () => ({
  mail: { identities: new Map() },
}));

vi.mock('../contacts/store.svelte', () => ({
  contacts: { load: vi.fn() },
}));

vi.mock('../compose/compose.svelte', () => ({
  compose: { openWith: vi.fn() },
}));

vi.mock('../jmap/client', () => ({
  jmap: {
    hasCapability: vi.fn().mockReturnValue(true),
    batch: vi.fn(),
  },
  strict: vi.fn(),
}));

vi.mock('../jmap/types', () => ({
  Capability: {
    HeroldChat: 'urn:ietf:params:jmap:herold:chat',
    Contacts: 'urn:ietf:params:jmap:contacts',
    Calendars: 'urn:ietf:params:jmap:calendars',
    Core: 'urn:ietf:params:jmap:core',
  },
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    principalId: 'prin-me',
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:contacts': 'acc-1' },
    },
  },
}));

vi.mock('../chat/new-chat-picker.svelte', () => ({
  newChatPicker: { open: vi.fn() },
}));

vi.mock('../chat/overlay-store.svelte', () => ({
  chatOverlay: { openWindow: vi.fn() },
}));

vi.mock('../chat/store.svelte', () => ({
  chat: {
    findExistingDM: vi.fn(),
    createConversation: vi.fn(),
  },
}));

vi.mock('../router/router.svelte', () => ({
  router: { navigate: vi.fn(), parts: [] },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

// ── imports ───────────────────────────────────────────────────────────────────

import RecipientHoverCard from './RecipientHoverCard.svelte';
// Use the REAL singleton so $derived(recipientHover.open) tracks it.
import { recipientHover } from './recipient-hover.svelte';
import { peekPerson, resolvePerson } from './person-resolver.svelte';
import { chat } from '../chat/store.svelte';
import { chatOverlay } from '../chat/overlay-store.svelte';
import { newChatPicker } from '../chat/new-chat-picker.svelte';
import { toast } from '../toast/toast.svelte';
import type { Conversation } from '../chat/types';

// ── helpers ───────────────────────────────────────────────────────────────────

function makePerson(principalId = 'prin-alice') {
  return {
    email: 'alice@example.com',
    displayName: 'Alice',
    avatarUrl: null as string | null,
    phones: [] as { type: string; number: string }[],
    contactId: null as string | null,
    principalId,
  };
}

/**
 * Open the hover card for the given principal and wait for the chat
 * button to appear.
 *
 * peekPerson and resolvePerson are both mocked to return the person so
 * that: (1) the card renders synchronously from cache, and (2) the
 * async background refresh in the $effect does not set person back to
 * null (which would hide the card again).
 */
async function openCardAndFindChatButton(principalId = 'prin-alice') {
  const person = makePerson(principalId);
  vi.mocked(peekPerson).mockReturnValue(person);
  // resolvePerson must return the person (not null) so the async
  // background refresh does not overwrite person to null.
  vi.mocked(resolvePerson).mockResolvedValue(person);
  recipientHover.requestOpen(
    {
      anchor: document.createElement('button'),
      email: 'alice@example.com',
      capturedName: 'Alice',
    },
    { immediate: true },
  );
  return screen.findByRole('button', { name: 'contact.card.chat' });
}

// ── tests ─────────────────────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
  recipientHover.closeNow();
});

afterEach(() => {
  recipientHover.closeNow();
});

describe('RecipientHoverCard handleChat (re #61)', () => {
  it('opens existing DM in overlay window when one already exists', async () => {
    const existingConv: Conversation = {
      id: 'conv-existing',
      kind: 'dm',
      name: 'Alice',
      members: [
        { id: 'm1', conversationId: 'conv-existing', principalId: 'prin-me', role: 'member', joinedAt: '' },
        { id: 'm2', conversationId: 'conv-existing', principalId: 'prin-alice', role: 'member', joinedAt: '' },
      ],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 0,
    };
    vi.mocked(chat.findExistingDM).mockReturnValue(existingConv);

    render(RecipientHoverCard);
    const chatBtn = await openCardAndFindChatButton('prin-alice');
    await fireEvent.click(chatBtn);

    await waitFor(() => {
      expect(chatOverlay.openWindow).toHaveBeenCalledWith('conv-existing');
    });
    expect(chat.createConversation).not.toHaveBeenCalled();
    expect(newChatPicker.open).not.toHaveBeenCalled();
  });

  it('creates a new DM directly (no picker dialog) when no existing DM', async () => {
    vi.mocked(chat.findExistingDM).mockReturnValue(null);
    vi.mocked(chat.createConversation).mockResolvedValue({ id: 'conv-new' });

    render(RecipientHoverCard);
    const chatBtn = await openCardAndFindChatButton('prin-alice');
    await fireEvent.click(chatBtn);

    await waitFor(() => {
      expect(chat.createConversation).toHaveBeenCalledWith({
        kind: 'dm',
        members: ['prin-me', 'prin-alice'],
      });
    });
    await waitFor(() => {
      expect(chatOverlay.openWindow).toHaveBeenCalledWith('conv-new');
    });
    // The generic new-chat picker must NOT have been opened (re #61).
    expect(newChatPicker.open).not.toHaveBeenCalled();
  });

  it('shows a toast when createConversation fails', async () => {
    vi.mocked(chat.findExistingDM).mockReturnValue(null);
    vi.mocked(chat.createConversation).mockRejectedValue(new Error('server error'));

    render(RecipientHoverCard);
    const chatBtn = await openCardAndFindChatButton('prin-alice');
    await fireEvent.click(chatBtn);

    await waitFor(() => {
      expect(toast.show).toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'error' }),
      );
    });
    expect(chatOverlay.openWindow).not.toHaveBeenCalled();
    expect(newChatPicker.open).not.toHaveBeenCalled();
  });
});
