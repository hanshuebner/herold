/**
 * Component tests for NewChatPicker.
 *
 * Covers:
 *   - DM mode: happy path with autocomplete selection
 *   - DM mode: hard error on non-Herold free-text email
 *   - DM mode: dedup routing to existing DM
 *   - Space mode: create button disabled until name + at least one member
 *   - Space mode: happy path
 *
 * REQ-CHAT-01a..d, REQ-CHAT-02a, REQ-CHAT-15.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';

// ── mocks ───────────────────────────────────────────────────────────────────

vi.mock('./store.svelte', () => ({
  chat: {
    searchPrincipals: vi.fn(),
    lookupPrincipalByEmail: vi.fn(),
    findExistingDM: vi.fn(),
    createConversation: vi.fn(),
  },
}));

vi.mock('./overlay-store.svelte', () => ({
  chatOverlay: {
    openWindow: vi.fn(),
  },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

import NewChatPicker from './NewChatPicker.svelte';
import { newChatPicker } from './new-chat-picker.svelte';
import { chat } from './store.svelte';
import { chatOverlay } from './overlay-store.svelte';
import { toast } from '../toast/toast.svelte';

// ── helpers ──────────────────────────────────────────────────────────────────

function openDM(): void {
  newChatPicker.open({ mode: 'dm' });
}

function openSpace(): void {
  newChatPicker.open({ mode: 'space' });
}

// ── tests ────────────────────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
  newChatPicker.close();
});

describe('NewChatPicker', () => {
  it('does not render when picker is closed', () => {
    render(NewChatPicker);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('renders the DM modal when opened in DM mode', async () => {
    render(NewChatPicker);
    openDM();
    await waitFor(() =>
      expect(screen.getByRole('dialog')).toBeInTheDocument(),
    );
    expect(screen.getByText('Chat')).toBeInTheDocument();
  });

  it('renders the Space modal when opened in Space mode', async () => {
    render(NewChatPicker);
    openSpace();
    await waitFor(() =>
      expect(screen.getByRole('dialog')).toBeInTheDocument(),
    );
    // Title is always "Chat"; mode tab "Create Space" is selected.
    expect(screen.getByText('Chat')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Create Space', selected: true })).toBeInTheDocument();
  });

  it('switches mode via tab buttons', async () => {
    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    await fireEvent.click(screen.getByRole('tab', { name: 'Create Space' }));
    // Title stays "Chat" regardless of mode.
    expect(screen.getByText('Chat')).toBeInTheDocument();
  });

  it('closes when the Cancel button is clicked', async () => {
    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    );
  });

  // ── DM: autocomplete happy path ──────────────────────────────────────

  it('DM mode: autocomplete suggests principals and selecting starts the chat', async () => {
    vi.mocked(chat.searchPrincipals).mockResolvedValue([
      { id: 'prin-alice', email: 'alice@example.com', displayName: 'Alice' },
    ]);
    vi.mocked(chat.findExistingDM).mockReturnValue(null);
    vi.mocked(chat.createConversation).mockResolvedValue({ id: 'conv-new-1' });

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'ali' } });

    // Advance the 150ms debounce.
    await new Promise((r) => setTimeout(r, 200));
    await waitFor(() =>
      expect(screen.getByText('Alice')).toBeInTheDocument(),
    );
    expect(screen.getByText('alice@example.com')).toBeInTheDocument();

    await fireEvent.click(screen.getByRole('button', { name: /Alice/ }));

    // Picking the recipient is the entire DM-creation action; no second
    // click on Start chat is required.
    await waitFor(() =>
      expect(chat.createConversation).toHaveBeenCalledWith({
        kind: 'dm',
        members: ['prin-alice'],
      }),
    );
    await waitFor(() =>
      expect(chatOverlay.openWindow).toHaveBeenCalledWith('conv-new-1'),
    );
  });

  it('DM mode: Start chat button is disabled with no recipient', async () => {
    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const startBtn = screen.getByRole('button', { name: 'Start chat' });
    expect(startBtn).toBeDisabled();
  });

  // ── DM: free-text email hard error ───────────────────────────────────

  it('DM mode: non-Herold email shows inline error, does not proceed', async () => {
    vi.mocked(chat.lookupPrincipalByEmail).mockResolvedValue(null);

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'nobody@outside.com' } });
    await fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() =>
      expect(
        screen.getByRole('alert'),
      ).toHaveTextContent('nobody@outside.com is not a Herold user on this server'),
    );

    // No chip added, Start chat still disabled.
    expect(screen.queryByLabelText(/Recipient:/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Start chat' })).toBeDisabled();
  });

  it('DM mode: pressing Enter on a valid email starts the chat without a second click', async () => {
    vi.mocked(chat.lookupPrincipalByEmail).mockResolvedValue({
      id: 'prin-bob',
      email: 'bob@example.com',
      displayName: 'Bob',
    });
    vi.mocked(chat.findExistingDM).mockReturnValue(null);
    vi.mocked(chat.createConversation).mockResolvedValue({ id: 'conv-new-2' });

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'bob@example.com' } });
    await fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() =>
      expect(chat.createConversation).toHaveBeenCalledWith({
        kind: 'dm',
        members: ['prin-bob'],
      }),
    );
    await waitFor(() =>
      expect(chatOverlay.openWindow).toHaveBeenCalledWith('conv-new-2'),
    );
  });

  // ── DM: dedup routing ────────────────────────────────────────────────

  it('DM mode: routes to existing DM rather than creating a new one', async () => {
    const existingConv = {
      id: 'conv-existing',
      type: 'dm' as const,
      name: 'Alice',
      members: [],
      createdAt: '',
      pinned: false,
      muted: false,
      unreadCount: 0,
    };
    vi.mocked(chat.findExistingDM).mockReturnValue(existingConv);
    vi.mocked(chat.lookupPrincipalByEmail).mockResolvedValue({
      id: 'prin-alice',
      email: 'alice@example.com',
      displayName: 'Alice',
    });

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'alice@example.com' } });
    await fireEvent.keyDown(input, { key: 'Enter' });

    // Picking the recipient now auto-submits; the existing DM is opened
    // directly without going through Conversation/set.
    await waitFor(() =>
      expect(chatOverlay.openWindow).toHaveBeenCalledWith('conv-existing'),
    );
    expect(chat.createConversation).not.toHaveBeenCalled();
  });

  // ── Space mode: disabled until name + member ─────────────────────────

  it('Space mode: Create Space button disabled until name and member are provided', async () => {
    render(NewChatPicker);
    openSpace();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const createBtn = screen.getByRole('button', { name: 'Create Space' });
    expect(createBtn).toBeDisabled();

    // Add a name but no member — still disabled.
    await fireEvent.input(
      screen.getByPlaceholderText('e.g. Project Hermes'),
      { target: { value: 'Hermes' } },
    );
    expect(createBtn).toBeDisabled();
  });

  // ── Space mode: happy path ───────────────────────────────────────────

  it('Space mode: submits and opens the new space', async () => {
    vi.mocked(chat.searchPrincipals).mockResolvedValue([
      { id: 'prin-alice', email: 'alice@example.com', displayName: 'Alice' },
    ]);
    vi.mocked(chat.createConversation).mockResolvedValue({ id: 'conv-space-1' });

    render(NewChatPicker);
    openSpace();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    // Fill in space name.
    await fireEvent.input(
      screen.getByPlaceholderText('e.g. Project Hermes'),
      { target: { value: 'Hermes' } },
    );

    // Type in recipient input to trigger suggestions.
    const recipientInput = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(recipientInput, { target: { value: 'ali' } });

    await new Promise((r) => setTimeout(r, 200));
    await waitFor(() => expect(screen.getByText('Alice')).toBeInTheDocument());
    await fireEvent.click(screen.getByRole('button', { name: /Alice/ }));

    await waitFor(() =>
      expect(screen.getByLabelText(/Recipient: Alice/)).toBeInTheDocument(),
    );

    const createBtn = screen.getByRole('button', { name: 'Create Space' });
    expect(createBtn).not.toBeDisabled();

    await fireEvent.click(createBtn);
    await waitFor(() =>
      expect(chat.createConversation).toHaveBeenCalledWith({
        kind: 'space',
        members: ['prin-alice'],
        name: 'Hermes',
        topic: undefined,
      }),
    );
    await waitFor(() =>
      expect(chatOverlay.openWindow).toHaveBeenCalledWith('conv-space-1'),
    );
  });

  // ── JMAP error handling ──────────────────────────────────────────────

  it('DM mode: shows toast on createConversation failure, keeps picker open', async () => {
    vi.mocked(chat.findExistingDM).mockReturnValue(null);
    vi.mocked(chat.createConversation).mockRejectedValue(new Error('server error'));
    vi.mocked(chat.lookupPrincipalByEmail).mockResolvedValue({
      id: 'prin-bob',
      email: 'bob@example.com',
      displayName: 'Bob',
    });

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'bob@example.com' } });
    await fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() =>
      expect(screen.getByLabelText(/Recipient: Bob/)).toBeInTheDocument(),
    );

    await fireEvent.click(screen.getByRole('button', { name: 'Start chat' }));
    await waitFor(() =>
      expect(toast.show).toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'error' }),
      ),
    );
    // Picker stays open.
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  // ── floating suggestions: modal height must not change ───────────────

  it('suggestions dropdown does not change modal outer height', async () => {
    vi.mocked(chat.searchPrincipals).mockResolvedValue([
      { id: 'p1', email: 'alpha@example.com', displayName: 'Alpha' },
      { id: 'p2', email: 'beta@example.com', displayName: 'Beta' },
      { id: 'p3', email: 'gamma@example.com', displayName: 'Gamma' },
      { id: 'p4', email: 'delta@example.com', displayName: 'Delta' },
      { id: 'p5', email: 'epsilon@example.com', displayName: 'Epsilon' },
    ]);

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const dialog = screen.getByRole('dialog');
    const heightBefore = dialog.offsetHeight;

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'al' } });

    // Advance the 150ms debounce.
    await new Promise((r) => setTimeout(r, 200));
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument());

    // Suggestions are present; the dialog's outer height must not have grown.
    const heightAfter = dialog.offsetHeight;
    expect(heightAfter).toBe(heightBefore);
  });

  // ── chip label rendering (issue #45) ────────────────────────────────

  it('chip label: shows name only when displayName differs from email, email on title', async () => {
    vi.mocked(chat.lookupPrincipalByEmail).mockResolvedValue({
      id: 'prin-alice',
      email: 'alice@example.com',
      displayName: 'Alice Smith',
    });

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'alice@example.com' } });
    await fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() =>
      expect(screen.getByLabelText(/Recipient: Alice Smith/)).toBeInTheDocument(),
    );

    const chipLabel = screen.getByRole('dialog').querySelector('.chip-label') as HTMLElement;
    expect(chipLabel).not.toBeNull();
    // Visible text is the name only — email must not appear in text content.
    expect(chipLabel.textContent?.trim()).toBe('Alice Smith');
    expect(chipLabel.textContent).not.toContain('alice@example.com');
    // Email is on the title attribute for hover.
    expect(chipLabel.getAttribute('title')).toBe('alice@example.com');
  });

  it('chip label: shows email only when displayName equals email (no real name set), no title', async () => {
    vi.mocked(chat.lookupPrincipalByEmail).mockResolvedValue({
      id: 'prin-bob',
      email: 'bob@example.com',
      // Server returns email as displayName when no real name is set.
      displayName: 'bob@example.com',
    });

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'bob@example.com' } });
    await fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() =>
      expect(screen.getByLabelText(/Recipient: bob@example.com/)).toBeInTheDocument(),
    );

    const chipLabel = screen.getByRole('dialog').querySelector('.chip-label') as HTMLElement;
    expect(chipLabel).not.toBeNull();
    // Visible text is the email — appears exactly once in the chip label.
    expect(chipLabel.textContent?.trim()).toBe('bob@example.com');
    // No title attribute when name equals email.
    expect(chipLabel.getAttribute('title')).toBeNull();
  });

  // ── REQ-CHAT-15: no principal id in DOM ─────────────────────────────

  it('REQ-CHAT-15: principal id is never rendered in the chip or suggestions', async () => {
    vi.mocked(chat.searchPrincipals).mockResolvedValue([
      { id: 'opaque-id-12345', email: 'alice@example.com', displayName: 'Alice' },
    ]);

    render(NewChatPicker);
    openDM();
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument());

    const input = screen.getByRole('textbox', { name: /Search for a person/i });
    await fireEvent.input(input, { target: { value: 'ali' } });

    await new Promise((r) => setTimeout(r, 200));
    await waitFor(() => expect(screen.getByText('Alice')).toBeInTheDocument());

    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent).not.toContain('opaque-id-12345');

    await fireEvent.click(screen.getByRole('button', { name: /Alice/ }));
    await waitFor(() =>
      expect(screen.getByLabelText(/Recipient: Alice/)).toBeInTheDocument(),
    );
    expect(screen.getByRole('dialog').textContent).not.toContain('opaque-id-12345');
  });
});
