/**
 * Unit tests for the message-actions prefs store (re #60).
 *
 * Coverage:
 *   - default state matches the action registry order / visible counts
 *   - reorder: moves an item between positions
 *   - toggleVisible: promotes overflow item to primary / demotes primary to overflow
 *   - setVisibleCount: clamped writing
 *   - resetToDefaults: restores a single scope
 *   - resetAll: restores both scopes
 *   - persistence round-trip: saved JSON is correctly parsed back on load
 *   - repair: new action IDs appended when registry grows after prefs were saved
 *   - repair: unknown IDs from stale prefs are filtered out
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { MESSAGE_ACTIONS, THREAD_ACTIONS, DEFAULT_MSG_VISIBLE, DEFAULT_THREAD_VISIBLE } from './actions';

// ── localStorage stub ─────────────────────────────────────────────────────────
// happy-dom provides a real localStorage but we want isolation between tests.

const localStorageData: Record<string, string> = {};
const localStorageMock = {
  getItem: (k: string) => localStorageData[k] ?? null,
  setItem: (k: string, v: string) => { localStorageData[k] = v; },
  removeItem: (k: string) => { delete localStorageData[k]; },
  clear: () => { Object.keys(localStorageData).forEach((k) => delete localStorageData[k]); },
};
Object.defineProperty(globalThis, 'localStorage', { value: localStorageMock, writable: true });

const STORAGE_KEY = 'herold.msgActionsPrefs.v1';

// Re-import the store after every test to get a fresh $state from scratch.
// vitest module isolation via vi.resetModules() + dynamic import is the
// correct pattern for singleton rune stores.
async function freshPrefs() {
  vi.resetModules();
  const mod = await import('./messageActionsPrefs.svelte');
  return mod.messageActionsPrefs;
}

beforeEach(() => {
  localStorageMock.clear();
});

afterEach(() => {
  localStorageMock.clear();
});

// ── default state ─────────────────────────────────────────────────────────────

describe('messageActionsPrefs: default state', () => {
  it('message.order matches the MESSAGE_ACTIONS registry order', async () => {
    const prefs = await freshPrefs();
    expect(prefs.message.order).toEqual(MESSAGE_ACTIONS.map((a) => a.id));
  });

  it('thread.order matches the THREAD_ACTIONS registry order', async () => {
    const prefs = await freshPrefs();
    expect(prefs.thread.order).toEqual(THREAD_ACTIONS.map((a) => a.id));
  });

  it('message.visibleCount is DEFAULT_MSG_VISIBLE', async () => {
    const prefs = await freshPrefs();
    expect(prefs.message.visibleCount).toBe(DEFAULT_MSG_VISIBLE);
  });

  it('thread.visibleCount is DEFAULT_THREAD_VISIBLE', async () => {
    const prefs = await freshPrefs();
    expect(prefs.thread.visibleCount).toBe(DEFAULT_THREAD_VISIBLE);
  });
});

// ── reorder ───────────────────────────────────────────────────────────────────

describe('messageActionsPrefs: reorder', () => {
  it('moves item from index 0 to index 2', async () => {
    const prefs = await freshPrefs();
    const original = [...prefs.message.order];
    prefs.reorder('message', 0, 2);
    const expected = [...original];
    const moved = expected.splice(0, 1)[0]!;
    expected.splice(2, 0, moved);
    expect(prefs.message.order).toEqual(expected);
  });

  it('moving last item to first changes order correctly', async () => {
    const prefs = await freshPrefs();
    const original = [...prefs.message.order];
    const last = original.length - 1;
    prefs.reorder('message', last, 0);
    const expected = [...original];
    const moved = expected.splice(last, 1)[0]!;
    expected.splice(0, 0, moved);
    expect(prefs.message.order).toEqual(expected);
  });

  it('reorder persists to localStorage', async () => {
    const prefs = await freshPrefs();
    prefs.reorder('message', 0, 1);
    const raw = localStorageMock.getItem(STORAGE_KEY);
    expect(raw).not.toBeNull();
    const parsed = JSON.parse(raw!);
    expect(parsed.message.order).toEqual(prefs.message.order);
  });
});

// ── setVisibleCount ───────────────────────────────────────────────────────────

describe('messageActionsPrefs: setVisibleCount', () => {
  it('sets the visible count for the message scope', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('message', 6);
    expect(prefs.message.visibleCount).toBe(6);
  });

  it('sets the visible count for the thread scope', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('thread', 2);
    expect(prefs.thread.visibleCount).toBe(2);
  });

  it('persists visible count change', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('thread', 3);
    const raw = localStorageMock.getItem(STORAGE_KEY);
    const parsed = JSON.parse(raw!);
    expect(parsed.thread.visibleCount).toBe(3);
  });
});

// ── toggleVisible ─────────────────────────────────────────────────────────────

describe('messageActionsPrefs: toggleVisible', () => {
  it('promotes an overflow item into the primary row', async () => {
    const prefs = await freshPrefs();
    // The item at index visibleCount is in overflow — toggle it in.
    const overflowId = prefs.message.order[DEFAULT_MSG_VISIBLE]!;
    const before = prefs.message.visibleCount;
    prefs.toggleVisible('message', overflowId);
    expect(prefs.message.visibleCount).toBe(before + 1);
    expect(prefs.message.order.indexOf(overflowId)).toBeLessThan(prefs.message.visibleCount);
  });

  it('demotes a primary item to overflow', async () => {
    const prefs = await freshPrefs();
    // The last primary item.
    const primaryId = prefs.message.order[DEFAULT_MSG_VISIBLE - 1]!;
    const before = prefs.message.visibleCount;
    prefs.toggleVisible('message', primaryId);
    expect(prefs.message.visibleCount).toBe(before - 1);
  });

  it('refuses to hide the only primary action (minimum 1)', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('message', 1);
    const onlyPrimary = prefs.message.order[0]!;
    prefs.toggleVisible('message', onlyPrimary);
    // Count must stay at 1.
    expect(prefs.message.visibleCount).toBe(1);
  });

  it('ignores unknown ids', async () => {
    const prefs = await freshPrefs();
    const before = prefs.message.visibleCount;
    prefs.toggleVisible('message', 'non-existent-id');
    expect(prefs.message.visibleCount).toBe(before);
  });
});

// ── resetToDefaults ───────────────────────────────────────────────────────────

describe('messageActionsPrefs: resetToDefaults', () => {
  it('restores message scope to registry defaults', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('message', 8);
    prefs.reorder('message', 0, 3);
    prefs.resetToDefaults('message');
    expect(prefs.message.visibleCount).toBe(DEFAULT_MSG_VISIBLE);
    expect(prefs.message.order).toEqual(MESSAGE_ACTIONS.map((a) => a.id));
  });

  it('does not disturb the thread scope', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('thread', 6);
    prefs.resetToDefaults('message');
    expect(prefs.thread.visibleCount).toBe(6);
  });

  it('restores thread scope to registry defaults', async () => {
    const prefs = await freshPrefs();
    prefs.reorder('thread', 0, 2);
    prefs.resetToDefaults('thread');
    expect(prefs.thread.order).toEqual(THREAD_ACTIONS.map((a) => a.id));
  });
});

// ── resetAll ──────────────────────────────────────────────────────────────────

describe('messageActionsPrefs: resetAll', () => {
  it('restores both scopes to defaults', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('message', 7);
    prefs.setVisibleCount('thread', 1);
    prefs.resetAll();
    expect(prefs.message.visibleCount).toBe(DEFAULT_MSG_VISIBLE);
    expect(prefs.thread.visibleCount).toBe(DEFAULT_THREAD_VISIBLE);
    expect(prefs.message.order).toEqual(MESSAGE_ACTIONS.map((a) => a.id));
    expect(prefs.thread.order).toEqual(THREAD_ACTIONS.map((a) => a.id));
  });
});

// ── persistence round-trip ────────────────────────────────────────────────────

describe('messageActionsPrefs: persistence round-trip', () => {
  it('saves on mutation and loads correctly on next initialisation', async () => {
    const prefs = await freshPrefs();
    prefs.setVisibleCount('message', 3);
    prefs.reorder('thread', 0, 1);

    // Reload (simulates new page load reading the same localStorage).
    const prefs2 = await freshPrefs();
    expect(prefs2.message.visibleCount).toBe(3);
    // Thread order should reflect the reorder we did.
    const expected = [...THREAD_ACTIONS.map((a) => a.id)];
    const moved = expected.splice(0, 1)[0]!;
    expected.splice(1, 0, moved);
    expect(prefs2.thread.order).toEqual(expected);
  });
});

// ── repair: unknown IDs filtered, new IDs appended ────────────────────────────

describe('messageActionsPrefs: repair on load', () => {
  it('filters out stale ids not in the current registry', async () => {
    localStorageMock.setItem(STORAGE_KEY, JSON.stringify({
      message: {
        order: ['reply', 'DELETED_OLD_ACTION', 'forward'],
        visibleCount: 3,
      },
      thread: {
        order: THREAD_ACTIONS.map((a) => a.id),
        visibleCount: DEFAULT_THREAD_VISIBLE,
      },
    }));
    const prefs = await freshPrefs();
    expect(prefs.message.order).not.toContain('DELETED_OLD_ACTION');
    expect(prefs.message.order).toContain('reply');
    expect(prefs.message.order).toContain('forward');
  });

  it('appends new registry ids missing from saved prefs', async () => {
    // Simulate prefs that only have a subset of current registry ids.
    localStorageMock.setItem(STORAGE_KEY, JSON.stringify({
      message: {
        order: ['reply', 'forward'], // missing everything else
        visibleCount: 2,
      },
      thread: {
        order: THREAD_ACTIONS.map((a) => a.id),
        visibleCount: DEFAULT_THREAD_VISIBLE,
      },
    }));
    const prefs = await freshPrefs();
    // All current registry ids must be present.
    for (const a of MESSAGE_ACTIONS) {
      expect(prefs.message.order).toContain(a.id);
    }
  });

  it('handles corrupt JSON gracefully by falling back to defaults', async () => {
    localStorageMock.setItem(STORAGE_KEY, 'not-valid-json{{{');
    const prefs = await freshPrefs();
    expect(prefs.message.order).toEqual(MESSAGE_ACTIONS.map((a) => a.id));
    expect(prefs.thread.order).toEqual(THREAD_ACTIONS.map((a) => a.id));
  });
});

// ── action registry invariants ────────────────────────────────────────────────

describe('action registry: every action has a valid labelKey', () => {
  it('every MESSAGE_ACTION has a non-empty id and labelKey', () => {
    for (const a of MESSAGE_ACTIONS) {
      expect(a.id, `id empty for ${JSON.stringify(a)}`).toBeTruthy();
      expect(a.labelKey, `labelKey empty for ${a.id}`).toBeTruthy();
      expect(a.iconName, `iconName empty for ${a.id}`).toBeTruthy();
      expect(a.scope).toBe('message');
    }
  });

  it('every THREAD_ACTION has a non-empty id and labelKey', () => {
    for (const a of THREAD_ACTIONS) {
      expect(a.id, `id empty for ${JSON.stringify(a)}`).toBeTruthy();
      expect(a.labelKey, `labelKey empty for ${a.id}`).toBeTruthy();
      expect(a.iconName, `iconName empty for ${a.id}`).toBeTruthy();
      expect(a.scope).toBe('thread');
    }
  });

  it('all action ids are unique across both scopes', () => {
    const allIds = [...MESSAGE_ACTIONS, ...THREAD_ACTIONS].map((a) => a.id);
    const unique = new Set(allIds);
    expect(unique.size).toBe(allIds.length);
  });
});

// ── i18n coverage check ───────────────────────────────────────────────────────

describe('action registry: all labelKeys have en + de translations', () => {
  it('every action labelKey exists in the English catalogue', async () => {
    const { en } = await import('../i18n/en');
    for (const a of [...MESSAGE_ACTIONS, ...THREAD_ACTIONS]) {
      expect(
        Object.prototype.hasOwnProperty.call(en, a.labelKey),
        `Missing en key: ${a.labelKey} (action ${a.id})`,
      ).toBe(true);
    }
  });

  it('every action labelKey exists in the German catalogue', async () => {
    const { de } = await import('../i18n/de');
    for (const a of [...MESSAGE_ACTIONS, ...THREAD_ACTIONS]) {
      expect(
        Object.prototype.hasOwnProperty.call(de, a.labelKey),
        `Missing de key: ${a.labelKey} (action ${a.id})`,
      ).toBe(true);
    }
  });
});
