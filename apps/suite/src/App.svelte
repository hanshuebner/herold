<script lang="ts">
  import Shell from './lib/shell/Shell.svelte';
  import AuthGate from './lib/auth/AuthGate.svelte';
  import { router } from './lib/router/router.svelte';
  import MailView from './views/MailView.svelte';
  import ChatView from './views/ChatView.svelte';
  import SettingsView from './views/SettingsView.svelte';
  import NotFoundView from './views/NotFoundView.svelte';

  let activeApp = $derived<'mail' | 'chat'>(
    router.matches('chat') ? 'chat' : 'mail',
  );

  function selectApp(app: 'mail' | 'chat'): void {
    router.navigate(app === 'chat' ? '/chat' : '/mail');
  }
</script>

<AuthGate>
<Shell {activeApp} mailUnread={14} chatUnread={3} onAppSelect={selectApp}>
  {#snippet sidebar()}
    {#if activeApp === 'mail'}
      <div class="sidebar-inner">
        <button type="button" class="compose">
          <span aria-hidden="true">✎</span> Compose
        </button>

        <ul class="mailbox-list">
          <li class:active={router.matches('mail') && !router.parts[1]}>
            <button type="button" onclick={() => router.navigate('/mail')}>
              <span>Inbox</span><span class="count">14</span>
            </button>
          </li>
          <li><button type="button">Snoozed</button></li>
          <li><button type="button">Important</button></li>
          <li><button type="button">Sent</button></li>
          <li>
            <button type="button">
              <span>Drafts</span><span class="count">1</span>
            </button>
          </li>
          <li><button type="button">All Mail</button></li>
          <li class="more"><button type="button">More</button></li>
        </ul>

        <h3>Labels</h3>
        <ul class="label-list">
          <li class:active={router.matches('mail', 'label', 'work')}>
            <button type="button" onclick={() => router.navigate('/mail/label/work')}>
              <span class="dot" style="--c: #4589ff"></span><span>work</span>
            </button>
          </li>
          <li class:active={router.matches('mail', 'label', 'family')}>
            <button type="button" onclick={() => router.navigate('/mail/label/family')}>
              <span class="dot" style="--c: #42be65"></span><span>family</span>
            </button>
          </li>
          <li>
            <button type="button">
              <span class="dot" style="--c: #f1c21b"></span><span>volunteer</span>
            </button>
          </li>
        </ul>
      </div>
    {:else}
      <div class="sidebar-inner">
        <h3>Conversations</h3>
        <ul class="mailbox-list">
          <li><button type="button">Direct messages</button></li>
          <li><button type="button">Spaces</button></li>
        </ul>
      </div>
    {/if}
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
    gap: var(--spacing-04);
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
  }
  .compose:hover {
    filter: brightness(1.1);
  }

  .mailbox-list,
  .label-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .mailbox-list li,
  .label-list li {
    display: flex;
    color: var(--text-secondary);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .mailbox-list li button,
  .label-list li button {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    padding: var(--spacing-03) var(--spacing-04);
    border-radius: var(--radius-md);
    color: inherit;
    min-height: var(--touch-min);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .mailbox-list li.active,
  .label-list li.active {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 600;
  }
  .mailbox-list li:hover,
  .label-list li:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .label-list li button {
    justify-content: flex-start;
  }
  .mailbox-list .count {
    color: var(--text-helper);
    font-variant-numeric: tabular-nums;
  }
  .mailbox-list .more {
    background: var(--layer-02);
    color: var(--text-helper);
    margin-top: var(--spacing-02);
  }

  h3 {
    font-size: var(--type-heading-compact-01-size);
    line-height: var(--type-heading-compact-01-line);
    font-weight: var(--type-heading-compact-01-weight);
    color: var(--text-helper);
    margin: var(--spacing-04) 0 0;
    padding: 0 var(--spacing-04);
  }

  .label-list .dot {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: var(--radius-pill);
    background: var(--c, var(--text-helper));
    margin-right: var(--spacing-03);
  }
</style>
