/**
 * Tests for MailStore.primaryIdentity — the getter that determines the
 * default From identity prefilled in compose (REQ-MAIL-12, REQ-SET-IDENT-04).
 *
 * The getter delegates to resolveDefault() from identity-status.ts, which
 * prefers the identity flagged isDefault: true and falls back to the first
 * verified identity in stable sort order. These tests drive the getter via
 * the live mail singleton (same pattern as store.reset.test.ts) and verify
 * the priority rules.
 */

import { describe, it, expect, vi } from 'vitest';
import type { Identity } from './types';

// ── Stub dependencies loaded by store.svelte.ts ────────────────────────────

vi.mock('../jmap/sync.svelte', () => ({
  sync: { on: vi.fn(() => vi.fn()) },
}));
vi.mock('../jmap/client', () => ({
  jmap: { batch: vi.fn(), session: null, uploadBlob: vi.fn(), downloadUrl: vi.fn() },
  strict: (r: unknown[]) => r,
}));
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: { capabilities: {}, primaryAccounts: {} },
    principalId: 'p1',
  },
  registerAccountResetCallback: vi.fn(),
}));
vi.mock('../toast/toast.svelte', () => ({ toast: { show: vi.fn() } }));
vi.mock('../notifications/sounds.svelte', () => ({ sounds: { play: vi.fn(), enabled: false } }));
vi.mock('../notifications/cue-gates', () => ({ shouldPlayMailCue: () => false }));
vi.mock('../router/router.svelte', () => ({ router: { parts: [], getParam: () => null } }));
vi.mock('../settings/settings.svelte', () => ({
  settings: { desktopNotifEnabled: false, isImageAllowed: () => false },
}));
vi.mock('../storage/account-scoped', () => ({
  accountKey: (_key: string) => _key,
}));
vi.mock('../debug-ring/debug-ring', () => ({ appendEvent: vi.fn() }));

// ── Helpers ────────────────────────────────────────────────────────────────

/** Minimal verified identity stub. */
function makeIdentity(overrides: Partial<Identity> & Pick<Identity, 'id' | 'email'>): Identity {
  return {
    name: overrides.email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
    // verifiedAt absent → legacy-server compatible (treated as verified).
    ...overrides,
  };
}

// ── Tests ──────────────────────────────────────────────────────────────────

const { mail } = await import('./store.svelte');

describe('MailStore.primaryIdentity', () => {
  it('returns null when the identity map is empty', () => {
    mail.identities = new Map();
    expect(mail.primaryIdentity).toBeNull();
  });

  it('returns the isDefault identity even when it is not first in insertion order', () => {
    const first = makeIdentity({ id: 'id-first', email: 'first@example.com' });
    const second = makeIdentity({ id: 'id-default', email: 'default@example.com', isDefault: true });
    const third = makeIdentity({ id: 'id-third', email: 'third@example.com' });

    // Insert in order: first, second (isDefault), third.
    mail.identities = new Map([
      [first.id, first],
      [second.id, second],
      [third.id, third],
    ]);

    const result = mail.primaryIdentity;
    expect(result).not.toBeNull();
    expect(result!.id).toBe('id-default');
  });

  it('prefers the isDefault identity over the alphabetically-first identity', () => {
    const alpha = makeIdentity({ id: 'id-alpha', email: 'aardvark@example.com' });
    const zulu = makeIdentity({
      id: 'id-zulu',
      email: 'zulu@example.com',
      isDefault: true,
    });

    mail.identities = new Map([
      [alpha.id, alpha],
      [zulu.id, zulu],
    ]);

    expect(mail.primaryIdentity!.id).toBe('id-zulu');
  });

  it('falls back to the first verified identity (alphabetically) when no isDefault is set', () => {
    // Both identities are verified (verifiedAt absent = legacy-compat treated as verified).
    // resolveDefault sorts verified identities alphabetically and picks the first.
    const bravo = makeIdentity({ id: 'id-b', email: 'bravo@example.com' });
    const alpha = makeIdentity({ id: 'id-a', email: 'alpha@example.com' });

    // Insert bravo first so insertion order would pick bravo, but alpha comes first alphabetically.
    mail.identities = new Map([
      [bravo.id, bravo],
      [alpha.id, alpha],
    ]);

    expect(mail.primaryIdentity!.id).toBe('id-a');
  });

  it('returns null when every identity is unverified and no isDefault is set', () => {
    const unverified = makeIdentity({
      id: 'id-unverified',
      email: 'pend@example.com',
      verifiedAt: null,
      verificationPendingSince: null,
    });

    mail.identities = new Map([[unverified.id, unverified]]);

    expect(mail.primaryIdentity).toBeNull();
  });

  it('honours isDefault even on an otherwise-unverified identity', () => {
    // resolveDefault checks isDefault before checking verified status.
    const defaultButUnverified = makeIdentity({
      id: 'id-default-unverified',
      email: 'default@example.com',
      isDefault: true,
      verifiedAt: null,
      verificationPendingSince: null,
    });
    const verified = makeIdentity({ id: 'id-verified', email: 'other@example.com' });

    mail.identities = new Map([
      [verified.id, verified],
      [defaultButUnverified.id, defaultButUnverified],
    ]);

    // resolveDefault returns the isDefault one first regardless of verification.
    expect(mail.primaryIdentity!.id).toBe('id-default-unverified');
  });
});
