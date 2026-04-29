<script lang="ts">
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
  import ChatOverlayHost from '../chat/ChatOverlayHost.svelte';

  interface Props {
    /** When true, suppress the chat overlay host (fullscreen chat route). */
    hideChatOverlay?: boolean;
    /** When false, hide the overlay host (capability gate). */
    chatEnabled?: boolean;
    sidebar?: import('svelte').Snippet;
    children?: import('svelte').Snippet;
  }
  let {
    hideChatOverlay = false,
    chatEnabled = false,
    sidebar,
    children,
  }: Props = $props();
</script>

<div class="shell">
  <div class="middle">
    <aside class="sidebar" aria-label="Navigation">
      <a class="brand" href="/" aria-label="Herold home">Herold</a>
      {#if sidebar}
        {@render sidebar()}
      {/if}
    </aside>

    <main class="content">
      <GlobalBar />
      <div class="content-body">
        {@render children?.()}
      </div>
    </main>
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
    min-height: 0;
  }
  .sidebar {
    flex: 0 0 240px;
    display: flex;
    flex-direction: column;
    border-right: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
    overflow-y: auto;
  }
  .brand {
    height: var(--spacing-08);
    display: flex;
    align-items: center;
    flex-shrink: 0;
    padding: 0 var(--spacing-04);
    color: var(--text-primary);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    letter-spacing: 0.02em;
    text-decoration: none;
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

  @media (max-width: 768px) {
    .sidebar {
      display: none;
    }
  }
</style>
