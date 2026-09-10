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
  import { hasSubAccounts } from '../auth/capabilities';
  import { subAccounts } from '../mail/sub-accounts.svelte';
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

  // ── Sub-account scope switcher (issue #212, REQ-MAIL-SUB-02/09) ─────
  //
  // Renders only when the session advertises the sub-accounts capability
  // (REQ-MAIL-SUB-09); absent it this whole block is skipped and the menu
  // is byte-for-byte the pre-#212 Settings/Sign-out list.
  let showSwitcher = $derived(hasSubAccounts());

  $effect(() => {
    if (open && showSwitcher) void subAccounts.load();
  });

  /** The accountId of the currently-scoped sub-account, or null in "All mail". */
  let currentScopeAccountId = $derived(
    router.parts[0] === 'account' ? (router.parts[1] ?? null) : null,
  );

  function selectScope(accountId: string | null): void {
    close();
    if (accountId) router.navigate(`/account/${encodeURIComponent(accountId)}`);
    else router.navigate('/mail');
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
      {#if showSwitcher}
        <li role="none" class="menu-section-label">{t('shell.profile.scopesLabel')}</li>
        <li role="none">
          <button
            role="menuitemradio"
            type="button"
            class="menu-item scope-item"
            aria-checked={currentScopeAccountId === null}
            class:current={currentScopeAccountId === null}
            onclick={() => selectScope(null)}
            data-testid="profile-scope-all"
          >
            <span class="scope-name">{t('shell.profile.allMail')}</span>
          </button>
        </li>
        {#each subAccounts.list as account (account.accountId)}
          <li role="none">
            <button
              role="menuitemradio"
              type="button"
              class="menu-item scope-item"
              aria-checked={currentScopeAccountId === account.accountId}
              class:current={currentScopeAccountId === account.accountId}
              onclick={() => selectScope(account.accountId)}
              data-testid="profile-scope-{account.accountId}"
            >
              <span class="scope-name">{account.name}</span>
              {#if account.unreadThreads > 0}
                <span
                  class="scope-badge"
                  aria-label={t('shell.profile.scopeUnreadAria', { count: account.unreadThreads })}
                >
                  {account.unreadThreads}
                </span>
              {/if}
            </button>
          </li>
        {/each}
        <li role="none" class="menu-divider"></li>
      {/if}
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

  .menu-section-label {
    padding: var(--spacing-02, 4px) var(--spacing-04, 12px);
    font-size: var(--type-helper-text-01-size, 12px);
    font-weight: 600;
    color: var(--text-helper);
    text-transform: uppercase;
    letter-spacing: 0.02em;
  }

  .scope-item {
    justify-content: space-between;
  }

  .scope-item.current {
    font-weight: 600;
    color: var(--interactive);
  }

  .scope-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .scope-badge {
    flex-shrink: 0;
    min-width: 18px;
    padding: 0 5px;
    border-radius: var(--radius-pill, 999px);
    background: var(--interactive);
    color: var(--text-on-color);
    font-size: var(--type-helper-text-01-size, 11px);
    line-height: 18px;
    text-align: center;
  }

  .menu-divider {
    margin: var(--spacing-02, 4px) 0;
    border-top: 1px solid var(--border-subtle-01);
  }
</style>
