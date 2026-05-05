/**
 * Per-account preferences for message and thread action toolbars (re #60).
 *
 * Stores:
 *   - ordered list of action IDs for each scope
 *   - how many are "primary" (shown in the toolbar with text labels)
 *   - the rest go into the overflow "More actions" menu
 *
 * Persistence: localStorage keyed by `herold.msgActionsPrefs.v1`.
 * TODO: migrate to server-persisted JMAP custom-property storage once the
 * server-side custom-properties endpoint is wired (follow-up to re #60).
 * For now localStorage is used because no per-account key-value storage
 * API exists in the JMAP session today. The key includes "v1" so a future
 * schema change can migrate without clobbering old data.
 *
 * The module exports a single `messageActionsPrefs` singleton that
 * MessageAccordion, ThreadToolbar, and the Settings UI all import.
 */

import {
  MESSAGE_ACTIONS,
  THREAD_ACTIONS,
  DEFAULT_MSG_VISIBLE,
  DEFAULT_THREAD_VISIBLE,
} from './actions';

const STORAGE_KEY = 'herold.msgActionsPrefs.v1';

export interface ScopePrefs {
  /** Ordered action IDs. Position determines toolbar order. */
  order: string[];
  /** How many leading items are shown in the primary toolbar (with text label). */
  visibleCount: number;
}

export interface MsgActionsPrefs {
  message: ScopePrefs;
  thread: ScopePrefs;
}

function defaultPrefs(): MsgActionsPrefs {
  return {
    message: {
      order: MESSAGE_ACTIONS.map((a) => a.id),
      visibleCount: DEFAULT_MSG_VISIBLE,
    },
    thread: {
      order: THREAD_ACTIONS.map((a) => a.id),
      visibleCount: DEFAULT_THREAD_VISIBLE,
    },
  };
}

function loadFromStorage(): MsgActionsPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return defaultPrefs();
    const parsed = JSON.parse(raw) as Partial<MsgActionsPrefs>;

    // Validate and repair: keep only ids that exist in the registry; append
    // any new ids that appeared since the prefs were last saved so new actions
    // are discoverable without requiring a manual reset.
    const repairScope = (
      saved: Partial<ScopePrefs> | undefined,
      registry: typeof MESSAGE_ACTIONS,
      defaultVisible: number,
    ): ScopePrefs => {
      const validIds = new Set(registry.map((a) => a.id));
      const savedOrder = (saved?.order ?? []).filter((id) => validIds.has(id));
      // Append any registry ids not yet present in the saved order.
      for (const a of registry) {
        if (!savedOrder.includes(a.id)) savedOrder.push(a.id);
      }
      return {
        order: savedOrder,
        visibleCount: typeof saved?.visibleCount === 'number' ? saved.visibleCount : defaultVisible,
      };
    };

    return {
      message: repairScope(parsed.message, MESSAGE_ACTIONS, DEFAULT_MSG_VISIBLE),
      thread: repairScope(parsed.thread, THREAD_ACTIONS, DEFAULT_THREAD_VISIBLE),
    };
  } catch {
    return defaultPrefs();
  }
}

function saveToStorage(prefs: MsgActionsPrefs): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
    // Storage may be unavailable in some browser contexts. Gracefully ignore.
  }
}

function createPrefsStore() {
  let _prefs = $state<MsgActionsPrefs>(loadFromStorage());

  function getPrefs(): MsgActionsPrefs {
    return _prefs;
  }

  function setOrder(scope: 'message' | 'thread', order: string[]): void {
    _prefs = { ..._prefs, [scope]: { ..._prefs[scope], order } };
    saveToStorage(_prefs);
  }

  function setVisibleCount(scope: 'message' | 'thread', count: number): void {
    _prefs = { ..._prefs, [scope]: { ..._prefs[scope], visibleCount: count } };
    saveToStorage(_prefs);
  }

  function reorder(scope: 'message' | 'thread', fromIndex: number, toIndex: number): void {
    const order = [..._prefs[scope].order];
    const spliced = order.splice(fromIndex, 1);
    const moved = spliced[0];
    if (moved === undefined) return;
    order.splice(toIndex, 0, moved);
    setOrder(scope, order);
  }

  function toggleVisible(scope: 'message' | 'thread', id: string): void {
    const current = _prefs[scope];
    const idx = current.order.indexOf(id);
    if (idx === -1) return;

    const currentVisible = current.visibleCount;
    if (idx < currentVisible) {
      // Currently in primary: move it just past the visible boundary by
      // adjusting visibleCount (if it's the last primary item) or by
      // reordering it to just after the boundary.
      if (currentVisible <= 1) return; // refuse to hide the only primary action
      setVisibleCount(scope, currentVisible - 1);
      // If it's not the last primary item, swap it to the last primary slot
      // so it becomes the first overflow item.
      if (idx < currentVisible - 1) {
        const newOrder = [...current.order];
        // Move item to position currentVisible - 1.
        newOrder.splice(idx, 1);
        newOrder.splice(currentVisible - 1, 0, id);
        _prefs = { ..._prefs, [scope]: { order: newOrder, visibleCount: currentVisible - 1 } };
        saveToStorage(_prefs);
      }
    } else {
      // Currently in overflow: move it to just before the visible boundary.
      const newOrder = [...current.order];
      newOrder.splice(idx, 1);
      newOrder.splice(currentVisible, 0, id);
      _prefs = { ..._prefs, [scope]: { order: newOrder, visibleCount: currentVisible + 1 } };
      saveToStorage(_prefs);
    }
  }

  function resetToDefaults(scope: 'message' | 'thread'): void {
    const defaults = defaultPrefs();
    _prefs = { ..._prefs, [scope]: defaults[scope] };
    saveToStorage(_prefs);
  }

  function resetAll(): void {
    _prefs = defaultPrefs();
    saveToStorage(_prefs);
  }

  return {
    get prefs() { return _prefs; },
    get message() { return _prefs.message; },
    get thread() { return _prefs.thread; },
    getPrefs,
    setOrder,
    setVisibleCount,
    reorder,
    toggleVisible,
    resetToDefaults,
    resetAll,
    // Exposed for tests only.
    _forTest_loadFromStorage: loadFromStorage,
    _forTest_defaultPrefs: defaultPrefs,
  };
}

export const messageActionsPrefs = createPrefsStore();
