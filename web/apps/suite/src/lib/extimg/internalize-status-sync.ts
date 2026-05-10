/**
 * REQ-EXTIMG-BG-INTERNAL-30: subscribe to InternalizeStatus state-change
 * pushes and refresh the JMAP session descriptor on each push so the
 * settings → privacy "Image processing" pending count (REQ-EXTIMG-BG-32)
 * stays current while the background internalize-worker drains the
 * backlog.
 *
 * The server bumps `JMAPStateKindInternalizeStatus` once per non-empty
 * worker batch (REQ-EXTIMG-BG-INTERNAL-21), so the SPA gets one push
 * per batch rather than the per-message Email-state storm the original
 * REQ-EXTIMG-BG-22 design produced. This module is imported for
 * side-effect from App.svelte; the registration lives at module top
 * level so it runs exactly once per app load (mirroring how the chat
 * and mail stores register their sync handlers in their singleton
 * constructors).
 */

import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';

sync.on('InternalizeStatus', () => {
  // Fire-and-forget; refreshSession() handles its own errors and the
  // user keeps the previous descriptor on failure.
  void auth.refreshSession();
});
