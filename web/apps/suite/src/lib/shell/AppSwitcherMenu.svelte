<!--
  AppSwitcherMenu — burger-menu button + dropdown that lists the other
  suite components the user has access to.

  REQ-UI-13k: burger icon at the left of the brand-mark row.
  REQ-UI-13l: fixed-order entries, each gated on its capability/scope.
  REQ-UI-13m: admin entry gated on the 'admin' scope.
  REQ-UI-13n: closes on entry click, Escape, or click-outside.
-->
<script lang="ts">
  import { auth } from '../auth/auth.svelte';
  import { Capability } from '../jmap/types';
  import { t } from '../i18n/i18n.svelte';
  import MailIcon from '../icons/MailIcon.svelte';
  import CalendarIcon from '../icons/CalendarIcon.svelte';
  import ContactsIcon from '../icons/ContactsIcon.svelte';
  import ChatIcon from '../icons/ChatIcon.svelte';
  import AdminIcon from '../icons/AdminIcon.svelte';

  type AppId = 'mail' | 'calendar' | 'contacts' | 'chat' | 'admin';

  interface Props {
    /** The app currently active -- omitted from the menu. */
    currentApp: AppId;
  }

  let { currentApp }: Props = $props();

  let open = $state(false);
  let buttonEl = $state<HTMLButtonElement | null>(null);
  let menuEl = $state<HTMLUListElement | null>(null);

  // Capability and scope gates derived from auth state.
  let capabilities = $derived(
    auth.status === 'ready' && auth.session
      ? auth.session.capabilities
      : ({} as Record<string, unknown>),
  );
  let scopes = $derived(auth.scopes);

  // Per REQ-UI-13l: calendar and contacts are cut for v1 (capability not
  // advertised yet), so we gate on the presence of the JMAP capability URIs
  // in the session. Chat is gated on HeroldChat. Admin on 'admin' scope.
  let hasChatCap = $derived(Capability.HeroldChat in capabilities);
  // Calendar / Contacts capabilities: gate on RFC 8620 standard URIs.
  // Currently not advertised for v1; the entries will be hidden in practice.
  let hasCalendarCap = $derived(Capability.Calendars in capabilities);
  let hasContactsCap = $derived(Capability.Contacts in capabilities);
  let hasAdminScope = $derived(scopes.includes('admin'));

  interface AppEntry {
    id: AppId;
    href: string;
    labelKey: string;
  }

  // Fixed order per REQ-UI-13l.
  const allEntries: AppEntry[] = [
    { id: 'mail', href: '/#/mail', labelKey: 'app.mail' },
    { id: 'calendar', href: '/#/calendar', labelKey: 'app.calendar' },
    { id: 'contacts', href: '/#/contacts', labelKey: 'app.contacts' },
    { id: 'chat', href: '/#/chat', labelKey: 'app.chat' },
    { id: 'admin', href: '/admin/', labelKey: 'app.admin' },
  ];

  function isVisible(entry: AppEntry): boolean {
    if (entry.id === currentApp) return false;
    switch (entry.id) {
      case 'mail':
        return true;
      case 'calendar':
        return hasCalendarCap;
      case 'contacts':
        return hasContactsCap;
      case 'chat':
        return hasChatCap;
      case 'admin':
        return hasAdminScope;
      default:
        return false;
    }
  }

  let visibleEntries = $derived(allEntries.filter(isVisible));

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
</script>

<div class="switcher">
  <button
    bind:this={buttonEl}
    type="button"
    class="burger"
    aria-label={t('app.switch')}
    aria-expanded={open}
    aria-controls="app-switcher-menu"
    aria-haspopup="menu"
    onclick={toggle}
  >
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      stroke-width="1.8"
      stroke-linecap="round"
      aria-hidden="true"
      focusable="false"
    >
      <line x1="3" y1="6" x2="21" y2="6" />
      <line x1="3" y1="12" x2="21" y2="12" />
      <line x1="3" y1="18" x2="21" y2="18" />
    </svg>
  </button>

  {#if open}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <ul
      bind:this={menuEl}
      id="app-switcher-menu"
      role="menu"
      class="menu"
      onkeydown={onMenuKeydown}
    >
      {#each visibleEntries as entry (entry.id)}
        <li role="none">
          <a
            role="menuitem"
            href={entry.href}
            target="_blank"
            rel="noopener"
            class="menu-item"
            onclick={close}
          >
            <span class="menu-icon" aria-hidden="true">
              {#if entry.id === 'mail'}
                <MailIcon size={16} />
              {:else if entry.id === 'calendar'}
                <CalendarIcon size={16} />
              {:else if entry.id === 'contacts'}
                <ContactsIcon size={16} />
              {:else if entry.id === 'chat'}
                <ChatIcon size={16} />
              {:else if entry.id === 'admin'}
                <AdminIcon size={16} />
              {/if}
            </span>
            {t(entry.labelKey)}
          </a>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .switcher {
    position: relative;
    display: flex;
    align-items: center;
    flex-shrink: 0;
  }

  .burger {
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

  .burger:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .burger:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .menu {
    position: absolute;
    top: calc(100% + 4px);
    left: 0;
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
    padding: var(--spacing-03, 8px) var(--spacing-04, 12px);
    min-height: var(--touch-min, 44px);
    color: var(--text-primary);
    text-decoration: none;
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
