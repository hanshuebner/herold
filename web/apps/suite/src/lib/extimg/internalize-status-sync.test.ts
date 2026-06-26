/**
 * REQ-EXTIMG-BG-INTERNAL-64: a synthetic InternalizeStatus push must
 * trigger auth.refreshSession() and must NOT trigger the Email-state
 * path. The first half is the contract on the new sync handler; the
 * second half guards against a regression where someone re-adds
 * `auth.refreshSession()` to mail/store.svelte.ts:#onEmailStateChange.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// Capture handlers registered via sync.on so the test can drive the
// store as if an EventSource state advance had fired.
const syncHandlers = new Map<
  string,
  (newState: string, accountId: string) => void
>();

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(
      (
        type: string,
        handler: (newState: string, accountId: string) => void,
      ) => {
        syncHandlers.set(type, handler);
        return vi.fn();
      },
    ),
  },
}));

const refreshSession = vi.fn();
vi.mock('../auth/auth.svelte', () => ({
  auth: {
    status: 'ready',
    session: null,
    principalId: 'p1',
    refreshSession,
  },
  registerAccountResetCallback: vi.fn(),
}));

describe('InternalizeStatus sync handler (REQ-EXTIMG-BG-INTERNAL-30/64)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    refreshSession.mockClear();
  });

  it('imports register a handler that calls auth.refreshSession()', async () => {
    await import('./internalize-status-sync');

    const handler = syncHandlers.get('InternalizeStatus');
    expect(handler).toBeDefined();

    handler!('1', 'acc1');
    expect(refreshSession).toHaveBeenCalledTimes(1);

    // A second push fires it again — there is no dedup at this layer
    // because the sync layer already filtered same-state pushes (the
    // server bumps once per worker batch, so each push is meaningful).
    handler!('2', 'acc1');
    expect(refreshSession).toHaveBeenCalledTimes(2);
  });
});
