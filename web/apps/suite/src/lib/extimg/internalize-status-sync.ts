/**
 * REQ-EXTIMG-BG-INTERNAL-30: subscribe to InternalizeStatus state-change
 * pushes and refresh the JMAP session descriptor when the InternalizeStatus
 * state advances, so the settings → privacy "Image processing" pending
 * count (REQ-EXTIMG-BG-32) stays current while the background
 * internalize-worker drains the backlog.
 *
 * The server bumps `JMAPStateKindInternalizeStatus` once per non-empty
 * worker batch (REQ-EXTIMG-BG-INTERNAL-21). Every StateChange the server
 * pushes carries the current InternalizeStatus state alongside whatever
 * type actually changed (a mail delivery pushes Email and Mailbox), so the
 * handler compares against the last state it saw per account and refreshes
 * only on a difference. The first state seen per account is the one the
 * EventSource sends on connect, right after the bootstrap fetched the
 * descriptor; it becomes the baseline. This module is imported for
 * side-effect from App.svelte; the registration lives at module top
 * level so it runs exactly once per app load (mirroring how the chat
 * and mail stores register their sync handlers in their singleton
 * constructors).
 */

import { auth, registerAccountResetCallback } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';

const lastState = new Map<string, string>();

registerAccountResetCallback(() => {
  lastState.clear();
});

sync.on('InternalizeStatus', (newState, accountId) => {
  const previous = lastState.get(accountId);
  lastState.set(accountId, newState);
  if (previous === undefined || previous === newState) return;
  // Fire-and-forget; refreshSession() handles its own errors and the
  // user keeps the previous descriptor on failure.
  void auth.refreshSession();
});
