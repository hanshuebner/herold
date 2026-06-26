/**
 * SecurityForm.svelte — TOTP QR code rendering tests (re #13).
 *
 * Verifies that:
 *   1. A QR code SVG renders (non-empty, contains rect modules) when
 *      the user starts TOTP enrollment via startEnroll().
 *   2. The QR renders at the configured 320x320 dimension with a
 *      provisioning_uri from the server.
 *
 * This test covers the path the browser verification screenshot
 * demonstrates: settings/security -> Enable 2FA -> QR visible.
 * The password-change and TOTP-disable flows are exercised in the
 * live-browser puppeteer session; this file handles the structural
 * QR-presence assertion that automated tests can make deterministically.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import SecurityForm from './SecurityForm.svelte';

// ── Auth mock ─────────────────────────────────────────────────────────────

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    principalId: '42',
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'account-1' },
      capabilities: {},
      username: 'test@example.local',
    },
  },
  registerAccountResetCallback: vi.fn(),
}));

// ── API client mock ────────────────────────────────────────────────────────

const mockGet = vi.fn();
const mockPost = vi.fn();
const mockPut = vi.fn();
const mockDel = vi.fn();

vi.mock('../../lib/api/client', () => ({
  get: (...args: unknown[]) => mockGet(...args),
  post: (...args: unknown[]) => mockPost(...args),
  put: (...args: unknown[]) => mockPut(...args),
  del: (...args: unknown[]) => mockDel(...args),
  ApiError: class ApiError extends Error {
    constructor(
      public status: number,
      message: string,
    ) {
      super(message);
      this.name = 'ApiError';
    }
  },
  UnauthenticatedError: class UnauthenticatedError extends Error {
    constructor(message: string) {
      super(message);
      this.name = 'UnauthenticatedError';
    }
  },
}));

// ── Dialog and toast mocks ─────────────────────────────────────────────────

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => true) },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

// ── i18n mock ─────────────────────────────────────────────────────────────

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string) => key,
}));

// ── Fixtures ─────────────────────────────────────────────────────────────

const PRINCIPAL_NOT_ENROLLED = {
  id: '42',
  canonical_email: 'test@example.local',
  display_name: 'Test User',
  totp_enabled: false,
  flags: [],
};

const PROVISIONING_URI =
  'otpauth://totp/test%40example.local?secret=JBSWY3DPEHPK3PXP&issuer=Herold&algorithm=SHA1&digits=6&period=30';

// ── Tests ─────────────────────────────────────────────────────────────────

describe('SecurityForm — TOTP QR rendering (re #13)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Default: principal fetch succeeds with TOTP not enrolled.
    mockGet.mockResolvedValue(PRINCIPAL_NOT_ENROLLED);
  });

  it('renders a non-empty QR SVG after enrollment starts with a provisioning_uri', async () => {
    mockPost.mockResolvedValueOnce({
      secret: 'JBSWY3DPEHPK3PXP',
      provisioning_uri: PROVISIONING_URI,
    });

    const { container } = render(SecurityForm);

    // Wait for the principal GET to resolve and the form to render.
    // The spinner disappears and the Two-factor section becomes visible.
    await waitFor(() => {
      // The TOTP status div appears once loading is done.
      expect(container.querySelector('.totp-status')).not.toBeNull();
    });

    // Find the "Enable 2FA" button (primary button in the not-enrolled state).
    // With our t(key)=>key mock its text is the i18n key.
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const enableBtn = buttons.find(
      (b) => b.textContent?.includes('settings.security.enable2fa'),
    );
    expect(enableBtn).not.toBeNull();
    await fireEvent.click(enableBtn!);

    // Wait for the POST to resolve and the QR container to appear.
    await waitFor(() => {
      expect(container.querySelector('.totp-qr')).not.toBeNull();
    });

    const qrEl = container.querySelector('.totp-qr') as HTMLElement;
    expect(qrEl).not.toBeNull();

    // qrcode-svg injects an SVG into the .totp-qr div via {@html}.
    // Verify that the SVG is present and has QR module rectangles.
    const svgContent = qrEl.innerHTML;
    expect(svgContent).toContain('<svg');
    expect(svgContent).toContain('</svg>');
    // A real QR code for a ~120-char otpauth URI encodes >100 modules;
    // each module is a <rect> in qrcode-svg output.
    expect((svgContent.match(/<rect/g) ?? []).length).toBeGreaterThan(10);
  });

  it('QR SVG has the configured 320x320 dimensions', async () => {
    mockPost.mockResolvedValueOnce({
      secret: 'JBSWY3DPEHPK3PXP',
      provisioning_uri: PROVISIONING_URI,
    });

    const { container } = render(SecurityForm);

    await waitFor(() => {
      expect(container.querySelector('.totp-status')).not.toBeNull();
    });

    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const enableBtn = buttons.find(
      (b) => b.textContent?.includes('settings.security.enable2fa'),
    );
    await fireEvent.click(enableBtn!);

    await waitFor(() => {
      expect(container.querySelector('.totp-qr')).not.toBeNull();
    });

    const qrEl = container.querySelector('.totp-qr') as HTMLElement;
    // qrcode-svg emits width="320" height="320" on the root SVG per the
    // SecurityForm's startEnroll() call-site parameters (re #13).
    expect(qrEl.innerHTML).toContain('width="320"');
    expect(qrEl.innerHTML).toContain('height="320"');
  });
});
