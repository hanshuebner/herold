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
 *  2. Normal shell with browser history available (history.length > 1):
 *     window.history.back() returns the user to the thread list they came
 *     from, preserving their scroll position.
 *
 *  3. No history (first page in session, direct link):
 *     fall back to the mail listing for the current folder.
 */

import { router } from '../router/router.svelte';
import { mail } from './store.svelte';

export function navigateBackFromThread(): void {
  if (router.parts[0] === 'thread-window') {
    const threadId = router.parts[1] ?? '';
    router.navigate('/thread-window/' + encodeURIComponent(threadId));
    return;
  }
  if (window.history.length > 1) {
    window.history.back();
    return;
  }
  const folder = mail.listFolder;
  if (folder === 'inbox') {
    router.navigate('/mail');
  } else {
    router.navigate(`/mail/folder/${encodeURIComponent(folder)}`);
  }
}
