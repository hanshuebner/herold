<script lang="ts">
  import GlobalBar from './GlobalBar.svelte';
  import CoachStrip from './CoachStrip.svelte';
  import AppSwitcherMenu from './AppSwitcherMenu.svelte';
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
  import NewChatPicker from '../chat/NewChatPicker.svelte';

  interface Props {
    /** When false, hide the overlay host (capability gate). */
    chatEnabled?: boolean;
    sidebar?: import('svelte').Snippet;
    children?: import('svelte').Snippet;
  }
  let {
    chatEnabled = false,
    sidebar,
    children,
  }: Props = $props();
</script>

<div class="shell">
  <div class="middle">
    <aside class="sidebar" aria-label="Navigation">
      <div class="brand-row">
        <AppSwitcherMenu currentApp="mail" />
        <a class="brand" href="/" aria-label="Herold home">Herold</a>
      </div>
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
  <NewChatPicker />

  <!-- Floating chat overlay windows. The host filters out the
       conversation that's already rendered in the dedicated chat
       route to avoid duplicate views; otherwise it renders so that
       a background-arriving message can pop an overlay even while
       the user is on /chat. Phone breakpoints suppress via CSS. -->
  {#if chatEnabled}
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
  .brand-row {
    display: flex;
    align-items: center;
    flex-shrink: 0;
    height: var(--touch-min, 44px);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
    gap: 0;
  }

  .brand {
    flex: 1;
    display: flex;
    align-items: center;
    height: 100%;
    padding: 0 var(--spacing-04) 0 var(--spacing-02);
    color: var(--text-primary);
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
