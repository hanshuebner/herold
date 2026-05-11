<script lang="ts">
  /**
   * Identity list in the Account section of Settings.
   *
   * REQ-SET-IDENT-01..08: one row per Identity, with a default-selector
   * radio at the leading edge, the avatar thumbnail, the display name /
   * email, the three-state verification chip, and an external-submission
   * disabled treatment for unverified / mis-configured external rows.
   *
   * Clicking anywhere on the row (except the radio) opens the per-identity
   * edit dialog (REQ-SET-IDENT-10) via the `onedit` callback. The radio
   * promotes the row to default via `mail.setDefaultIdentity` (REQ-SET-
   * IDENT-04). An "Add identity" button sits above the list; for v1 it
   * is rendered as a disabled stub — the wizard ships in task #21.
   *
   * For external-submission state we read the per-identity store; if the
   * external-submission capability is absent (`hasExternalSubmission()`
   * returns false) the disabled treatment falls back to a verification-
   * only gate so the list still renders sensibly.
   */
  import type { Identity } from '../../lib/mail/types';
  import { mail } from '../../lib/mail/store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
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

  interface Props {
    /** Callback fired when the user clicks a row (anywhere except the radio). */
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

  function onRowClick(identity: Identity, event: MouseEvent | KeyboardEvent): void {
    // Don't propagate clicks on interactive child elements (radio, buttons).
    const target = event.target as HTMLElement | null;
    if (target?.closest('input,button,a')) return;
    onedit(identity);
  }

  function onRowKey(identity: Identity, event: KeyboardEvent): void {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    const target = event.target as HTMLElement | null;
    if (target?.closest('input,button,a')) return;
    event.preventDefault();
    onedit(identity);
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
</script>

<div class="identity-list" data-testid="identity-list">
  <header class="list-header">
    <h3>{t('settings.identityList.heading')}</h3>
    {#if showAddBtn}
      <button
        type="button"
        class="add-btn"
        onclick={onAddIdentity}
        title={t('settings.identityList.addTooltip')}
        data-testid="identity-add-btn"
      >
        + {t('settings.identityList.addBtn')}
      </button>
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
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <li
          class="row"
          class:disabled
          class:default={isDefault}
          data-testid="identity-row"
          data-identity-id={identity.id}
          data-identity-status={status}
        >
          <!-- Whole-row clickable wrapper. The radio and action buttons
               stop their own clicks from bubbling so they don't open
               the edit dialog. -->
          <div
            class="row-body"
            role="button"
            tabindex="0"
            aria-label={t('settings.identityList.editRowAria', { email: identity.email })}
            onclick={(e) => onRowClick(identity, e)}
            onkeydown={(e) => onRowKey(identity, e)}
          >
            <label class="radio-wrap" title={canBeDefault(identity)
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
                onclick={(e) => e.stopPropagation()}
                data-testid="identity-default-radio"
              />
            </label>

            <div class="avatar-wrap" aria-hidden="true">
              {#if avatarUrl}
                <img src={avatarUrl} alt="" class="avatar" />
              {:else}
                <div class="avatar avatar-placeholder">{initial(identity)}</div>
              {/if}
            </div>

            <div class="meta">
              <span class="name">{identity.name || identity.email}</span>
              {#if identity.name}
                <span class="email mono">{identity.email}</span>
              {/if}
            </div>

            <div class="status">
              {#if status === 'verifying'}
                <span class="chip chip-verifying" data-testid="identity-chip">
                  {t('settings.identityList.chip.verifying')}
                </span>
                <button
                  type="button"
                  class="action ghost"
                  onclick={(e) => {
                    e.stopPropagation();
                    onResend(identity);
                  }}
                  title={t('settings.identityList.resendTooltip')}
                  data-testid="identity-resend-btn"
                >
                  {t('settings.identityList.resendBtn')}
                </button>
              {:else if status === 'unverified'}
                <span class="chip chip-unverified" data-testid="identity-chip">
                  {t('settings.identityList.chip.unverified')}
                </span>
                <button
                  type="button"
                  class="action primary"
                  onclick={(e) => {
                    e.stopPropagation();
                    onVerify(identity);
                  }}
                  title={t('settings.identityList.verifyTooltip')}
                  data-testid="identity-verify-btn"
                >
                  {t('settings.identityList.verifyBtn')}
                </button>
              {:else}
                <!-- REQ-SET-IDENT-02: verified rows have no chip
                     (silent normal) — render an empty status cell so
                     the layout grid still aligns. -->
                <span class="chip-spacer" aria-hidden="true"></span>
              {/if}
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

  .add-btn {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    font-size: var(--type-body-compact-01-size);
  }

  .add-btn:hover {
    filter: brightness(1.1);
  }

  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .row {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .row:hover:not(.disabled) {
    border-color: var(--interactive);
  }

  .row.default {
    border-color: color-mix(in srgb, var(--interactive) 60%, transparent);
    background: color-mix(in srgb, var(--interactive) 4%, var(--layer-01));
  }

  .row.disabled {
    opacity: 0.55;
  }

  .row-body {
    display: grid;
    grid-template-columns: auto auto 1fr auto;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-04);
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    color: inherit;
    cursor: pointer;
    border-radius: var(--radius-md);
  }

  .row.disabled .row-body {
    cursor: default;
  }

  .row-body:focus-visible {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .radio-wrap {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
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

  .name {
    color: var(--text-primary);
    font-weight: 600;
    font-size: var(--type-body-01-size);
    word-break: break-word;
  }

  .email {
    color: var(--text-helper);
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

  .chip {
    display: inline-flex;
    align-items: center;
    padding: 2px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    white-space: nowrap;
  }

  .chip-verifying {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: color-mix(in srgb, var(--support-warning) 90%, var(--text-primary));
    border: 1px solid color-mix(in srgb, var(--support-warning) 50%, transparent);
  }

  .chip-unverified {
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
    color: var(--support-error);
    border: 1px solid color-mix(in srgb, var(--support-error) 50%, transparent);
  }

  .chip-spacer {
    display: inline-block;
    min-width: 1px;
  }

  .action {
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-pill);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    min-height: 28px;
  }

  .action.primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .action.primary:hover {
    filter: brightness(1.1);
  }

  .action.ghost {
    color: var(--text-secondary);
    background: transparent;
    border: 1px solid var(--border-subtle-01);
  }

  .action.ghost:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .muted {
    color: var(--text-helper);
    font-style: italic;
  }

  @media (max-width: 640px) {
    .row-body {
      grid-template-columns: auto auto 1fr;
      grid-template-rows: auto auto;
    }
    .status {
      grid-column: 1 / -1;
      justify-content: flex-end;
    }
  }
</style>
