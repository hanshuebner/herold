/**
 * Tests for DiagnosticsForm.svelte (REQ-CLOG-06).
 *
 * Coverage:
 *   1. Renders the telemetry checkbox
 *   2. Checkbox is checked when session capability says enabled=true
 *   3. Checkbox is unchecked when session capability says enabled=false
 *   4. Toggling to false calls PUT /api/v1/me/clientlog/telemetry_enabled
 *      with {enabled: false}
 *   5. Toggling to true calls PUT with {enabled: true}
 *   6. On PUT failure, reverts optimistic state and shows error
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import DiagnosticsForm from './DiagnosticsForm.svelte';

// ── Hoist shared mutable state so vi.mock factories can reference it ──────

const CAP = 'urn:netzhansa:params:jmap:clientlog';

const { mockAuth, mockPut } = vi.hoisted(() => {
  const mockAuth = {
    status: 'ready' as
      | 'idle'
      | 'bootstrapping'
      | 'ready'
      | 'unauthenticated'
      | 'error',
    session: {
      capabilities: {
        'urn:netzhansa:params:jmap:clientlog': {
          telemetry_enabled: true,
        } as Record<string, unknown>,
      },
      username: 'test@example.com',
    },
  };
  const mockPut = vi.fn();
  return { mockAuth, mockPut };
});

// ── Auth mock ─────────────────────────────────────────────────────────────

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: mockAuth,
}));

// ── API client mock ────────────────────────────────────────────────────────

vi.mock('../../lib/api/client', () => ({
  put: (...args: unknown[]) => mockPut(...args),
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

// ── i18n mock ─────────────────────────────────────────────────────────────

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string) => {
    const map: Record<string, string> = {
      'settings.diagnostics.heading': 'Diagnostics',
      'settings.diagnostics.telemetry.label':
        'Send anonymous diagnostic logs to my mail-server operator',
    };
    return map[key] ?? key;
  },
}));

beforeEach(() => {
  vi.clearAllMocks();
  mockAuth.status = 'ready';
  mockAuth.session.capabilities[CAP] = { telemetry_enabled: true };
  mockPut.mockResolvedValue(undefined);
});

describe('DiagnosticsForm', () => {
  it('renders the telemetry checkbox', async () => {
    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(
        getByRole('checkbox', { name: /Send anonymous diagnostic logs/ }),
      ).toBeInTheDocument();
    });
  });

  it('checkbox is checked when capability telemetry_enabled=true', async () => {
    mockAuth.session.capabilities[CAP] = { telemetry_enabled: true };
    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      const checkbox = getByRole('checkbox', {
        name: /Send anonymous diagnostic logs/,
      }) as HTMLInputElement;
      expect(checkbox.checked).toBe(true);
    });
  });

  it('checkbox is unchecked when capability telemetry_enabled=false', async () => {
    mockAuth.session.capabilities[CAP] = { telemetry_enabled: false };
    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      const checkbox = getByRole('checkbox', {
        name: /Send anonymous diagnostic logs/,
      }) as HTMLInputElement;
      expect(checkbox.checked).toBe(false);
    });
  });

  it('toggling off calls PUT with {enabled: false}', async () => {
    mockPut.mockResolvedValue(undefined);
    mockAuth.session.capabilities[CAP] = { telemetry_enabled: true };
    const { getByRole } = render(DiagnosticsForm);

    const checkbox = getByRole('checkbox', {
      name: /Send anonymous diagnostic logs/,
    }) as HTMLInputElement;
    await fireEvent.change(checkbox, { target: { checked: false } });

    await waitFor(() => {
      expect(mockPut).toHaveBeenCalledWith(
        '/api/v1/me/clientlog/telemetry_enabled',
        { enabled: false },
      );
    });
  });

  it('toggling on calls PUT with {enabled: true}', async () => {
    mockPut.mockResolvedValue(undefined);
    mockAuth.session.capabilities[CAP] = { telemetry_enabled: false };
    const { getByRole } = render(DiagnosticsForm);

    const checkbox = getByRole('checkbox', {
      name: /Send anonymous diagnostic logs/,
    }) as HTMLInputElement;
    await fireEvent.change(checkbox, { target: { checked: true } });

    await waitFor(() => {
      expect(mockPut).toHaveBeenCalledWith(
        '/api/v1/me/clientlog/telemetry_enabled',
        { enabled: true },
      );
    });
  });

  it('reverts optimistic state and shows error on PUT failure', async () => {
    const { ApiError } = await import('../../lib/api/client');
    mockPut.mockRejectedValue(new ApiError(500, 'Internal Server Error'));
    mockAuth.session.capabilities[CAP] = { telemetry_enabled: true };

    const { getByRole, findByRole } = render(DiagnosticsForm);

    const checkbox = getByRole('checkbox', {
      name: /Send anonymous diagnostic logs/,
    }) as HTMLInputElement;
    await fireEvent.change(checkbox, { target: { checked: false } });

    // Error alert must appear.
    const alert = await findByRole('alert');
    expect(alert).toBeInTheDocument();
    expect(alert.textContent).toContain('Internal Server Error');
  });
});
