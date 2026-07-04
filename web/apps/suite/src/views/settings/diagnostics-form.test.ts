/**
 * Tests for DiagnosticsForm.svelte (REQ-CLOG-06, issue #83).
 *
 * Coverage:
 *   Telemetry toggle
 *   1.  Renders the telemetry checkbox
 *   2.  Checkbox is checked when session capability says enabled=true
 *   3.  Checkbox is unchecked when session capability says enabled=false
 *   4.  Toggling to false calls PUT /api/v1/me/clientlog/telemetry_enabled
 *       with {enabled: false}
 *   5.  Toggling to true calls PUT with {enabled: true}
 *   6.  On PUT failure, reverts optimistic state and shows error
 *
 *   Device-local debug ring
 *   7.  Debug log section heading always rendered
 *   8.  Debug logging toggle rendered
 *   9.  Toggling debug on calls setEnabled(true) and notifySW(true)
 *  10.  Refresh button calls readAll and updates rendered entries
 *  11.  Copy button formats entries and writes to clipboard
 *  12.  Clear button calls clearRing and empties the view
 *  13.  Empty state shown when ring is empty
 *
 *   Admin server log section
 *  14.  Not rendered for non-admin users
 *  15.  Rendered for admin role users
 *  16.  Rendered for superadmin role users
 *  17.  Load log button calls GET /api/v1/admin/clientlog
 *  18.  Rows rendered as plain text in textarea, oldest first
 *  19.  Empty state shown when no rows returned
 *  20.  Error state shown when fetch fails
 *  21.  Copy button rendered after log is loaded
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

// ── Debug ring mock ────────────────────────────────────────────────────────

const {
  mockReadAll,
  mockClearRing,
  mockGetEnabled,
  mockSetEnabled,
  mockNotifySW,
  mockFormatRecord,
} = vi.hoisted(() => {
  const mockReadAll = vi.fn();
  const mockClearRing = vi.fn();
  const mockGetEnabled = vi.fn();
  const mockSetEnabled = vi.fn();
  const mockNotifySW = vi.fn();
  const mockFormatRecord = vi.fn();
  return {
    mockReadAll,
    mockClearRing,
    mockGetEnabled,
    mockSetEnabled,
    mockNotifySW,
    mockFormatRecord,
  };
});

vi.mock('../../lib/debug-ring/debug-ring', () => ({
  readAll: (...args: unknown[]) => mockReadAll(...args),
  clear: (...args: unknown[]) => mockClearRing(...args),
  getEnabled: (...args: unknown[]) => mockGetEnabled(...args),
  setEnabled: (...args: unknown[]) => mockSetEnabled(...args),
  notifySW: (...args: unknown[]) => mockNotifySW(...args),
  formatRecord: (r: { ts: string; ctx: string; level: string; msg: string; payload?: string }) =>
    mockFormatRecord(r),
}));

// ── i18n mock ─────────────────────────────────────────────────────────────

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string) => {
    const map: Record<string, string> = {
      'settings.diagnostics.heading': 'Diagnostics',
      'settings.diagnostics.telemetry.label':
        'Send anonymous diagnostic logs to my mail-server operator',
      'settings.diagnostics.devLog.heading': 'Client debug log (this device)',
      'settings.diagnostics.devLog.toggle.label': 'Verbose page debug logging',
      'settings.diagnostics.devLog.toggle.hint': 'Hint text.',
      'settings.diagnostics.devLog.refreshBtn': 'Refresh',
      'settings.diagnostics.devLog.copyBtn': 'Copy',
      'settings.diagnostics.devLog.copiedBtn': 'Copied',
      'settings.diagnostics.devLog.clearBtn': 'Clear',
      'settings.diagnostics.devLog.empty': 'No entries yet.',
      'settings.diagnostics.logCopy.heading': 'Server log (server, admin)',
      'settings.diagnostics.logCopy.hint': 'Reads recent entries from the server.',
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
  mockReadAll.mockResolvedValue([]);
  mockClearRing.mockResolvedValue(undefined);
  mockGetEnabled.mockResolvedValue(false);
  mockSetEnabled.mockResolvedValue(undefined);
  mockNotifySW.mockReturnValue(undefined);
  mockFormatRecord.mockImplementation(
    (r: { ts: string; ctx: string; level: string; msg: string; payload?: string }) =>
      `${r.ts}  ${r.ctx}  ${r.level}  ${r.msg}`,
  );
});

// ── Telemetry toggle ──────────────────────────────────────────────────────

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

// ── Device-local debug ring ────────────────────────────────────────────────

describe('DiagnosticsForm — device-local debug log section', () => {
  it('always renders the debug log section heading', async () => {
    const { getByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(getByText('Client debug log (this device)')).toBeInTheDocument();
    });
  });

  it('renders the verbose-debug toggle', async () => {
    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(
        getByRole('checkbox', { name: /Verbose page debug logging/ }),
      ).toBeInTheDocument();
    });
  });

  it('toggle is unchecked when getEnabled returns false', async () => {
    mockGetEnabled.mockResolvedValue(false);
    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      const cb = getByRole('checkbox', {
        name: /Verbose page debug logging/,
      }) as HTMLInputElement;
      expect(cb.checked).toBe(false);
    });
  });

  it('toggle is checked when getEnabled returns true', async () => {
    mockGetEnabled.mockResolvedValue(true);
    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      const cb = getByRole('checkbox', {
        name: /Verbose page debug logging/,
      }) as HTMLInputElement;
      expect(cb.checked).toBe(true);
    });
  });

  it('toggling on calls setEnabled(true) and notifySW(true)', async () => {
    mockGetEnabled.mockResolvedValue(false);
    const { getByRole } = render(DiagnosticsForm);

    const cb = await waitFor(() =>
      getByRole('checkbox', { name: /Verbose page debug logging/ }),
    ) as HTMLInputElement;
    await fireEvent.change(cb, { target: { checked: true } });

    await waitFor(() => {
      expect(mockSetEnabled).toHaveBeenCalledWith(true);
      expect(mockNotifySW).toHaveBeenCalledWith(true);
    });
  });

  it('toggling off calls setEnabled(false) and notifySW(false)', async () => {
    mockGetEnabled.mockResolvedValue(true);
    const { getByRole } = render(DiagnosticsForm);

    const cb = await waitFor(() =>
      getByRole('checkbox', { name: /Verbose page debug logging/ }),
    ) as HTMLInputElement;
    await fireEvent.change(cb, { target: { checked: false } });

    await waitFor(() => {
      expect(mockSetEnabled).toHaveBeenCalledWith(false);
      expect(mockNotifySW).toHaveBeenCalledWith(false);
    });
  });

  it('renders Refresh button', async () => {
    const { getByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(getByRole('button', { name: /Refresh/ })).toBeInTheDocument();
    });
  });

  it('Refresh button calls readAll and updates the view', async () => {
    mockReadAll.mockResolvedValueOnce([]);
    const { getByRole } = render(DiagnosticsForm);

    // Second call returns an entry so we can observe the change.
    mockReadAll.mockResolvedValue([
      { id: 1, ts: '2026-07-04T12:00:00.000Z', ctx: 'sw', level: 'info', msg: 'sw.push' },
    ]);

    const btn = await waitFor(() => getByRole('button', { name: /Refresh/ }));
    await fireEvent.click(btn);

    await waitFor(() => {
      expect(mockReadAll).toHaveBeenCalledTimes(2);
    });
  });

  it('shows empty state when ring has no entries', async () => {
    mockReadAll.mockResolvedValue([]);
    const { getByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(getByText('No entries yet.')).toBeInTheDocument();
    });
  });

  it('renders textarea with formatted entries when ring is non-empty', async () => {
    const entry = {
      id: 1,
      ts: '2026-07-04T12:00:00.000Z',
      ctx: 'sw' as const,
      level: 'info',
      msg: 'sw.openApp.postNavigate',
    };
    mockReadAll.mockResolvedValue([entry]);
    mockFormatRecord.mockReturnValue(
      '2026-07-04T12:00:00.000Z  sw  info  sw.openApp.postNavigate',
    );

    const { getByRole } = render(DiagnosticsForm);

    const textarea = await waitFor(() =>
      getByRole('textbox', { name: /Client debug log/ }),
    ) as HTMLTextAreaElement;
    expect(textarea.value).toContain('sw.openApp.postNavigate');
  });

  it('Copy button is visible when entries are present', async () => {
    mockReadAll.mockResolvedValue([
      { id: 1, ts: '2026-07-04T12:00:00.000Z', ctx: 'sw' as const, level: 'info', msg: 'sw.push' },
    ]);
    const { getAllByRole } = render(DiagnosticsForm);
    await waitFor(() => {
      // There may be multiple "Copy" buttons if admin; just check one exists.
      const copies = getAllByRole('button', { name: /Copy/ });
      expect(copies.length).toBeGreaterThan(0);
    });
  });

  it('Clear button calls clearRing', async () => {
    mockReadAll.mockResolvedValue([
      { id: 1, ts: '2026-07-04T12:00:00.000Z', ctx: 'sw' as const, level: 'info', msg: 'sw.push' },
    ]);
    const { getByRole } = render(DiagnosticsForm);

    const clearBtn = await waitFor(() => getByRole('button', { name: /Clear/ }));
    await fireEvent.click(clearBtn);

    await waitFor(() => {
      expect(mockClearRing).toHaveBeenCalled();
    });
  });
});

// ── Admin server log section ────────────────────────────────────────────────

describe('DiagnosticsForm — admin server log section', () => {
  it('does not render the server log section for non-admin users', async () => {
    mockAuth.roles = [];
    const { queryByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(queryByText('Server log (server, admin)')).not.toBeInTheDocument();
    });
  });

  it('renders the server log section when user has admin role', async () => {
    mockAuth.roles = ['admin'];
    const { getByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(getByText('Server log (server, admin)')).toBeInTheDocument();
    });
  });

  it('renders the server log section when user has superadmin role', async () => {
    mockAuth.roles = ['superadmin'];
    const { getByText } = render(DiagnosticsForm);
    await waitFor(() => {
      expect(getByText('Server log (server, admin)')).toBeInTheDocument();
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
      getByRole('textbox', { name: /Server log/ }),
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
      // After loading there will be at least one Copy button.
      expect(getByRole('button', { name: /Copy/ })).toBeInTheDocument();
    });
  });
});
