/**
 * compose-failure.ts unit tests.
 *
 * REQ-MAIL-SUBMIT-06 — compose failure toast + re-auth shortcut.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  showExternalSubmissionFailure,
  parseExternalSubmissionFailure,
  type ExternalSubmissionFailureCategory,
} from './compose-failure';

// ── Mock dependencies ─────────────────────────────────────────────────────

vi.mock('../toast/toast.svelte', () => ({
  toast: {
    show: vi.fn(),
    dismiss: vi.fn(),
    current: null,
  },
}));

vi.mock('../router/router.svelte', () => ({
  router: {
    navigate: vi.fn(),
    current: '/mail',
    parts: ['mail'],
    getParam: vi.fn(() => null),
    setParam: vi.fn(),
  },
}));

// Import mocks after vi.mock declarations.
const { toast } = await import('../toast/toast.svelte');
const { router } = await import('../router/router.svelte');

// ── parseExternalSubmissionFailure ────────────────────────────────────────

describe('parseExternalSubmissionFailure', () => {
  it('returns null for non-external-submission errors', () => {
    expect(parseExternalSubmissionFailure({ type: 'serverFail', description: 'oops' })).toBeNull();
    expect(parseExternalSubmissionFailure({ type: 'invalidArguments' })).toBeNull();
  });

  it('extracts auth-failed category', () => {
    expect(
      parseExternalSubmissionFailure({
        type: 'external-submission-failed',
        category: 'auth-failed',
      }),
    ).toBe('auth-failed');
  });

  it('extracts unreachable category', () => {
    expect(
      parseExternalSubmissionFailure({
        type: 'external-submission-failed',
        category: 'unreachable',
      }),
    ).toBe('unreachable');
  });

  it('extracts permanent category', () => {
    expect(
      parseExternalSubmissionFailure({
        type: 'external-submission-failed',
        category: 'permanent',
      }),
    ).toBe('permanent');
  });

  it('extracts transient category', () => {
    expect(
      parseExternalSubmissionFailure({
        type: 'external-submission-failed',
        category: 'transient',
      }),
    ).toBe('transient');
  });

  it('falls back to permanent for unknown category on external-submission-failed', () => {
    expect(
      parseExternalSubmissionFailure({
        type: 'external-submission-failed',
        category: 'bogus',
      }),
    ).toBe('permanent');
  });

  it('falls back to permanent when category is absent', () => {
    expect(
      parseExternalSubmissionFailure({
        type: 'external-submission-failed',
      }),
    ).toBe('permanent');
  });
});

// ── showExternalSubmissionFailure ─────────────────────────────────────────

describe('showExternalSubmissionFailure', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('auth-failed: shows toast with Re-authenticate action label and no auto-dismiss', () => {
    showExternalSubmissionFailure({
      category: 'auth-failed',
      identityId: 'id-123',
    });

    expect(toast.show).toHaveBeenCalledOnce();
    const spec = vi.mocked(toast.show).mock.calls[0]![0];
    expect(spec.kind).toBe('error');
    expect(spec.timeoutMs).toBe(0);
    expect(spec.actionLabel).toBe('Re-authenticate');
    expect(typeof spec.undo).toBe('function');
    expect(spec.message).toContain('Authentication failed');
  });

  it('auth-failed: Re-authenticate navigates to settings with identity param', () => {
    showExternalSubmissionFailure({
      category: 'auth-failed',
      identityId: 'id-abc',
    });

    const spec = vi.mocked(toast.show).mock.calls[0]![0];
    // Invoke the undo (re-auth) action.
    spec.undo!();

    expect(router.navigate).toHaveBeenCalledOnce();
    const navArg = vi.mocked(router.navigate).mock.calls[0]![0] as string;
    expect(navArg).toContain('/settings/account');
    expect(navArg).toContain('identity=id-abc');
    expect(navArg).toContain('action=reauth');
  });

  it('auth-failed: includes diagnostic in message when provided', () => {
    showExternalSubmissionFailure({
      category: 'auth-failed',
      identityId: 'id-x',
      diagnostic: '535 Bad credentials',
    });

    const spec = vi.mocked(toast.show).mock.calls[0]![0];
    expect(spec.message).toContain('535 Bad credentials');
  });

  it('unreachable: shows toast without undo / re-auth button', () => {
    showExternalSubmissionFailure({
      category: 'unreachable',
      identityId: 'id-123',
    });

    const spec = vi.mocked(toast.show).mock.calls[0]![0];
    expect(spec.kind).toBe('error');
    // timeoutMs should be non-zero (auto-dismiss for transport failures).
    expect((spec.timeoutMs ?? 5000) > 0).toBe(true);
    expect(spec.undo).toBeUndefined();
    // The message mentions the server could not be reached.
    expect(spec.message).toContain('could not be reached');
  });

  it('permanent: shows informational toast without re-auth', () => {
    showExternalSubmissionFailure({
      category: 'permanent',
      identityId: 'id-123',
    });

    const spec = vi.mocked(toast.show).mock.calls[0]![0];
    expect(spec.undo).toBeUndefined();
    expect(spec.message).toContain('permanently rejected');
  });

  it('transient: shows informational toast without re-auth', () => {
    showExternalSubmissionFailure({
      category: 'transient',
      identityId: 'id-123',
    });

    const spec = vi.mocked(toast.show).mock.calls[0]![0];
    expect(spec.undo).toBeUndefined();
    expect(spec.message).toContain('temporary error');
  });
});

// ── Capability gating (toast should only appear when capability present) ──

describe('capability gating in compose-failure', () => {
  it('parseExternalSubmissionFailure returns null for non-matching types regardless of capability', () => {
    // The capability gate is enforced by compose.svelte.ts calling
    // hasExternalSubmission() before invoking parseExternalSubmissionFailure.
    // The helper itself is capability-agnostic — it just parses the JMAP error.
    const result = parseExternalSubmissionFailure({
      type: 'serverFail',
      description: 'not an external failure',
    });
    expect(result).toBeNull();
  });
});
