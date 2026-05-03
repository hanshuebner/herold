/**
 * Tests for RecipientField saveToContacts notCreated error handling (re #68).
 *
 * Verifies:
 *   1. When the server returns notCreated for a Contact/set create, a toast
 *      with kind:'error' is shown (was previously a silent swallow).
 *   2. When the server successfully creates the contact, no error toast is
 *      shown.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/svelte';
import RecipientField from './RecipientField.svelte';

// ── Mocks ─────────────────────────────────────────────────────────────────

// Contacts store: does not include SEEN_EMAIL so the chip is "seen only".
vi.mock('../contacts/store.svelte', () => ({
  contacts: {
    status: 'ready',
    suggestions: [],
    filter: () => [],
    filterAsync: vi.fn(async () => []),
    load: vi.fn(),
  },
}));

// SeenAddresses: includes the test email so isSeenOnly() returns true.
vi.mock('../contacts/seen-addresses.svelte', () => ({
  seenAddresses: {
    status: 'ready',
    entries: [
      {
        id: 'sa-1',
        // Inline the literal — vi.mock factories are hoisted above const declarations.
        email: 'seen@example.com',
        displayName: 'Seen User',
        firstSeenAt: '2026-01-01T00:00:00Z',
        lastUsedAt: '2026-04-01T12:00:00Z',
        sendCount: 1,
        receivedCount: 0,
      },
    ],
    load: vi.fn(),
  },
}));

vi.mock('../auth/capabilities', () => ({
  hasDirectoryAutocomplete: vi.fn(() => false),
}));

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: {
        'urn:ietf:params:jmap:contacts': 'acc-1',
      },
    },
  },
}));

// JMAP client mock.
vi.mock('../jmap/client', () => {
  const jmap = {
    batch: vi.fn(async (builder: (b: unknown) => void) => {
      builder({
        call: (_name: string, _args: unknown, _using: string[]) => {
          return { ref: (p: string) => ({ resultOf: 'c0', name: _name, path: p }) };
        },
      });
      return { responses: [], sessionState: 'state-1' };
    }),
    hasCapability: vi.fn(() => true),
  };
  const strict = vi.fn((responses: unknown[]) => responses);
  return { jmap, strict };
});

// Toast mock.
vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

// ── Helpers ────────────────────────────────────────────────────────────────

async function getJmapMock() {
  return (await import('../jmap/client')) as unknown as {
    jmap: { batch: ReturnType<typeof vi.fn> };
    strict: ReturnType<typeof vi.fn>;
  };
}

async function getToastMock() {
  return (await import('../toast/toast.svelte')) as unknown as {
    toast: { show: ReturnType<typeof vi.fn> };
  };
}

function renderField() {
  return render(RecipientField, {
    label: 'To',
    // Chip email must match the seenAddresses mock entry above.
    chips: [{ email: 'seen@example.com', name: 'Seen User' }],
    onChipsChange: vi.fn(),
    onWarning: vi.fn(),
  });
}

beforeEach(async () => {
  const { jmap, strict } = await getJmapMock();
  vi.mocked(jmap.batch).mockClear();
  vi.mocked(strict).mockClear();
  const { toast } = await getToastMock();
  vi.mocked(toast.show).mockClear();
});

// ── Tests ──────────────────────────────────────────────────────────────────

describe('RecipientField saveToContacts — notCreated error handling (re #68)', () => {
  it('shows an error toast when the server returns notCreated for the contact', async () => {
    const { jmap, strict } = await getJmapMock();

    const fakeResponses = [
      [
        'Contact/set',
        {
          accountId: 'acc-1',
          oldState: '0',
          newState: '0',
          created: {},
          notCreated: {
            new1: {
              type: 'invalidProperties',
              description: 'addressBookId is required',
            },
          },
          updated: {},
          destroyed: [],
        },
        'c0',
      ],
    ];
    vi.mocked(jmap.batch).mockResolvedValue({
      responses: fakeResponses,
      sessionState: 'state-1',
    } as never);
    vi.mocked(strict).mockReturnValue(fakeResponses as never);

    renderField();

    const saveBtn = screen.getByRole('button', { name: /save.*contacts/i });
    await fireEvent.click(saveBtn);

    const { toast } = await getToastMock();
    await waitFor(() => {
      expect(toast.show).toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'error' }),
      );
    });

    // The toast message must contain the server-provided description.
    const call = vi.mocked(toast.show).mock.calls[0]![0] as { message: string };
    expect(call.message).toContain('addressBookId is required');
  });

  it('does not show an error toast when the server creates the contact successfully', async () => {
    const { jmap, strict } = await getJmapMock();

    const fakeResponses = [
      [
        'Contact/set',
        {
          accountId: 'acc-1',
          oldState: '0',
          newState: '1',
          created: { new1: { id: 'contact-42' } },
          notCreated: {},
          updated: {},
          destroyed: [],
        },
        'c0',
      ],
    ];
    vi.mocked(jmap.batch).mockResolvedValue({
      responses: fakeResponses,
      sessionState: 'state-1',
    } as never);
    vi.mocked(strict).mockReturnValue(fakeResponses as never);

    renderField();

    const saveBtn = screen.getByRole('button', { name: /save.*contacts/i });
    await fireEvent.click(saveBtn);

    const { toast } = await getToastMock();
    // Wait a tick for the async handler to complete.
    await new Promise((r) => setTimeout(r, 0));

    expect(toast.show).not.toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'error' }),
    );
  });
});
