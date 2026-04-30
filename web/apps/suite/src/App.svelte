<script lang="ts">
  import { untrack } from 'svelte';
  import Shell from './lib/shell/Shell.svelte';
  import AuthGate from './lib/auth/AuthGate.svelte';
  import { router } from './lib/router/router.svelte';
  import { keyboard } from './lib/keyboard/engine.svelte';
  import { auth } from './lib/auth/auth.svelte';
  import { sync } from './lib/jmap/sync.svelte';
  import { chatWs } from './lib/chat/chat-ws.svelte';
  import { chat } from './lib/chat/store.svelte';
  import { presence as chatPresence } from './lib/chat/presence.svelte';
  import { chatOverlay } from './lib/chat/overlay-store.svelte';
  import { handleOpenChatDeepLink } from './lib/chat/deep-link';
  import { Capability } from './lib/jmap/types';
  import { compose } from './lib/compose/compose.svelte';
  import { composeStack } from './lib/compose/compose-stack.svelte';
  import { help } from './lib/help/help.svelte';
  import { settings, applyTheme } from './lib/settings/settings.svelte';
  import { t } from './lib/i18n/i18n.svelte';
  import { confirm } from './lib/dialog/confirm.svelte';
  import { prompt } from './lib/dialog/prompt.svelte';
  import { mail } from './lib/mail/store.svelte';
  import { pushSubscription } from './lib/push/push-subscription.svelte';
  import MailView from './views/MailView.svelte';
  import ChatView from './views/ChatView.svelte';
  import SettingsView from './views/SettingsView.svelte';
  import NotFoundView from './views/NotFoundView.svelte';
  import SidebarChats from './lib/chat/SidebarChats.svelte';

  // True when the user's session has the chat capability.
  let hasChatCap = $derived(
    auth.status === 'ready' && auth.session
      ? Capability.HeroldChat in (auth.session.capabilities ?? {})
      : false,
  );


  // Open the EventSource subscription once auth is ready. Sync handlers
  // were registered at module init by the mail store, so they're already
  // listening when the connection comes up. Also prime the mailbox list
  // so the sidebar unread badges populate regardless of which route
  // the user lands on.
  //
  // The mailbox-prime branch is wrapped in untrack() so reading
  // mail.mailboxes.size and writing this.mailboxes inside loadMailboxes()
  // do not turn the effect into a self-fueled retry loop. The effect
  // depends only on auth.status; the mailbox prime is a one-shot
  // side-effect that fires once when auth becomes ready. Mailbox state
  // changes from then on come over the EventSource sync handlers.
  $effect(() => {
    if (auth.status === 'ready') {
      settings.hydrate();
      // Start the EventSource for mail types; if chat is available also
      // subscribe to Conversation, Message, Membership changes.
      const hasCap = auth.session
        ? Capability.HeroldChat in (auth.session.capabilities ?? {})
        : false;
      const types = hasCap
        ? ['Email', 'Mailbox', 'Thread', 'Conversation', 'Message', 'Membership', 'SeenAddress']
        : ['Email', 'Mailbox', 'Thread', 'SeenAddress'];
      sync.start(types);

      untrack(() => {
        if (mail.mailboxes.size === 0) {
          mail.loadMailboxes().catch((err) => {
            console.error('initial mailbox load failed', err);
          });
        }
        if (hasCap) {
          chatWs.connect();
          // Wire global window-focus / input listeners that drive the
          // chat presence state machine (REQ-CHAT-180..184). Idempotent;
          // safe to call repeatedly across re-fires of this effect.
          chatPresence.install();
          // Load conversations so the sidebar chats section populates
          // on first boot without needing to visit /chat.
          if (chat.conversationsStatus === 'idle') {
            chat.loadConversations().catch((err) => {
              console.error('initial conversation load failed', err);
            });
          }
        }
      });
    } else if (auth.status === 'unauthenticated') {
      // Clean disconnect on logout / session expiry.
      chatWs.disconnect();
    }
  });

  // Apply theme reactively.
  $effect(() => {
    applyTheme(settings.theme);
  });

  // Mirror the active locale onto <html lang>.
  $effect(() => {
    document.documentElement.lang = settings.locale;
  });

  // Wire the compose stack's auto-minimize hook.
  compose.setBeforeOpenHook(() => composeStack.beforeOpenNew());

  // Sidebar "More" expand/collapse state.
  let moreOpen = $state(false);
  let moreInitialised = $state(false);
  $effect(() => {
    if (!moreInitialised && mail.customMailboxes.length > 0) {
      untrack(() => {
        moreOpen = true;
        moreInitialised = true;
      });
    }
  });

  async function promptCreateMailbox(): Promise<void> {
    const name = await prompt.ask({
      title: 'New mailbox',
      label: 'Mailbox name',
      confirmLabel: 'Create',
    });
    if (!name) return;
    const id = await mail.createMailbox(name);
    if (id) router.navigate(`/mail/folder/${encodeURIComponent(id)}`);
  }
  async function promptRenameMailbox(id: string, current: string): Promise<void> {
    const next = await prompt.ask({
      title: 'Rename mailbox',
      label: 'New name',
      defaultValue: current,
      confirmLabel: 'Rename',
    });
    if (!next || next === current) return;
    await mail.renameMailbox(id, next);
  }
  async function confirmDestroyMailbox(id: string, name: string): Promise<void> {
    const ok = await confirm.ask({
      title: `Delete mailbox "${name}"?`,
      message:
        "Messages it contains will remain in any other mailboxes they're in (otherwise they go to Trash on the server).",
      confirmLabel: 'Delete',
      cancelLabel: 'Cancel',
      kind: 'danger',
    });
    if (!ok) return;
    await mail.destroyMailbox(id);
  }

  // ── Deep-link: ?openChat=<conversationId> ────────────────────────────────
  //
  // Allows push notifications (and any external link) to land on a mail or
  // settings route while immediately opening a specific conversation as a
  // floating overlay.  The parameter is intentionally in the hash query string
  // so the server never sees it and the SPA's static hosting contract is
  // unchanged.
  $effect(() => {
    if (auth.status !== 'ready') return;
    const param = router.getParam('openChat');
    const loaded = chat.conversations.size > 0;
    const onChatRoute = router.matches('chat');

    untrack(() => {
      handleOpenChatDeepLink({
        param,
        conversationsReady: loaded,
        hasChatCap,
        onChatRoute,
        openWindow: (id) => chatOverlay.openWindow(id),
        clearParam: () => router.setParam('openChat', null),
      });
    });
  });

  // ── Web Push: non-modal notification banner (REQ-PUSH-30) ───────────────
  let showPushBanner = $state(false);

  $effect(() => {
    if (auth.status !== 'ready') return;
    if (!pushSubscription.available) return;
    if (pushSubscription.subscribed) return;
    if (pushSubscription.permissionState !== 'default') return;

    const until = parseInt(localStorage.getItem('herold:push:denied_until') ?? '0', 10);
    if (Date.now() < until) return;

    const timer = setTimeout(() => {
      untrack(() => {
        showPushBanner = true;
      });
    }, 60_000);

    return () => clearTimeout(timer);
  });

  function dismissPushBanner(): void {
    showPushBanner = false;
  }

  async function enablePushFromBanner(): Promise<void> {
    showPushBanner = false;
    await pushSubscription.subscribe();
  }

  // ── Web Push: SW update notification (REQ-PUSH-72 / REQ-MOB-75) ──────────

  let showSwUpdateBanner = $state(false);

  $effect(() => {
    if (!('serviceWorker' in navigator)) return;
    navigator.serviceWorker.addEventListener('controllerchange', () => {
      untrack(() => {
        showSwUpdateBanner = true;
      });
    });
  });

  // Suite-global bindings — always active.
  keyboard.registerGlobal({
    key: 'c',
    description: 'Compose',
    action: () => compose.openBlank(),
  });
  keyboard.registerGlobal({
    key: '?',
    description: 'Show keyboard shortcuts',
    action: () => help.toggle(),
  });
  keyboard.registerGlobal({
    key: 'g i',
    description: 'Go to Inbox',
    action: () => router.navigate('/mail'),
  });
  keyboard.registerGlobal({
    key: 'g s',
    description: 'Go to Settings',
    action: () => router.navigate('/settings'),
  });
</script>

<AuthGate>
<!-- Non-modal push-enable banner (REQ-PUSH-30): shown after 60s in-app. -->
{#if showPushBanner}
  <div class="push-banner" role="status" aria-live="polite">
    <span>Get notified about new mail and messages.</span>
    <button type="button" class="push-banner-enable" onclick={() => void enablePushFromBanner()}>
      Enable
    </button>
    <button type="button" class="push-banner-dismiss" aria-label="Dismiss" onclick={dismissPushBanner}>
      &#10005;
    </button>
  </div>
{/if}
<!-- SW update banner (REQ-PUSH-72 / REQ-MOB-75). -->
{#if showSwUpdateBanner}
  <div class="sw-update-banner" role="status" aria-live="polite">
    <span>A new version is available.</span>
    <button type="button" onclick={() => location.reload()}>Reload</button>
    <button type="button" aria-label="Dismiss" onclick={() => (showSwUpdateBanner = false)}>
      &#10005;
    </button>
  </div>
{/if}
<Shell
  chatEnabled={hasChatCap}
>
  {#snippet sidebar()}
    <div class="sidebar-inner">
      <button type="button" class="compose" onclick={() => compose.openBlank()}>
        <span aria-hidden="true">&#x270E;</span> {t('sidebar.compose')}
      </button>

      <ul class="mailbox-list">
        <li class:active={router.matches('mail') && !router.parts[1]}>
          <button type="button" onclick={() => router.navigate('/mail')}>
            <span>{t('sidebar.inbox')}</span>
            {#if (mail.inbox?.unreadEmails ?? 0) > 0}
              <span class="count">{mail.inbox?.unreadEmails ?? 0}</span>
            {/if}
          </button>
        </li>
        <li class:active={router.matches('mail', 'folder', 'snoozed')}>
          <button
            type="button"
            onclick={() => router.navigate('/mail/folder/snoozed')}
          >
            <span>{t('sidebar.snoozed')}</span>
          </button>
        </li>
        <li class:active={router.matches('mail', 'folder', 'important')}>
          <button
            type="button"
            onclick={() => router.navigate('/mail/folder/important')}
          >
            <span>{t('sidebar.important')}</span>
          </button>
        </li>
        <li class:active={router.matches('mail', 'folder', 'sent')}>
          <button type="button" onclick={() => router.navigate('/mail/folder/sent')}>
            <span>{t('sidebar.sent')}</span>
          </button>
        </li>
        <li class:active={router.matches('mail', 'folder', 'drafts')}>
          <button type="button" onclick={() => router.navigate('/mail/folder/drafts')}>
            <span>{t('sidebar.drafts')}</span>
            {#if (mail.drafts?.totalEmails ?? 0) > 0}
              <span class="count">{mail.drafts?.totalEmails ?? 0}</span>
            {/if}
          </button>
        </li>
        <li class:active={router.matches('mail', 'folder', 'trash')}>
          <button type="button" onclick={() => router.navigate('/mail/folder/trash')}>
            <span>{t('sidebar.trash')}</span>
          </button>
        </li>
        <li class:active={router.matches('mail', 'folder', 'all')}>
          <button type="button" onclick={() => router.navigate('/mail/folder/all')}>
            <span>{t('sidebar.allMail')}</span>
          </button>
        </li>
      </ul>

      <button
        type="button"
        class="more-toggle"
        aria-expanded={moreOpen}
        onclick={() => (moreOpen = !moreOpen)}
      >
        <span aria-hidden="true">{moreOpen ? '▾' : '▸'}</span>
        {t('sidebar.more')}
        <span class="count">{mail.customMailboxes.length}</span>
      </button>
      {#if moreOpen}
        <ul class="mailbox-list custom">
          {#each mail.customMailboxes as m (m.id)}
            <li class:active={router.matches('mail', 'folder', m.id)}>
              <button
                type="button"
                class="mailbox-row"
                onclick={() => router.navigate(`/mail/folder/${encodeURIComponent(m.id)}`)}
              >
                <span class="name">{m.name}</span>
                {#if m.unreadEmails > 0}
                  <span class="count">{m.unreadEmails}</span>
                {/if}
              </button>
              <div class="row-actions">
                <button
                  type="button"
                  class="row-action"
                  aria-label="{t('sidebar.rename')} {m.name}"
                  title={t('sidebar.rename')}
                  onclick={(ev) => {
                    ev.stopPropagation();
                    promptRenameMailbox(m.id, m.name);
                  }}
                >
                  &#x270E;
                </button>
                <button
                  type="button"
                  class="row-action danger"
                  aria-label="{t('sidebar.delete')} {m.name}"
                  title={t('sidebar.delete')}
                  onclick={(ev) => {
                    ev.stopPropagation();
                    confirmDestroyMailbox(m.id, m.name);
                  }}
                >
                  &times;
                </button>
              </div>
            </li>
          {:else}
            <li class="empty"><span>{t('sidebar.noCustom')}</span></li>
          {/each}
          <li class="add-row">
            <button type="button" onclick={promptCreateMailbox}>+ {t('sidebar.newMailbox')}</button>
          </li>
        </ul>
      {/if}

      {#if hasChatCap}
        <SidebarChats />
      {/if}
    </div>
  {/snippet}

  {#if router.matches('mail')}
    <MailView />
  {:else if router.matches('chat')}
    <ChatView />
  {:else if router.matches('settings')}
    <SettingsView />
  {:else}
    <NotFoundView />
  {/if}
</Shell>
</AuthGate>

<style>
  .sidebar-inner {
    padding: var(--spacing-04);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    height: 100%;
    box-sizing: border-box;
  }
  .compose {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
    flex-shrink: 0;
  }
  .compose:hover {
    filter: brightness(1.1);
  }

  .mailbox-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .mailbox-list li {
    display: flex;
    color: var(--text-secondary);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .mailbox-list li button {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    color: inherit;
    min-height: var(--touch-min);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .mailbox-list li.active {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 600;
  }
  .mailbox-list li:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .mailbox-list .count {
    color: var(--text-helper);
    font-variant-numeric: tabular-nums;
  }

  .more-toggle {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-helper);
    font-weight: 500;
    font-size: var(--type-body-compact-01-size);
    border-radius: var(--radius-md);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    flex-shrink: 0;
  }
  .more-toggle:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .more-toggle .count {
    margin-left: auto;
    color: var(--text-helper);
    font-variant-numeric: tabular-nums;
  }
  .mailbox-list.custom li {
    position: relative;
  }
  .mailbox-list.custom li.empty {
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    font-style: italic;
  }
  /* The mailbox-row button fills the full row width; its count span is the
     last child so justify-content: space-between places it at the right edge.
     padding-right reserves space so the count is not obscured when the
     absolutely-positioned action group fades in on hover. */
  .mailbox-list.custom .mailbox-row {
    width: 100%;
    justify-content: space-between;
    padding-right: calc(2 * 28px + var(--spacing-01) + var(--spacing-04));
  }
  /* Action buttons are positioned absolutely over the right edge of the row.
     They start invisible and fade in on hover / focus-within so the count
     remains the rightmost visible element at rest. */
  .mailbox-list.custom .row-actions {
    position: absolute;
    right: 0;
    top: 0;
    bottom: 0;
    display: flex;
    align-items: center;
    gap: var(--spacing-01);
    padding-right: var(--spacing-01);
    pointer-events: none;
  }
  .mailbox-list.custom li:hover .row-actions,
  .mailbox-list.custom li:focus-within .row-actions {
    pointer-events: auto;
  }
  .mailbox-list.custom .row-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    padding: var(--spacing-02);
    color: var(--text-helper);
    border-radius: var(--radius-md);
    line-height: 1;
    opacity: 0;
    transition:
      opacity var(--duration-fast-02) var(--easing-productive-enter),
      background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .mailbox-list.custom li:hover .row-action,
  .mailbox-list.custom li:focus-within .row-action {
    opacity: 1;
  }
  .mailbox-list.custom .row-action:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .mailbox-list.custom .row-action.danger:hover {
    background: var(--support-error);
    color: var(--text-on-color);
  }
  .mailbox-list.custom .add-row button {
    color: var(--interactive);
    font-weight: 500;
  }

  /* Push enable / SW update banners: fixed bottom-center strip. */
  .push-banner,
  .sw-update-banner {
    position: fixed;
    bottom: var(--spacing-05);
    left: 50%;
    transform: translateX(-50%);
    z-index: 500;
    background: var(--layer-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-05);
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.24);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    max-width: 480px;
    width: calc(100vw - var(--spacing-06) * 2);
  }

  .push-banner span,
  .sw-update-banner span {
    flex: 1;
  }

  .push-banner-enable {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    white-space: nowrap;
  }
  .push-banner-enable:hover {
    filter: brightness(1.1);
  }

  .push-banner-dismiss,
  .sw-update-banner button:last-child {
    width: 28px;
    height: 28px;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .push-banner-dismiss:hover,
  .sw-update-banner button:last-child:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .sw-update-banner button:not(:last-child) {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .sw-update-banner button:not(:last-child):hover {
    filter: brightness(1.1);
  }
</style>
