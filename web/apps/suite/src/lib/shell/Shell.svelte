<script lang="ts">
  import Rail from './Rail.svelte';
  import GlobalBar from './GlobalBar.svelte';
  import CoachStrip from './CoachStrip.svelte';
  import ToastHost from '../toast/ToastHost.svelte';
  import ComposeWindow from '../compose/ComposeWindow.svelte';
  import MinimizedTray from '../compose/MinimizedTray.svelte';
  import HelpOverlay from '../help/HelpOverlay.svelte';
  import MoveTargetPicker from '../mail/MoveTargetPicker.svelte';
  import LabelPicker from '../mail/LabelPicker.svelte';
  import SnoozePicker from '../mail/SnoozePicker.svelte';
  import ReactionConfirmModal from '../mail/ReactionConfirmModal.svelte';
  import ConfirmDialog from '../dialog/ConfirmDialog.svelte';
  import PromptDialog from '../dialog/PromptDialog.svelte';
  import ChatRail from '../chat/ChatRail.svelte';
  import ChatOverlayHost from '../chat/ChatOverlayHost.svelte';

  interface Props {
    activeApp?: 'mail' | 'chat';
    mailUnread?: number;
    chatUnread?: number;
    /** When true, suppress the chat rail and overlay host (fullscreen chat route). */
    hideChatOverlay?: boolean;
    /** When false, hide both chat rail and overlay (capability gate). */
    chatEnabled?: boolean;
    sidebar?: import('svelte').Snippet;
    children?: import('svelte').Snippet;
    onAppSelect?: (app: 'mail' | 'chat') => void;
  }
  let {
    activeApp = 'mail',
    mailUnread = 0,
    chatUnread = 0,
    hideChatOverlay = false,
    chatEnabled = false,
    sidebar,
    children,
    onAppSelect,
  }: Props = $props();
</script>

<div class="shell">
  <!-- Issue #31: the GlobalBar sits inside the content pane so the
       search field spans only the content area. The space above the
       rail and sidebar is reserved for the brand mark. -->
  <div class="middle">
    <div class="left-stack">
      <a class="brand" href="/" aria-label="Herold home">Herold</a>
      <Rail {activeApp} {mailUnread} {chatUnread} onSelect={onAppSelect} />
    </div>

    {#if sidebar}
      <aside class="sidebar" aria-label="Navigation">
        {@render sidebar()}
      </aside>
    {/if}

    <main class="content">
      <GlobalBar
        placeholder={activeApp === 'chat' ? 'Search chat' : 'Search mail'}
      />
      <div class="content-body">
        {@render children?.()}
      </div>
    </main>

    <!-- Right-edge chat rail: hidden on fullscreen chat route or when
         chat capability is absent.  Also hidden at <768px via CSS. -->
    {#if chatEnabled && !hideChatOverlay}
      <ChatRail />
    {/if}
  </div>

  <CoachStrip />
  <ToastHost />
  <ComposeWindow />
  <MinimizedTray />
  <HelpOverlay />
  <MoveTargetPicker />
  <LabelPicker />
  <SnoozePicker />
  <ReactionConfirmModal />
  <ConfirmDialog />
  <PromptDialog />

  <!-- Floating chat overlay windows: hidden on fullscreen chat route,
       phone breakpoints, or when chat capability is absent. -->
  {#if chatEnabled && !hideChatOverlay}
    <ChatOverlayHost />
  {/if}
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
  /* Brand mark + rail share the leftmost column. The brand sits in the
     empty header strip the GlobalBar used to occupy (issue #31). */
  .left-stack {
    display: flex;
    flex-direction: column;
    border-right: 1px solid var(--border-subtle-01);
    background: var(--background);
  }
  .brand {
    height: var(--spacing-08);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0 var(--spacing-04);
    color: var(--text-primary);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    letter-spacing: 0.02em;
    text-decoration: none;
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
    display: flex;
    flex-direction: column;
    background: var(--background);
  }
  .content-body {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }

  /* Tablet portrait + smaller: collapse the rail's labels and the sidebar
     to off-canvas later. For now, just ensure overflow works on narrow viewports. */
  @media (max-width: 768px) {
    .sidebar {
      display: none;
    }
    .brand {
      padding: 0 var(--spacing-02);
    }
  }
</style>
