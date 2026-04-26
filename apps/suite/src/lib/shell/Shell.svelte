<script lang="ts">
  import Rail from './Rail.svelte';
  import GlobalBar from './GlobalBar.svelte';
  import CoachStrip from './CoachStrip.svelte';
  import ToastHost from '../toast/ToastHost.svelte';
  import ComposeWindow from '../compose/ComposeWindow.svelte';

  interface Props {
    activeApp?: 'mail' | 'chat';
    mailUnread?: number;
    chatUnread?: number;
    sidebar?: import('svelte').Snippet;
    children?: import('svelte').Snippet;
    onAppSelect?: (app: 'mail' | 'chat') => void;
  }
  let {
    activeApp = 'mail',
    mailUnread = 0,
    chatUnread = 0,
    sidebar,
    children,
    onAppSelect,
  }: Props = $props();
</script>

<div class="shell">
  <GlobalBar placeholder={activeApp === 'chat' ? 'Search chat' : 'Search mail'} />

  <div class="middle">
    <Rail {activeApp} {mailUnread} {chatUnread} onSelect={onAppSelect} />

    {#if sidebar}
      <aside class="sidebar" aria-label="Navigation">
        {@render sidebar()}
      </aside>
    {/if}

    <main class="content">
      {@render children?.()}
    </main>
  </div>

  <CoachStrip />
  <ToastHost />
  <ComposeWindow />
</div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100vh;
    height: 100dvh;
  }
  .middle {
    flex: 1;
    display: flex;
    min-height: 0; /* enable child overflow */
  }
  .sidebar {
    flex: 0 0 240px;
    border-right: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
    overflow-y: auto;
  }
  .content {
    flex: 1;
    min-width: 0;
    overflow: auto;
    background: var(--background);
  }

  /* Tablet portrait + smaller: collapse the rail's labels and the sidebar
     to off-canvas later. For now, just ensure overflow works on narrow viewports. */
  @media (max-width: 768px) {
    .sidebar {
      display: none;
    }
  }
</style>
