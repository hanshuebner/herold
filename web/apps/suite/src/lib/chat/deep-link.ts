/**
 * Deep-link handler for the openChat query parameter.
 *
 * When the URL contains ?openChat=<conversationId> the suite opens the
 * conversation as a floating overlay rather than navigating to the
 * fullscreen /chat/* route.  This is the preferred path for push
 * notification click-throughs so the user lands in their mail view with
 * an overlay rather than losing their place.
 *
 * The function is intentionally pure with respect to its callers: it
 * receives everything it needs via arguments so it can be unit-tested
 * without mounting the full App component.
 *
 * Called from App.svelte inside a $effect / untrack block.
 * REQ-PUSH-* (push notification routing), REQ-CHAT-* (overlay open).
 */

export interface DeepLinkDeps {
  /** Current openChat param value (null if absent). */
  param: string | null;
  /** True when the conversations cache has been populated. */
  conversationsReady: boolean;
  /** True when auth is ready and the server reports HeroldChat capability. */
  hasChatCap: boolean;
  /** True when the current route is the fullscreen chat view. */
  onChatRoute: boolean;
  /** Open a conversation in an overlay window. */
  openWindow: (id: string) => void;
  /** Remove the openChat param from the URL. */
  clearParam: () => void;
}

/**
 * Returns true if the deep-link conditions are met and calls openWindow +
 * clearParam.  Returns false (no-op) otherwise.
 *
 * The return value is used only in tests to verify whether the handler fired.
 */
export function handleOpenChatDeepLink(deps: DeepLinkDeps): boolean {
  const { param, conversationsReady, hasChatCap, onChatRoute, openWindow, clearParam } = deps;
  if (!param) return false;
  if (!hasChatCap) return false;
  if (onChatRoute) return false;
  if (!conversationsReady) return false;

  openWindow(param);
  clearParam();
  return true;
}
