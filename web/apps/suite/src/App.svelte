<script lang="ts">
  import { untrack } from 'svelte';
  import Shell from './lib/shell/Shell.svelte';
  import AuthGate from './lib/auth/AuthGate.svelte';
  import { router } from './lib/router/router.svelte';
  import { keyboard } from './lib/keyboard/engine.svelte';
  import { auth } from './lib/auth/auth.svelte';
  import { sync } from './lib/jmap/sync.svelte';
  import { compose } from './lib/compose/compose.svelte';
  import { composeStack } from './lib/compose/compose-stack.svelte';
  import { help } from './lib/help/help.svelte';
  import { settings, applyTheme } from './lib/settings/settings.svelte';
  import { mail } from './lib/mail/store.svelte';
  import MailView from './views/MailView.svelte';
  import ChatView from './views/ChatView.svelte';
  import SettingsView from './views/SettingsView.svelte';
  import NotFoundView from './views/NotFoundView.svelte';

  let activeApp = $derived<'mail' | 'chat'>(
    router.matches('chat') ? 'chat' : 'mail',
  );

  // Open the EventSource subscription once auth is ready. Sync handlers
  // were registered at module init by the mail store, so they're already
  // listening when the connection comes up. Also prime the mailbox list
  // so the rail/sidebar unread badges populate regardless of which route
  // the user lands on (otherwise they stay empty until /mail is visited).
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
      sync.start(['Email', 'Mailbox', 'Thread']);
      untrack(() => {
        if (mail.mailboxes.size === 0) {
          mail.loadMailboxes().catch((err) => {
            console.error('initial mailbox load failed', err);
          });
        }
      });
    }
  });

  // Apply theme reactively. settings.theme is read inside the effect, so
  // the user toggling theme in the panel re-runs this and updates <html>.
  $effect(() => {
    applyTheme(settings.theme);
  });

  // Wire the compose stack's auto-minimize hook so opening a fresh
  // compose while one is already active snapshots the current one
  // into the tray instead of overwriting it.
  compose.setBeforeOpenHook(() => composeStack.beforeOpenNew());

  function selectApp(app: 'mail' | 'chat'): void {
    router.navigate(app === 'chat' ? '/chat' : '/mail');
  }

  // Sidebar "More" expand/collapse state. The first time the user lands
  // on the app with custom mailboxes already in their account we expand
  // the section automatically; from then on we honour their explicit
  // collapse / expand choice.
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
    const name = prompt('New mailbox name')?.trim();
    if (!name) return;
    const id = await mail.createMailbox(name);
    if (id) router.navigate(`/mail/folder/${encodeURIComponent(id)}`);
  }
  async function promptRenameMailbox(id: string, current: string): Promise<void> {
    const next = prompt('Rename mailbox', current)?.trim();
    if (!next || next === current) return;
    await mail.renameMailbox(id, next);
  }
  async function confirmDestroyMailbox(id: string, name: string): Promise<void> {
    const ok = confirm(
      `Delete mailbox "${name}"? Messages it contains will remain in any other mailboxes they're in (otherwise they go to Trash on the server).`,
    );
    if (!ok) return;
    await mail.destroyMailbox(id);
  }

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
<Shell {activeApp} mailUnread={mail.inbox?.unreadEmails ?? 0} onAppSelect={selectApp}>
  {#snippet sidebar()}
    {#if activeApp === 'mail'}
      <div class="sidebar-inner">
        <button type="button" class="compose" onclick={() => compose.openBlank()}>
          <span aria-hidden="true">✎</span> Compose
        </button>

        <ul class="mailbox-list">
          <li class:active={router.matches('mail') && !router.parts[1]}>
            <button type="button" onclick={() => router.navigate('/mail')}>
              <span>Inbox</span>
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
              <span>Snoozed</span>
            </button>
          </li>
          <li class:active={router.matches('mail', 'folder', 'important')}>
            <button
              type="button"
              onclick={() => router.navigate('/mail/folder/important')}
            >
              <span>Important</span>
            </button>
          </li>
          <li class:active={router.matches('mail', 'folder', 'sent')}>
            <button type="button" onclick={() => router.navigate('/mail/folder/sent')}>
              <span>Sent</span>
            </button>
          </li>
          <li class:active={router.matches('mail', 'folder', 'drafts')}>
            <button type="button" onclick={() => router.navigate('/mail/folder/drafts')}>
              <span>Drafts</span>
              {#if (mail.drafts?.totalEmails ?? 0) > 0}
                <span class="count">{mail.drafts?.totalEmails ?? 0}</span>
              {/if}
            </button>
          </li>
          <li class:active={router.matches('mail', 'folder', 'trash')}>
            <button type="button" onclick={() => router.navigate('/mail/folder/trash')}>
              <span>Trash</span>
            </button>
          </li>
          <li class:active={router.matches('mail', 'folder', 'all')}>
            <button type="button" onclick={() => router.navigate('/mail/folder/all')}>
              <span>All Mail</span>
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
          More
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
                <button
                  type="button"
                  class="row-action"
                  aria-label="Rename {m.name}"
                  title="Rename"
                  onclick={(ev) => {
                    ev.stopPropagation();
                    promptRenameMailbox(m.id, m.name);
                  }}
                >
                  ✎
                </button>
                <button
                  type="button"
                  class="row-action danger"
                  aria-label="Delete {m.name}"
                  title="Delete"
                  onclick={(ev) => {
                    ev.stopPropagation();
                    confirmDestroyMailbox(m.id, m.name);
                  }}
                >
                  ×
                </button>
              </li>
            {:else}
              <li class="empty"><span>No custom mailboxes.</span></li>
            {/each}
            <li class="add-row">
              <button type="button" onclick={promptCreateMailbox}>+ New mailbox</button>
            </li>
          </ul>
        {/if}

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

  h3 {
    font-size: var(--type-heading-compact-01-size);
    line-height: var(--type-heading-compact-01-line);
    font-weight: var(--type-heading-compact-01-weight);
    color: var(--text-helper);
    margin: var(--spacing-04) 0 0;
    padding: 0 var(--spacing-04);
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
    margin-top: var(--spacing-02);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
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
    display: grid;
    grid-template-columns: 1fr auto auto;
    gap: var(--spacing-01);
    align-items: stretch;
  }
  .mailbox-list.custom li.add-row {
    grid-template-columns: 1fr;
  }
  .mailbox-list.custom li.empty {
    grid-template-columns: 1fr;
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    font-style: italic;
  }
  .mailbox-list.custom .mailbox-row {
    justify-content: space-between;
  }
  .mailbox-list.custom .row-action {
    width: 28px;
    padding: var(--spacing-02);
    color: var(--text-helper);
    border-radius: var(--radius-md);
    text-align: center;
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

  .label-list .dot {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: var(--radius-pill);
    background: var(--c, var(--text-helper));
    margin-right: var(--spacing-03);
  }
</style>
