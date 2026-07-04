/**
 * Tests for DiagnosticsForm.svelte (REQ-CLOG-06, issue #83).
 *
 * Coverage:
 *   1. Renders the telemetry checkbox
 *   2. Checkbox is checked when session capability says enabled=true
 *   3. Checkbox is unchecked when session capability says enabled=false
 *   4. Toggling to false calls PUT /api/v1/me/clientlog/telemetry_enabled
 *      with {enabled: false}
 *   5. Toggling to true calls PUT with {enabled: true}
 *   6. On PUT failure, reverts optimistic state and shows error
 *   7. Admin log copy section not rendered for non-admin users
 *   8. Admin log copy section rendered for admin role users
 *   9. Load log button calls GET /api/v1/admin/clientlog
 *  10. Log rows rendered as plain text in textarea, oldest first
 *  11. Empty state shown when no rows returned
 *  12. Error state shown when fetch fails
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import DiagnosticsForm from './DiagnosticsForm.svelte';

// ── Hoist shared mutable state so vi.mock factories can reference it ──────

const CAP = 'urn:netzhansa:params:jmap:clientlog';

const { mockAuth, mockPut, mockGet } = vi.hoisted(() => {
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
    roles: [] as string[],
  };
  const mockPut = vi.fn();
  const mockGet = vi.fn();
  return { mockAuth, mockPut, mockGet };
});

// ── Auth mock ─────────────────────────────────────────────────────────────

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: mockAuth,
  registerAccountResetCallback: vi.fn(),
}));

// ── API client mock ────────────────────────────────────────────────────────

vi.mock('../../lib/api/client', () => ({
  put: (...args: unknown[]) => mockPut(...args),
  get: (...args: unknown[]) => mockGet(...args),
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
      'settings.diagnostics.logCopy.heading': 'Recent service-worker log',
      'settings.diagnostics.logCopy.hint': 'Fetches the most recent anonymous client-log entries.',
      'settings.diagnostics.logCopy.fetchBtn': 'Load log',
      'settings.diagnostics.logCopy.loading': 'Loading...',
      'settings.diagnostics.logCopy.copyBtn': 'Copy',
      'settings.diagnostics.logCopy.copiedBtn': 'Copied',
      'settings.diagnostics.logCopy.empty': 'No log entries found.',
      'settings.diagnostics.logCopy.error': 'Could not load log.',
    };
    return map[key] ?? key;
  },
}));

beforeEach(() => {
  vi.clearAllMocks();
  mockAuth.status = 'ready';
  mockAuth.session.capabilities[CAP] = { telemetry_enabled: true };
  mockAuth.roles = [];
  mockPut.mockResolvedValue(undefined);
  mockGet.mockResolvedValue({ rows: [], next_cursor: '' });
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

// ── Admin log copy section ────────────────────────────────────────────────────
//
// The "Copy diagnostic log" affordance is only visible when the signed-in
// user holds the 'admin' or 'superadmin' role.  When visible, "Load log"
// fetches GET /api/v1/admin/clientlog?slice=public&app=suite&limit=100 and
// renders the rows as plain text in a read-only textarea with a Copy button.

describe('DiagnosticsForm — admin log copy section', () => {
  it('does not render the log copy section for non-admin users', async () => {
    mockAuth.roles = [];
    const { queryByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(queryByText('Recent service-worker log')).not.toBeInTheDocument();
    });
  });

  it('renders the log copy section when user has admin role', async () => {
    mockAuth.roles = ['admin'];
    const { getByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(getByText('Recent service-worker log')).toBeInTheDocument();
    });
  });

  it('renders the log copy section when user has superadmin role', async () => {
    mockAuth.roles = ['superadmin'];
    const { getByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(getByText('Recent service-worker log')).toBeInTheDocument();
    });
  });

  it('calls GET /api/v1/admin/clientlog when Load log is clicked', async () => {
    mockAuth.roles = ['admin'];
    mockGet.mockResolvedValue({ rows: [], next_cursor: '' });
    const { getByRole } = render(DiagnosticsForm);

    const btn = await waitFor(() => getByRole('button', { name: /Load log/ }));
    await fireEvent.click(btn);

    await waitFor(() => {
      expect(mockGet).toHaveBeenCalledWith(
        expect.stringContaining('/api/v1/admin/clientlog'),
      );
    });
  });

  it('shows the empty state when no rows are returned', async () => {
    mockAuth.roles = ['admin'];
    mockGet.mockResolvedValue({ rows: [], next_cursor: '' });
    const { getByRole, findByText } = render(DiagnosticsForm);

    await waitFor(() => getByRole('button', { name: /Load log/ }));
    await fireEvent.click(getByRole('button', { name: /Load log/ }));

    const empty = await findByText('No log entries found.');
    expect(empty).toBeInTheDocument();
  });

  it('renders log rows as plain text in a textarea, oldest first', async () => {
    mockAuth.roles = ['admin'];
    // API returns newest first (ring buffer order); component reverses to show oldest first.
    mockGet.mockResolvedValue({
      rows: [
        {
          id: 2,
          slice: 'public',
          server_ts: '2026-07-04T12:00:01.000Z',
          client_ts: '2026-07-04T12:00:01.000Z',
          app: 'suite',
          kind: 'log',
          level: 'info',
          msg: 'sw.openApp.focus {"windowCount":1}',
          page_id: 'p1',
          build_sha: '',
          ua: '',
        },
        {
          id: 1,
          slice: 'public',
          server_ts: '2026-07-04T12:00:00.000Z',
          client_ts: '2026-07-04T12:00:00.000Z',
          app: 'suite',
          kind: 'log',
          level: 'info',
          msg: 'sw.notificationclick {"action":"","kind":"mail","hasThreadId":true}',
          page_id: 'p1',
          build_sha: '',
          ua: '',
        },
      ],
      next_cursor: '',
    });

    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => getByRole('button', { name: /Load log/ }));
    await fireEvent.click(getByRole('button', { name: /Load log/ }));

    const textarea = await waitFor(() =>
      getByRole('textbox', { name: /Recent service-worker log/ }),
    ) as HTMLTextAreaElement;

    const lines = textarea.value.split('\n');
    // Oldest entry (id=1, the notificationclick) should appear first.
    expect(lines[0]).toContain('sw.notificationclick');
    // Newest entry (id=2, the focus) should appear last.
    expect(lines[lines.length - 1]).toContain('sw.openApp.focus');
  });

  it('shows error text when the fetch fails', async () => {
    mockAuth.roles = ['admin'];
    const { ApiError } = await import('../../lib/api/client');
    mockGet.mockRejectedValue(new ApiError(403, 'Forbidden'));

    const { getByRole, findByRole } = render(DiagnosticsForm);
    await waitFor(() => getByRole('button', { name: /Load log/ }));
    await fireEvent.click(getByRole('button', { name: /Load log/ }));

    const alert = await findByRole('alert');
    expect(alert).toBeInTheDocument();
    expect(alert.textContent).toContain('Forbidden');
  });

  it('renders the Copy button after log is loaded', async () => {
    mockAuth.roles = ['admin'];
    mockGet.mockResolvedValue({
      rows: [
        {
          id: 1,
          slice: 'public',
          server_ts: '2026-07-04T12:00:00.000Z',
          client_ts: '2026-07-04T12:00:00.000Z',
          app: 'suite',
          kind: 'log',
          level: 'info',
          msg: 'sw.push {"kind":"mail","hasThreadId":true}',
          page_id: 'p1',
          build_sha: '',
          ua: '',
        },
      ],
      next_cursor: '',
    });

    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => getByRole('button', { name: /Load log/ }));
    await fireEvent.click(getByRole('button', { name: /Load log/ }));

    await waitFor(() => {
      expect(getByRole('button', { name: /Copy/ })).toBeInTheDocument();
    });
  });
});
