/**
 * IndexedDB-backed debug ring buffer shared between the page and the service
 * worker.
 *
 * DB: herold-debug, version 1.
 * Stores:
 *   events  — append-only ring capped at MAX_RECORDS; key is auto-increment.
 *   meta    — key-value store for persistent flags ("enabled" etc.).
 *
 * The service worker carries an inline copy of the same IDB helpers (sw.js
 * cannot import ES modules).  This module is the page-side counterpart: it
 * reads and writes the same DB so the Diagnostics form can display both page
 * and SW events interleaved in insertion order.
 *
 * The "enabled" flag gates only verbose PAGE-level capture.  The SW always
 * writes its own sw.* events to the ring regardless of this flag — those
 * events are rare (one per notification click) and are exactly the trace
 * needed to diagnose notification-click failures (issue #83).
 *
 * REQ-PUSH-70..73, REQ-MOB-74.
 */

const DB_NAME = 'herold-debug';
const STORE_EVENTS = 'events';
const STORE_META = 'meta';

/** Maximum records kept in the ring.  Oldest entry pruned on overflow. */
export const MAX_RECORDS = 1000;

/** Shape of a single ring record. */
export interface DebugRecord {
  /** Auto-increment primary key; reflects insertion order. */
  id?: number;
  /** ISO-8601 timestamp, set at write time. */
  ts: string;
  /** Origin context: 'sw' for service-worker events, 'page' for page events. */
  ctx: 'sw' | 'page';
  /** Severity level string (e.g. 'info', 'warn', 'error'). */
  level: string;
  /** Short event name or message (e.g. 'sw.openApp.focus.ok'). */
  msg: string;
  /** Optional JSON-serialised payload for extra structured data. */
  payload?: string;
}

function openDb(): Promise<IDBDatabase> {
  return new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = (event) => {
      const db = (event.target as IDBOpenDBRequest).result;
      if (!db.objectStoreNames.contains(STORE_EVENTS)) {
        db.createObjectStore(STORE_EVENTS, { autoIncrement: true, keyPath: 'id' });
      }
      if (!db.objectStoreNames.contains(STORE_META)) {
        db.createObjectStore(STORE_META);
      }
    };
    req.onsuccess = (event) => resolve((event.target as IDBOpenDBRequest).result);
    req.onerror = (event) =>
      reject((event.target as IDBOpenDBRequest).error);
  });
}

/**
 * Append a record to the ring, pruning the oldest entry when the cap is
 * reached.  Never throws — IDB errors are swallowed silently.
 */
export async function appendEvent(
  ctx: 'sw' | 'page',
  level: string,
  msg: string,
  payload?: unknown,
): Promise<void> {
  try {
    const db = await openDb();
    const payloadStr =
      payload !== undefined
        ? typeof payload === 'string'
          ? payload
          : JSON.stringify(payload)
        : undefined;
    await new Promise<void>((resolve) => {
      const tx = db.transaction([STORE_EVENTS], 'readwrite');
      tx.oncomplete = () => {
        db.close();
        resolve();
      };
      tx.onerror = () => {
        db.close();
        resolve();
      };
      const store = tx.objectStore(STORE_EVENTS);
      const countReq = store.count();
      countReq.onsuccess = () => {
        const record: Omit<DebugRecord, 'id'> = {
          ts: new Date().toISOString(),
          ctx,
          level,
          msg,
          ...(payloadStr !== undefined ? { payload: payloadStr } : {}),
        };
        if ((countReq.result as number) >= MAX_RECORDS) {
          // Delete the oldest record (lowest auto-increment key) before adding.
          const cursorReq = store.openCursor();
          cursorReq.onsuccess = (e) => {
            const cursor = (
              e.target as IDBRequest<IDBCursorWithValue | null>
            ).result;
            if (cursor) {
              cursor.delete();
              store.add(record);
            }
          };
        } else {
          store.add(record);
        }
      };
    });
  } catch {
    /* never throw */
  }
}

/**
 * Read all records from the ring, ordered by auto-increment id (insertion
 * order, oldest first).  Returns an empty array on any IDB error.
 */
export async function readAll(): Promise<DebugRecord[]> {
  try {
    const db = await openDb();
    return new Promise<DebugRecord[]>((resolve) => {
      const tx = db.transaction([STORE_EVENTS], 'readonly');
      const store = tx.objectStore(STORE_EVENTS);
      const req = store.getAll();
      req.onsuccess = (e) => {
        db.close();
        resolve(((e.target as IDBRequest<DebugRecord[]>).result) ?? []);
      };
      req.onerror = () => {
        db.close();
        resolve([]);
      };
    });
  } catch {
    return [];
  }
}

/**
 * Delete all records from the events store.  Leaves the meta store intact.
 * Never throws.
 */
export async function clear(): Promise<void> {
  try {
    const db = await openDb();
    await new Promise<void>((resolve) => {
      const tx = db.transaction([STORE_EVENTS], 'readwrite');
      tx.oncomplete = () => {
        db.close();
        resolve();
      };
      tx.onerror = () => {
        db.close();
        resolve();
      };
      tx.objectStore(STORE_EVENTS).clear();
    });
  } catch {
    /* never throw */
  }
}

/**
 * Read the debug-logging enabled flag.  Returns false when absent or on any
 * IDB error.
 */
export async function getEnabled(): Promise<boolean> {
  try {
    const db = await openDb();
    return new Promise<boolean>((resolve) => {
      const tx = db.transaction([STORE_META], 'readonly');
      const req = tx.objectStore(STORE_META).get('enabled');
      req.onsuccess = (e) => {
        db.close();
        resolve((e.target as IDBRequest<boolean | undefined>).result === true);
      };
      req.onerror = () => {
        db.close();
        resolve(false);
      };
    });
  } catch {
    return false;
  }
}

/**
 * Persist the debug-logging enabled flag to the meta store.  Never throws.
 */
export async function setEnabled(enabled: boolean): Promise<void> {
  try {
    const db = await openDb();
    await new Promise<void>((resolve) => {
      const tx = db.transaction([STORE_META], 'readwrite');
      tx.oncomplete = () => {
        db.close();
        resolve();
      };
      tx.onerror = () => {
        db.close();
        resolve();
      };
      tx.objectStore(STORE_META).put(enabled, 'enabled');
    });
  } catch {
    /* never throw */
  }
}

/**
 * Post the debug-enabled flag change to the controlling service worker so the
 * SW can update its in-memory cache immediately without waiting for its next
 * IDB read on the next event.
 */
export function notifySW(enabled: boolean): void {
  if (!navigator.serviceWorker?.controller) return;
  navigator.serviceWorker.controller.postMessage({
    type: 'herold-debug',
    enabled,
  });
}

/**
 * Format a single DebugRecord as a plain-text line for copy output.
 *
 * Shape: <ts>  <ctx>  <level>  <msg>  [<payload>]
 *
 * Exported for use in both DiagnosticsForm and tests.
 */
export function formatRecord(r: DebugRecord): string {
  const parts = [r.ts, r.ctx, r.level.padEnd(5), r.msg];
  if (r.payload !== undefined) parts.push(r.payload);
  return parts.join('  ');
}
