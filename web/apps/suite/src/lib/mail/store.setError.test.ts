/**
 * Unit tests for the setErrorToUserMessage helper in the mail store
 * (issue #17). The function maps JMAP setError type strings to
 * localised user-facing messages, falling back to the server-supplied
 * description when present, and to a generic "Something went wrong"
 * key when the type is unknown.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { i18n } from '../i18n/i18n.svelte';

// Mock all heavy store dependencies so the module can be loaded
// without a real JMAP session or browser environment.  i18n is NOT
// mocked here — we assert on the real translated English strings.

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
    session: {
      capabilities: { 'urn:ietf:params:jmap:mail': {} },
      primaryAccounts: {
        'urn:ietf:params:jmap:mail': 'acc1',
        'urn:ietf:params:jmap:submission': 'acc1',
      },
    },
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

// Import after mocks are in place.
const { _internals_forTest } = await import('./store.svelte');
const { setErrorToUserMessage } = _internals_forTest;

describe('setErrorToUserMessage (issue #17)', () => {
  beforeEach(() => {
    i18n.locale = 'en';
  });

  it('maps notFound to the localised string', () => {
    expect(setErrorToUserMessage({ type: 'notFound' })).toBe(
      'That message no longer exists.',
    );
  });

  it('maps forbidden to the localised string', () => {
    expect(setErrorToUserMessage({ type: 'forbidden' })).toBe(
      'You do not have permission for that action.',
    );
  });

  it('maps invalidProperties to the localised string', () => {
    expect(setErrorToUserMessage({ type: 'invalidProperties' })).toBe(
      'The change was rejected as invalid.',
    );
  });

  it('maps overQuota to the localised string', () => {
    expect(setErrorToUserMessage({ type: 'overQuota' })).toBe(
      'Your mailbox is over quota.',
    );
  });

  it('maps unknown types to the generic fallback', () => {
    expect(setErrorToUserMessage({ type: 'somethingNew' })).toBe(
      'Something went wrong.',
    );
  });

  it('prefers the server description over the type mapping', () => {
    expect(
      setErrorToUserMessage({ type: 'notFound', description: 'Custom server message' }),
    ).toBe('Custom server message');
  });

  it('ignores empty description and falls through to type mapping', () => {
    expect(setErrorToUserMessage({ type: 'notFound', description: '' })).toBe(
      'That message no longer exists.',
    );
  });

  it('maps tooManyMailboxes to the localised string', () => {
    expect(setErrorToUserMessage({ type: 'tooManyMailboxes' })).toBe(
      'Too many mailboxes for one message.',
    );
  });

  it('maps serverFail to the localised string', () => {
    expect(setErrorToUserMessage({ type: 'serverFail' })).toBe(
      'The server returned an error.',
    );
  });
});
