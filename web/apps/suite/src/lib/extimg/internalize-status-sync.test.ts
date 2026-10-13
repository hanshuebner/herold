/**
 * REQ-EXTIMG-BG-INTERNAL-64: an InternalizeStatus push whose state
 * differs from the last one seen triggers auth.refreshSession(); a push
 * that repeats the state (every StateChange carries it, e.g. the one a
 * mail delivery produces) does not.
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
  registerAccountResetCallback: vi.fn((cb: () => void) => {
    resetCallbacks.push(cb);
  }),
}));

const resetCallbacks: Array<() => void> = [];

describe('InternalizeStatus sync handler (REQ-EXTIMG-BG-INTERNAL-30/64)', () => {
  beforeEach(() => {
    syncHandlers.clear();
    resetCallbacks.length = 0;
    refreshSession.mockClear();
    vi.resetModules();
  });

  async function handler(): Promise<(newState: string, accountId: string) => void> {
    await import('./internalize-status-sync');
    const h = syncHandlers.get('InternalizeStatus');
    expect(h).toBeDefined();
    return h!;
  }

  it('takes the first state as the baseline and refreshes when it advances', async () => {
    const h = await handler();
    h('1', 'acc1');
    expect(refreshSession).not.toHaveBeenCalled();
    h('2', 'acc1');
    expect(refreshSession).toHaveBeenCalledTimes(1);
    h('3', 'acc1');
    expect(refreshSession).toHaveBeenCalledTimes(2);
  });

  it('ignores pushes that repeat the InternalizeStatus state', async () => {
    const h = await handler();
    h('5', 'acc1');
    h('5', 'acc1');
    h('5', 'acc1');
    expect(refreshSession).not.toHaveBeenCalled();
  });

  it('tracks each account separately', async () => {
    const h = await handler();
    h('1', 'acc1');
    h('7', 'acc2');
    expect(refreshSession).not.toHaveBeenCalled();
    h('7', 'acc2');
    expect(refreshSession).not.toHaveBeenCalled();
    h('2', 'acc1');
    expect(refreshSession).toHaveBeenCalledTimes(1);
  });

  it('starts a fresh baseline after an account reset', async () => {
    const h = await handler();
    h('1', 'acc1');
    for (const cb of resetCallbacks) cb();
    h('9', 'acc1');
    expect(refreshSession).not.toHaveBeenCalled();
  });
});
