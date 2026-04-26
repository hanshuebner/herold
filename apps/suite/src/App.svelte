<script lang="ts">
  import Shell from './lib/shell/Shell.svelte';

  let activeApp = $state<'mail' | 'chat'>('mail');
</script>

<Shell
  {activeApp}
  mailUnread={14}
  chatUnread={3}
  onAppSelect={(app) => (activeApp = app)}
>
  {#snippet sidebar()}
    <div class="sidebar-inner">
      <button type="button" class="compose">
        <span aria-hidden="true">✎</span> Compose
      </button>

      <ul class="mailbox-list">
        <li class="active"><span>Inbox</span><span class="count">14</span></li>
        <li><span>Snoozed</span></li>
        <li><span>Important</span></li>
        <li><span>Sent</span></li>
        <li><span>Drafts</span><span class="count">1</span></li>
        <li><span>All Mail</span></li>
        <li class="more"><span>More</span></li>
      </ul>

      <h3>Labels</h3>
      <ul class="label-list">
        <li><span class="dot" style="--c: #4589ff"></span><span>work</span></li>
        <li><span class="dot" style="--c: #42be65"></span><span>family</span></li>
        <li><span class="dot" style="--c: #f1c21b"></span><span>volunteer</span></li>
      </ul>
    </div>
  {/snippet}

  <div class="content-placeholder">
    <h1>{activeApp === 'mail' ? 'Inbox' : 'Chat'}</h1>
    <p class="lead">
      Suite shell skeleton — rail, global bar, sidebar, content slot, coach strip.
      Real views land in subsequent commits.
    </p>

    <p class="diag">
      Active app: <code>{activeApp}</code>
    </p>
  </div>
</Shell>

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
    align-items: center;
    justify-content: space-between;
    padding: var(--spacing-03) var(--spacing-04);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    cursor: pointer;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .mailbox-list li.active {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 600;
  }
  .mailbox-list li:hover,
  .label-list li:hover {
    background: var(--layer-02);
    color: var(--text-primary);
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
  .label-list li {
    justify-content: flex-start;
  }

  .content-placeholder {
    padding: var(--spacing-07);
    max-width: 60ch;
  }
  .content-placeholder h1 {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    margin: 0 0 var(--spacing-03);
  }
  .lead {
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-06);
  }
  .diag {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-helper);
  }
  code {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    background: var(--layer-02);
    padding: 0 var(--spacing-02);
    border-radius: var(--radius-sm);
    color: var(--text-primary);
  }
</style>
