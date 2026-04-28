<script lang="ts">
  import MailIcon from '../icons/MailIcon.svelte';
  import ChatIcon from '../icons/ChatIcon.svelte';
  import { t } from '../i18n/i18n.svelte';

  interface Props {
    activeApp?: 'mail' | 'chat';
    mailUnread?: number;
    chatUnread?: number;
    onSelect?: (app: 'mail' | 'chat') => void;
  }
  let { activeApp = 'mail', mailUnread = 0, chatUnread = 0, onSelect }: Props = $props();
</script>

<nav class="rail" aria-label="Suite apps">
  <button
    type="button"
    class="rail-item"
    class:active={activeApp === 'mail'}
    aria-current={activeApp === 'mail' ? 'page' : undefined}
    onclick={() => onSelect?.('mail')}
  >
    <span class="icon-wrap">
      <MailIcon size={24} />
      {#if mailUnread > 0}
        <span class="badge" aria-label="{mailUnread} unread">
          {mailUnread > 99 ? '99+' : mailUnread}
        </span>
      {/if}
    </span>
    <span class="label">{t('rail.mail')}</span>
  </button>

  <button
    type="button"
    class="rail-item"
    class:active={activeApp === 'chat'}
    aria-current={activeApp === 'chat' ? 'page' : undefined}
    onclick={() => onSelect?.('chat')}
  >
    <span class="icon-wrap">
      <ChatIcon size={24} />
      {#if chatUnread > 0}
        <span class="badge" aria-label="{chatUnread} unread">
          {chatUnread > 99 ? '99+' : chatUnread}
        </span>
      {/if}
    </span>
    <span class="label">{t('rail.chat')}</span>
  </button>
</nav>

<style>
  .rail {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    padding: var(--spacing-04) var(--spacing-02);
    background: var(--background);
    border-right: 1px solid var(--border-subtle-01);
    width: 72px;
    flex: 0 0 auto;
  }
  .rail-item {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--spacing-01);
    padding: var(--spacing-03) var(--spacing-02);
    border-radius: var(--radius-md);
    min-height: var(--touch-min);
    color: var(--text-secondary);
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .rail-item:hover {
    background: var(--layer-01);
    color: var(--text-primary);
  }
  .rail-item.active {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .icon-wrap {
    position: relative;
    display: inline-flex;
  }
  .badge {
    position: absolute;
    top: -6px;
    right: -10px;
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    border-radius: var(--radius-pill);
    background: var(--support-error);
    color: var(--text-on-color);
    font-size: 10px;
    line-height: 18px;
    font-weight: 600;
    text-align: center;
  }
  .label {
    font-size: 11px;
    line-height: 14px;
    font-weight: 600;
  }
</style>
