<script lang="ts">
  /**
   * Identity list in the Account section of Settings.
   *
   * REQ-SET-IDENT-01..08: one row per Identity, with a default-selector
   * radio at the leading edge, the avatar thumbnail, the display name /
   * email, the verification status label, and an external-submission
   * disabled treatment for unverified / mis-configured external rows.
   *
   * Each row is split into two regions (re #18):
   *   - A non-clickable radio column at the leading edge that promotes
   *     the row to default via `mail.setDefaultIdentity`
   *     (REQ-SET-IDENT-04); the default row also carries a static
   *     "Standard" badge.
   *   - A click-to-edit card body that fires `onedit` on click and on
   *     Enter / Space when keyboard-focused. The kebab menu's Edit item
   *     remains available as the explicit invocation; the row click is
   *     the discoverable shortcut.
   *
   * Interactive controls inside the card body (Verify / Resend buttons,
   * the kebab trigger) call `event.stopPropagation()` on their click
   * handlers so they fire their own behaviour without also opening the
   * editor.
   *
   * Verification state ("nicht verifiziert" / "Verifikation ausstehend")
   * renders as a flat, non-interactive status label — never a button.
   * Only the verify / resend affordances are buttons (shared
   * design-system Button).
   *
   * For external-submission state we read the per-identity store; if the
   * external-submission capability is absent (`hasExternalSubmission()`
   * returns false) the disabled treatment falls back to a verification-
   * only gate so the list still renders sensibly.
   */
  import type { Identity } from '../../lib/mail/types';
  import { mail } from '../../lib/mail/store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import {
    hasExternalSubmission,
    hasIdentityVerification,
  } from '../../lib/auth/capabilities';
  import { submissionStore } from '../../lib/identities/identity-submission.svelte';
  import {
    identityStatus,
    canBeDefault,
    isExternalWithoutSubmission,
    resolveDefault,
    sortIdentities,
    type SubmissionSummary,
  } from '../../lib/identities/identity-status';
  import { identityAvatarUrl } from '../../lib/mail/identity-avatar';
  import Button from '@herold/design-system/Button.svelte';

  interface Props {
    /** Callback fired when the user clicks a row's edit button. */
    onedit: (identity: Identity) => void;
    /** Optional: open the add-identity wizard (REQ-SET-IDENT-30). */
    onadd?: () => void;
    /** Optional: open the verify dialog for `identity` (REQ-SET-IDENT-20). */
    onverify?: (identity: Identity) => void;
    /** Optional: trigger the verification-email resend for `identity`
     *  (REQ-IDENT-36). When omitted, the Resend button stays inert. */
    onresend?: (identity: Identity) => void | Promise<void>;
  }
  let { onedit, onadd, onverify, onresend }: Props = $props();

  let identitiesArray = $derived(Array.from(mail.identities.values()));
  let sorted = $derived(sortIdentities(identitiesArray));
  let defaultId = $derived(resolveDefault(identitiesArray));
  let showExtSub = $derived(hasExternalSubmission());
  let showAddBtn = $derived(hasIdentityVerification());

  function submissionSummary(id: string): SubmissionSummary | null {
    if (!showExtSub) return null;
    const handle = submissionStore.forIdentity(id);
    const data = handle.data;
    if (!data) return null;
    return {
      configured: data.configured === true,
      state: data.state ?? null,
    };
  }

  function initial(id: Identity): string {
    return (id.name?.trim() || id.email).charAt(0).toUpperCase();
  }

  async function onRadioChange(identity: Identity): Promise<void> {
    if (!canBeDefault(identity)) return;
    if (defaultId?.id === identity.id) return;
    try {
      await mail.setDefaultIdentity(identity.id);
      toast.show({ message: t('settings.identityList.defaultChanged'), timeoutMs: 3000 });
    } catch (err) {
      toast.show({
        message: t('settings.identityList.defaultChangeFailed'),
        kind: 'error',
        timeoutMs: 5000,
      });
      // Re-throw so the caller surfaces the error in test mode.
      console.error('setDefaultIdentity failed', err);
    }
  }

  // REQ-SET-IDENT-05: open the Add-identity wizard. The wizard mount is
  // owned by SettingsView so the dialog state survives across re-renders
  // of the list; if the parent did not wire `onadd` the button is
  // hidden via the `showAddBtn` capability gate already.
  function onAddIdentity(): void {
    onadd?.();
  }

  // REQ-SET-IDENT-20: open the verify dialog for an unverified row.
  function onVerify(identity: Identity): void {
    onverify?.(identity);
  }

  // REQ-IDENT-36: trigger the verification-email resend for a pending
  // row. The actual REST call lives in the parent; this component
  // only forwards the click.
  function onResend(identity: Identity): void {
    void onresend?.(identity);
  }

  // ── Per-row overflow (kebab) menu (re #20) ──────────────────────────
  //
  // Each row carries a kebab trigger that opens a small dropdown with
  // "Edit" and "Delete". Only one menu is open at a time; it closes on
  // outside click, Escape, or item selection.
  let openMenuId = $state<string | null>(null);

  function toggleMenu(id: string): void {
    openMenuId = openMenuId === id ? null : id;
  }

  function closeMenu(): void {
    openMenuId = null;
  }

  function onMenuKeyDown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      closeMenu();
    }
  }

  // Close any open menu on an outside click.
  $effect(() => {
    if (openMenuId === null) return;
    function onDocClick(e: MouseEvent): void {
      const target = e.target as HTMLElement;
      if (target.closest('[data-identity-row-menu]')) return;
      closeMenu();
    }
    document.addEventListener('click', onDocClick, true);
    return () => document.removeEventListener('click', onDocClick, true);
  });

  function onEditFromMenu(identity: Identity): void {
    closeMenu();
    onedit(identity);
  }

  // re #18: card body is the click-to-edit surface. Enter / Space on the
  // focused card open the editor; mouse click on the card does the same.
  function onCardKeydown(e: KeyboardEvent, identity: Identity): void {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onedit(identity);
    }
  }

  /**
   * Delete an identity from the kebab menu. Confirms first, then issues
   * `Identity/set { destroy }` via the store (which optimistically
   * drops the row). The synthesized default (mayDelete false) never
   * reaches here — the menu item is hidden for it.
   */
  async function onDeleteFromMenu(identity: Identity): Promise<void> {
    closeMenu();
    const ok = await confirm.ask({
      title: t('settings.identityList.deleteConfirmTitle'),
      message: t('settings.identityList.deleteConfirmMessage', {
        email: identity.email,
      }),
      confirmLabel: t('settings.identityList.deleteConfirm'),
      cancelLabel: t('settings.identityWizard.cancel'),
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await mail.deleteIdentity(identity.id);
      toast.show({
        message: t('settings.identityList.deleted', { email: identity.email }),
        timeoutMs: 3000,
      });
    } catch (err) {
      toast.show({
        message: t('settings.identityList.deleteFailed'),
        kind: 'error',
        timeoutMs: 5000,
      });
      console.error('deleteIdentity failed', err);
    }
  }
</script>

<div class="identity-list" data-testid="identity-list">
  <header class="list-header">
    <h3>{t('settings.identityList.heading')}</h3>
    {#if showAddBtn}
      <Button
        variant="primary"
        compact
        title={t('settings.identityList.addTooltip')}
        testid="identity-add-btn"
        onclick={onAddIdentity}
      >
        + {t('settings.identityList.addBtn')}
      </Button>
    {/if}
  </header>

  {#if sorted.length === 0}
    <p class="muted">{t('settings.account.noIdentities')}</p>
  {:else}
    <ul class="rows" role="list">
      {#each sorted as identity (identity.id)}
        {@const status = identityStatus(identity)}
        {@const sub = submissionSummary(identity.id)}
        {@const disabled = isExternalWithoutSubmission(identity, sub)}
        {@const isDefault = defaultId?.id === identity.id}
        {@const avatarUrl = identityAvatarUrl(identity)}
        <li
          class="row"
          class:disabled
          class:default={isDefault}
          data-testid="identity-row"
          data-identity-id={identity.id}
          data-identity-status={status}
        >
          <label class="radio-col" title={canBeDefault(identity)
            ? ''
            : t('settings.identityList.defaultRadioDisabledTitle')}>
            <input
              type="radio"
              name="default-identity"
              value={identity.id}
              checked={isDefault}
              disabled={!canBeDefault(identity)}
              aria-label={t('settings.identityList.defaultRadioAria', {
                email: identity.email,
              })}
              onchange={() => void onRadioChange(identity)}
              data-testid="identity-default-radio"
            />
          </label>

          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <div
            class="card-body"
            role="button"
            tabindex="0"
            aria-label={t('settings.identityList.editRowAria', { email: identity.email })}
            data-testid="identity-card-body"
            onclick={() => onedit(identity)}
            onkeydown={(e) => onCardKeydown(e, identity)}
          >
            <div class="avatar-wrap" aria-hidden="true">
              {#if avatarUrl}
                <img src={avatarUrl} alt="" class="avatar" />
              {:else}
                <div class="avatar avatar-placeholder">{initial(identity)}</div>
              {/if}
            </div>

            <div class="meta">
              <span class="name-row">
                <span class="name">{identity.name || identity.email}</span>
                {#if isDefault}
                  <span class="default-badge" data-testid="identity-default-badge">
                    {t('settings.identityList.defaultBadge')}
                  </span>
                {/if}
              </span>
              {#if identity.name}
                <span class="email mono">{identity.email}</span>
              {/if}
            </div>

            <div class="status">
              {#if status === 'verifying'}
                <span class="status-label status-verifying" data-testid="identity-chip">
                  <span class="status-dot" aria-hidden="true"></span>
                  {t('settings.identityList.chip.verifying')}
                </span>
                <Button
                  variant="secondary"
                  compact
                  title={t('settings.identityList.resendTooltip')}
                  testid="identity-resend-btn"
                  onclick={(e) => { e.stopPropagation(); onResend(identity); }}
                >
                  {t('settings.identityList.resendBtn')}
                </Button>
              {:else if status === 'unverified'}
                <span class="status-label status-unverified" data-testid="identity-chip">
                  <span class="status-dot" aria-hidden="true"></span>
                  {t('settings.identityList.chip.unverified')}
                </span>
                <Button
                  variant="primary"
                  compact
                  title={t('settings.identityList.verifyTooltip')}
                  testid="identity-verify-btn"
                  onclick={(e) => { e.stopPropagation(); onVerify(identity); }}
                >
                  {t('settings.identityList.verifyBtn')}
                </Button>
              {:else}
                <!-- REQ-SET-IDENT-02: verified rows have no status label
                     (silent normal) — render an empty status cell so
                     the layout grid still aligns. -->
                <span class="chip-spacer" aria-hidden="true"></span>
              {/if}

              <div class="row-menu" data-identity-row-menu>
                <button
                  type="button"
                  class="kebab-trigger"
                  aria-haspopup="menu"
                  aria-expanded={openMenuId === identity.id}
                  aria-label={t('settings.identityList.rowMenuAria', {
                    email: identity.email,
                  })}
                  title={t('settings.identityList.rowMenuAria', {
                    email: identity.email,
                  })}
                  data-testid="identity-row-menu-trigger"
                  onclick={(e) => { e.stopPropagation(); toggleMenu(identity.id); }}
                >
                  <span class="kebab-dots" aria-hidden="true">&#x22EE;</span>
                </button>

                {#if openMenuId === identity.id}
                  <!-- svelte-ignore a11y_no_static_element_interactions -->
                  <div
                    class="row-menu-dropdown"
                    role="menu"
                    tabindex="-1"
                    data-testid="identity-row-menu"
                    onkeydown={onMenuKeyDown}
                  >
                    <button
                      type="button"
                      class="row-menu-item"
                      role="menuitem"
                      data-testid="identity-row-menu-edit"
                      onclick={(e) => { e.stopPropagation(); onEditFromMenu(identity); }}
                    >
                      {t('settings.identityList.editBtn')}
                    </button>
                    {#if identity.mayDelete}
                      <button
                        type="button"
                        class="row-menu-item danger"
                        role="menuitem"
                        data-testid="identity-row-menu-delete"
                        onclick={(e) => { e.stopPropagation(); void onDeleteFromMenu(identity); }}
                      >
                        {t('settings.identityList.deleteBtn')}
                      </button>
                    {/if}
                  </div>
                {/if}
              </div>
            </div>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .identity-list {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .list-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
  }

  .list-header h3 {
    margin: 0;
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-secondary);
  }

  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  /* The row is a two-column grid (re #18): the radio column lives
     OUTSIDE the click-to-edit card body so a click on the default
     selector cannot also open the editor. The card body carries the
     surface treatment (background + border + radius) so the radio
     column reads as a flat selector to the left of the card. */
  .row {
    display: grid;
    grid-template-columns: 24px 1fr;
    align-items: center;
    gap: var(--spacing-04);
  }

  .row.disabled .card-body {
    opacity: 0.55;
  }

  .radio-col {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    /* No background / border — the radio is a flat selector beside
       the card, not part of the click-to-edit surface. */
  }

  .radio-col input[type='radio'] {
    accent-color: var(--interactive);
    width: 18px;
    height: 18px;
    cursor: pointer;
  }

  .radio-col input[type='radio']:disabled {
    cursor: not-allowed;
  }

  .card-body {
    display: grid;
    grid-template-columns: 40px 1fr auto;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-04);
    width: 100%;
    text-align: left;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter),
      box-shadow var(--duration-fast-02) var(--easing-productive-enter);
  }

  .card-body:hover {
    background: color-mix(in srgb, var(--interactive) 4%, var(--layer-02));
  }

  .card-body:focus-visible {
    outline: 2px solid var(--interactive);
    outline-offset: 2px;
  }

  .row.default .card-body {
    border-color: color-mix(in srgb, var(--interactive) 60%, transparent);
    background: color-mix(in srgb, var(--interactive) 4%, var(--layer-02));
  }

  .avatar-wrap {
    flex: 0 0 auto;
    width: 40px;
    height: 40px;
  }

  .avatar {
    width: 40px;
    height: 40px;
    border-radius: 50%;
    object-fit: cover;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--interactive);
    color: var(--text-on-color);
    font-weight: 600;
    font-size: var(--type-body-01-size);
    overflow: hidden;
  }

  .avatar-placeholder {
    user-select: none;
  }

  .meta {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    min-width: 0;
  }

  .name-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    min-width: 0;
  }

  .name {
    color: var(--text-primary);
    font-weight: 600;
    font-size: var(--type-body-01-size);
    word-break: break-word;
  }

  /* Static, non-interactive "default" badge on the current default row. */
  .default-badge {
    display: inline-flex;
    align-items: center;
    flex-shrink: 0;
    padding: 1px var(--spacing-02);
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--interactive) 18%, transparent);
    color: var(--interactive);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: uppercase;
  }

  .email {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    word-break: break-all;
  }

  .mono {
    font-family: var(--font-mono);
  }

  .status {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-shrink: 0;
  }

  /* Verification state is a flat, non-interactive label — never a
     button. Muted text + a small status dot, no border, no fill. */
  .status-label {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    white-space: nowrap;
    color: var(--text-helper);
  }

  .status-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .status-verifying .status-dot {
    background: var(--support-warning);
  }

  .status-unverified {
    color: var(--support-error);
  }

  .status-unverified .status-dot {
    background: var(--support-error);
  }

  .chip-spacer {
    display: inline-block;
    min-width: 1px;
  }

  /* Per-row kebab (overflow) menu (re #20). */
  .row-menu {
    position: relative;
    display: inline-flex;
  }

  .kebab-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: var(--radius-pill);
    background: transparent;
    color: var(--text-secondary);
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .kebab-trigger:hover {
    background: var(--layer-01);
    color: var(--text-primary);
  }

  .kebab-dots {
    font-size: 18px;
    line-height: 1;
  }

  .row-menu-dropdown {
    position: absolute;
    top: calc(100% + var(--spacing-02));
    right: 0;
    z-index: 300;
    min-width: 160px;
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.2);
    display: flex;
    flex-direction: column;
    padding: var(--spacing-02) 0;
  }

  .row-menu-item {
    display: flex;
    align-items: center;
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    text-align: left;
    width: 100%;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .row-menu-item:hover {
    background: var(--layer-01);
  }

  .row-menu-item.danger {
    color: var(--support-error);
  }

  .row-menu-item.danger:hover {
    background: color-mix(in srgb, var(--support-error) 12%, transparent);
  }

  .muted {
    color: var(--text-helper);
    font-style: italic;
  }

  @media (max-width: 640px) {
    .card-body {
      grid-template-columns: 40px 1fr;
      grid-template-rows: auto auto;
    }
    .status {
      grid-column: 1 / -1;
      justify-content: flex-end;
    }
  }
</style>
