<script lang="ts">
  /**
   * Identity list in the Account section of Settings.
   *
   * REQ-SET-IDENT-01..08: one row per Identity, with a default-selector
   * radio at the leading edge under a "Standard" column header, the
   * avatar thumbnail, the display name / email, the verification status
   * label, and an external-submission disabled treatment for unverified
   * / mis-configured external rows.
   *
   * The row itself is a plain presentational container: it is NOT
   * click-to-edit. Editing is an explicit per-row pencil button at the
   * trailing edge that fires `onedit`. The radio promotes the row to
   * default via `mail.setDefaultIdentity` (REQ-SET-IDENT-04); the
   * default row also carries a static "Standard" badge.
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
  import EditIcon from '../../lib/icons/EditIcon.svelte';

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
    <div class="rows-header" aria-hidden="true">
      <span class="col-default">{t('settings.identityList.defaultColumn')}</span>
      <span class="col-spacer"></span>
      <span class="col-identity">{t('settings.identityList.identityColumn')}</span>
    </div>
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
          <div class="row-body">
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
                  onclick={() => onResend(identity)}
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
                  onclick={() => onVerify(identity)}
                >
                  {t('settings.identityList.verifyBtn')}
                </Button>
              {:else}
                <!-- REQ-SET-IDENT-02: verified rows have no status label
                     (silent normal) — render an empty status cell so
                     the layout grid still aligns. -->
                <span class="chip-spacer" aria-hidden="true"></span>
              {/if}

              <Button
                variant="ghost"
                compact
                ariaLabel={t('settings.identityList.editRowAria', { email: identity.email })}
                title={t('settings.identityList.editBtn')}
                testid="identity-edit-btn"
                onclick={() => onedit(identity)}
              >
                {#snippet icon()}<EditIcon size={16} />{/snippet}
                {t('settings.identityList.editBtn')}
              </Button>
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

  /* Column headers align with the row-body grid: radio | avatar | meta. */
  .rows-header {
    display: grid;
    grid-template-columns: 24px 40px 1fr;
    gap: var(--spacing-04);
    padding: 0 var(--spacing-04);
  }

  .rows-header span {
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text-helper);
  }

  .col-default {
    text-align: center;
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
    grid-template-columns: 24px auto 1fr auto;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-04);
    width: 100%;
    text-align: left;
  }

  .radio-wrap {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
  }

  .radio-wrap input[type='radio'] {
    accent-color: var(--interactive);
    width: 18px;
    height: 18px;
    cursor: pointer;
  }

  .radio-wrap input[type='radio']:disabled {
    cursor: not-allowed;
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

  .muted {
    color: var(--text-helper);
    font-style: italic;
  }

  @media (max-width: 640px) {
    .row-body {
      grid-template-columns: 24px auto 1fr;
      grid-template-rows: auto auto;
    }
    .status {
      grid-column: 1 / -1;
      justify-content: flex-end;
    }
  }
</style>
