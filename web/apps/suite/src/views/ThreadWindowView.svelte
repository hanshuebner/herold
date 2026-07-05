<!--
  Chrome-less standalone thread window (REQ-PUSH-60).

  Rendered when the router is at /#/thread-window/<threadId>. Opened by
  wireDesktopNotificationClick via window.open(..., 'popup,...') so that
  clicking a desktop notification surfaces the thread in its own popup
  window rather than navigating the existing tab.

  Auth flows in via the shared session cookie. The App.svelte init effects
  (settings.hydrate, sync.start, mail.loadMailboxes, mail.loadIdentities)
  still run because they live in the script block of App.svelte and fire
  regardless of which route is active.
-->
<script lang="ts">
  import { mail } from '../lib/mail/store.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import ThreadReader from '../lib/mail/ThreadReader.svelte';

  interface Props {
    threadId: string;
  }
  let { threadId }: Props = $props();

  // Subject shown in the minimal header. Derives reactively from the mail
  // store: starts as the fallback once auth is ready and updates to the real
  // subject once mail.loadThread (triggered by ThreadReader) completes.
  let subject = $derived(
    mail.threadEmails(threadId)[0]?.subject || t('thread.subject.none'),
  );

  // Keep the browser-tab title in sync with the thread subject.
  $effect(() => {
    document.title = subject ? `${subject} — Herold` : 'Herold';
  });
</script>

<div class="thread-window">
  <div class="thread-window-header" role="banner">
    <span class="thread-window-subject">{subject}</span>
    <button
      type="button"
      class="thread-window-close"
      aria-label={t('common.close')}
      onclick={() => window.close()}
    >
      &#10005;
    </button>
  </div>
  <div class="thread-window-body">
    <ThreadReader {threadId} standalone={true} />
  </div>
</div>

<style>
  .thread-window {
    display: flex;
    flex-direction: column;
    height: 100vh;
    height: 100dvh;
    background: var(--background);
    color: var(--text-primary);
  }

  .thread-window-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-03) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
    flex-shrink: 0;
    min-height: var(--touch-min);
  }

  .thread-window-subject {
    flex: 1;
    font-weight: 600;
    font-size: var(--type-body-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .thread-window-close {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    flex-shrink: 0;
    line-height: 1;
  }

  .thread-window-close:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .thread-window-body {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
</style>
