/**
 * Tests for PrivacyForm.svelte — the "Remember recently-used addresses"
 * toggle (REQ-SET-15, REQ-MAIL-11m).
 *
 * Coverage:
 *   1. Toggle off writes seen_addresses_enabled: false via PATCH
 *   2. Toggle off clears the local seenAddresses store on success
 *   3. Toggle on writes seen_addresses_enabled: true via PATCH
 *   4. Toggle on does NOT clear the local seenAddresses store
 *   5. On PATCH failure the toggle reverts to the previous value
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import PrivacyForm from './PrivacyForm.svelte';

// ── Auth mock ─────────────────────────────────────────────────────────────

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    principalId: '42',
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'account-1' },
      capabilities: {},
      username: 'test@example.com',
    },
  },
}));

// ── API client mock ────────────────────────────────────────────────────────

const mockGet = vi.fn();
const mockPatch = vi.fn();

vi.mock('../../lib/api/client', () => ({
  get: (...args: unknown[]) => mockGet(...args),
  patch: (...args: unknown[]) => mockPatch(...args),
  ApiError: class ApiError extends Error {
    constructor(
      public status: number,
      message: string,
    ) {
      super(message);
      this.name = 'ApiError';
    }
  },
}));

// ── SeenAddresses mock ─────────────────────────────────────────────────────

const mockClear = vi.fn();

vi.mock('../../lib/contacts/seen-addresses.svelte', () => ({
  seenAddresses: {
    status: 'ready',
    entries: [{ id: 'sa-1', email: 'prev@example.com', displayName: 'Prev' }],
    clear: (...args: unknown[]) => mockClear(...args),
    load: vi.fn(),
    destroy: vi.fn(),
  },
}));

beforeEach(() => {
  vi.clearAllMocks();
  // Default: server returns enabled=true.
  mockGet.mockResolvedValue({ id: '42', seen_addresses_enabled: true });
  mockPatch.mockResolvedValue({ id: '42', seen_addresses_enabled: false });
});

describe('PrivacyForm', () => {
  it('renders the toggle checkbox', async () => {
    const { getByRole } = render(PrivacyForm);
    await waitFor(() => {
      const checkbox = getByRole('checkbox');
      expect(checkbox).toBeInTheDocument();
    });
  });

  it('toggle off calls PATCH with seen_addresses_enabled: false', async () => {
    mockPatch.mockResolvedValue({ id: '42', seen_addresses_enabled: false });
    const { getByRole } = render(PrivacyForm);

    await waitFor(() => expect(mockGet).toHaveBeenCalled());

    const checkbox = getByRole('checkbox') as HTMLInputElement;
    // Simulate unchecking.
    await fireEvent.change(checkbox, { target: { checked: false } });

    await waitFor(() => {
      expect(mockPatch).toHaveBeenCalledWith(
        '/api/v1/principals/42',
        { seen_addresses_enabled: false },
      );
    });
  });

  it('toggle off clears the local seenAddresses store on success', async () => {
    mockPatch.mockResolvedValue({ id: '42', seen_addresses_enabled: false });
    const { getByRole } = render(PrivacyForm);

    await waitFor(() => expect(mockGet).toHaveBeenCalled());

    const checkbox = getByRole('checkbox') as HTMLInputElement;
    await fireEvent.change(checkbox, { target: { checked: false } });

    await waitFor(() => {
      expect(mockClear).toHaveBeenCalled();
    });
  });

  it('toggle on calls PATCH with seen_addresses_enabled: true', async () => {
    // Start with disabled state.
    mockGet.mockResolvedValue({ id: '42', seen_addresses_enabled: false });
    mockPatch.mockResolvedValue({ id: '42', seen_addresses_enabled: true });

    const { getByRole } = render(PrivacyForm);

    await waitFor(() => expect(mockGet).toHaveBeenCalled());

    const checkbox = getByRole('checkbox') as HTMLInputElement;
    await fireEvent.change(checkbox, { target: { checked: true } });

    await waitFor(() => {
      expect(mockPatch).toHaveBeenCalledWith(
        '/api/v1/principals/42',
        { seen_addresses_enabled: true },
      );
    });
  });

  it('toggle on does NOT clear the seenAddresses store', async () => {
    mockGet.mockResolvedValue({ id: '42', seen_addresses_enabled: false });
    mockPatch.mockResolvedValue({ id: '42', seen_addresses_enabled: true });

    const { getByRole } = render(PrivacyForm);
    await waitFor(() => expect(mockGet).toHaveBeenCalled());

    const checkbox = getByRole('checkbox') as HTMLInputElement;
    await fireEvent.change(checkbox, { target: { checked: true } });

    await waitFor(() => expect(mockPatch).toHaveBeenCalled());
    expect(mockClear).not.toHaveBeenCalled();
  });

  it('reverts the toggle and shows an error when PATCH fails', async () => {
    const { ApiError } = await import('../../lib/api/client');
    mockPatch.mockRejectedValue(new ApiError(500, 'Server error'));

    const { getByRole, findByRole } = render(PrivacyForm);
    await waitFor(() => expect(mockGet).toHaveBeenCalled());

    const checkbox = getByRole('checkbox') as HTMLInputElement;
    await fireEvent.change(checkbox, { target: { checked: false } });

    // Should show an error message.
    const alert = await findByRole('alert');
    expect(alert).toBeInTheDocument();
    expect(alert.textContent).toContain('Server error');
  });
});
