<!--
  ProfileMenu — profile/avatar icon at the right of the global bar that
  opens a dropdown with Settings and Sign-out items.

  Issue #117: replaces the standalone cog and the redundant Settings
  entry in the sidebar bottom rail. The dropdown is the seed for
  future account-switcher / identity-card UX growth.
-->
<script lang="ts">
  import { auth } from '../auth/auth.svelte';
  import { router } from '../router/router.svelte';
  import { t } from '../i18n/i18n.svelte';
  import ProfileIcon from '../icons/ProfileIcon.svelte';
  import SettingsIcon from '../icons/SettingsIcon.svelte';

  let open = $state(false);
  let buttonEl = $state<HTMLButtonElement | null>(null);
  let menuEl = $state<HTMLUListElement | null>(null);

  function toggle(): void {
    open = !open;
  }

  function close(): void {
    open = false;
  }

  function onMenuKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      close();
      buttonEl?.focus();
    }
  }

  function onDocumentMousedown(event: MouseEvent): void {
    if (!open) return;
    const target = event.target as Node | null;
    if (
      target &&
      !buttonEl?.contains(target) &&
      !menuEl?.contains(target)
    ) {
      close();
    }
  }

  $effect(() => {
    if (open) {
      document.addEventListener('mousedown', onDocumentMousedown);
      return () => {
        document.removeEventListener('mousedown', onDocumentMousedown);
      };
    }
  });

  function openSettings(): void {
    close();
    router.navigate('/settings');
  }

  function signOut(): void {
    close();
    void auth.logout();
  }
</script>

<div class="profile-wrap">
  <button
    bind:this={buttonEl}
    type="button"
    class="profile-btn"
    aria-label={t('shell.profile.menu')}
    aria-expanded={open}
    aria-controls="profile-menu"
    aria-haspopup="menu"
    onclick={toggle}
  >
    <ProfileIcon size={20} />
  </button>

  {#if open}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <ul
      bind:this={menuEl}
      id="profile-menu"
      role="menu"
      class="menu"
      onkeydown={onMenuKeydown}
    >
      <li role="none">
        <button
          role="menuitem"
          type="button"
          class="menu-item"
          onclick={openSettings}
        >
          <span class="menu-icon" aria-hidden="true">
            <SettingsIcon size={16} />
          </span>
          {t('settings.title')}
        </button>
      </li>
      <li role="none">
        <button
          role="menuitem"
          type="button"
          class="menu-item"
          onclick={signOut}
        >
          {t('settings.account.signOut')}
        </button>
      </li>
    </ul>
  {/if}
</div>

<style>
  .profile-wrap {
    position: relative;
    display: flex;
    align-items: center;
    flex-shrink: 0;
  }

  .profile-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: var(--touch-min, 44px);
    height: var(--touch-min, 44px);
    min-width: var(--touch-min, 44px);
    min-height: var(--touch-min, 44px);
    padding: 0;
    background: none;
    border: none;
    border-radius: var(--radius-sm, 4px);
    color: var(--text-secondary);
    cursor: pointer;
    transition:
      background var(--duration-fast-02, 100ms) var(--easing-productive-enter, ease),
      color var(--duration-fast-02, 100ms) var(--easing-productive-enter, ease);
  }

  .profile-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .profile-btn:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .menu {
    position: absolute;
    top: calc(100% + 4px);
    right: 0;
    z-index: 200;
    list-style: none;
    margin: 0;
    padding: var(--spacing-02, 4px) 0;
    min-width: 180px;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md, 6px);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.12);
  }

  .menu-item {
    display: flex;
    align-items: center;
    gap: var(--spacing-03, 8px);
    width: 100%;
    padding: var(--spacing-03, 8px) var(--spacing-04, 12px);
    min-height: var(--touch-min, 44px);
    color: var(--text-primary);
    background: none;
    border: none;
    text-align: left;
    cursor: pointer;
    font-size: var(--type-body-compact-01-size);
    transition:
      background var(--duration-fast-02, 100ms) var(--easing-productive-enter, ease);
    white-space: nowrap;
  }

  .menu-item:hover {
    background: var(--layer-02);
  }

  .menu-item:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .menu-icon {
    display: flex;
    align-items: center;
    flex-shrink: 0;
    color: var(--text-secondary);
  }
</style>
