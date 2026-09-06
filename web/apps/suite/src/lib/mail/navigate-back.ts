/**
 * navigateBackFromThread: shared post-action navigation helper for thread
 * views (re #83).
 *
 * Three cases, evaluated in priority order:
 *
 *  1. Standalone thread-window popup (route: /thread-window/<threadId>):
 *     remain on the thread by re-navigating to the same thread-window URL.
 *     A popup window opened via window.open() has history.length === 1 and
 *     no inbox shell, so navigating to /mail would show the full inbox UI
 *     inside the popup. We never do that.
 *
 *  2. Normal shell, a prior list route is known (router.lastListPath set):
 *     push that path via router.navigate(). Pushing (rather than calling
 *     window.history.back()) leaves the thread's own history entry alone:
 *     a subsequent browser Back press lands back on the thread's reader
 *     instead of skipping past it (re #294). window.history.back() reuses
 *     the existing list entry in place, which moves the current history
 *     position one slot further back than opening the thread did -- a
 *     following user-initiated Back then goes past the thread entirely.
 *
 *  3. No prior list route recorded (thread opened directly by URL, first
 *     page in the session): fall back to the mail listing for the current
 *     folder.
 */

import { router } from '../router/router.svelte';
import { mail } from './store.svelte';

export function navigateBackFromThread(): void {
  if (router.parts[0] === 'thread-window') {
    const threadId = router.parts[1] ?? '';
    router.navigate('/thread-window/' + encodeURIComponent(threadId));
    return;
  }
  if (router.lastListPath) {
    router.navigate(router.lastListPath);
    return;
  }
  const folder = mail.listFolder;
  if (folder === 'inbox') {
    router.navigate('/mail');
  } else {
    router.navigate(`/mail/folder/${encodeURIComponent(folder)}`);
  }
}
