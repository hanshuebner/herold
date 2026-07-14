<script lang="ts">
  import { providerDetail, type CreateClaimMappingRulePayload } from '../lib/oidc-providers/provider-detail.svelte';
  import { superAdminState } from '../lib/auth/superadmin.svelte';
  import { router } from '../lib/router/router.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';
  import { formatAbsolute } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  interface Props {
    id: string;
  }
  let { id }: Props = $props();

  $effect(() => {
    void providerDetail.load(id);
    void superAdminState.check();
  });

  // -- authz_trusted toggle --------------------------------------------
  let trustSubmitting = $state(false);
  let trustError = $state<string | null>(null);

  async function setTrusted(trusted: boolean): Promise<void> {
    if (trustSubmitting || !providerDetail.provider) return;
    if (providerDetail.provider.authz_trusted === trusted) return;
    trustSubmitting = true;
    trustError = null;
    const result = await providerDetail.setAuthzTrusted(id, trusted);
    trustSubmitting = false;
    if (!result.ok) {
      trustError = result.errorMessage;
    }
  }

  // -- Claim allowlist ----------------------------------------------------
  let newClaim = $state('');
  let allowlistSubmitting = $state(false);
  let allowlistError = $state<string | null>(null);
  let removingClaim = $state<string | null>(null);

  async function handleAddClaim(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (allowlistSubmitting) return;
    const claim = newClaim.trim();
    if (!claim) return;
    allowlistSubmitting = true;
    allowlistError = null;
    const result = await providerDetail.addAllowlistClaim(id, claim);
    allowlistSubmitting = false;
    if (!result.ok) {
      allowlistError = result.errorMessage;
      return;
    }
    newClaim = '';
  }

  async function handleRemoveClaim(claim: string): Promise<void> {
    removingClaim = claim;
    allowlistError = null;
    const result = await providerDetail.deleteAllowlistClaim(id, claim);
    removingClaim = null;
    if (!result.ok) {
      allowlistError = result.errorMessage;
    }
  }

  // -- Claim-mapping rules --------------------------------------------------
  // resource_kind "server" is deliberately absent: the server always
  // refuses it (REQ-AC-64, "server:superadmin is never IdP-derivable"), so
  // it is never offered as a selectable option.
  const RESOURCE_KINDS = ['domain', 'list', 'mailbox'] as const;
  type ResourceKind = (typeof RESOURCE_KINDS)[number];

  const LEVELS_BY_KIND: Record<ResourceKind, string[]> = {
    domain: ['operator', 'owner'],
    list: ['moderator', 'owner'],
    mailbox: ['read', 'write', 'admin'],
  };

  let ruleDialogOpen = $state(false);
  let ruleClaim = $state('');
  let ruleMatchValue = $state('');
  let ruleResourceKind = $state<ResourceKind>('domain');
  let ruleResourceId = $state('');
  let ruleLevel = $state('operator');
  let ruleSubmitting = $state(false);
  let ruleError = $state<string | null>(null);

  const ruleLevelOptions = $derived(LEVELS_BY_KIND[ruleResourceKind]);

  function openRuleDialog(): void {
    ruleClaim = '';
    ruleMatchValue = '';
    ruleResourceKind = 'domain';
    ruleResourceId = '';
    ruleLevel = LEVELS_BY_KIND.domain[0]!;
    ruleError = null;
    ruleDialogOpen = true;
  }

  function onResourceKindChange(kind: ResourceKind): void {
    ruleResourceKind = kind;
    ruleLevel = LEVELS_BY_KIND[kind][0]!;
  }

  async function handleCreateRule(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (ruleSubmitting) return;
    const payload: CreateClaimMappingRulePayload = {
      claim: ruleClaim.trim(),
      match_value: ruleMatchValue.trim(),
      resource_kind: ruleResourceKind,
      resource_id: ruleResourceId.trim(),
      level: ruleLevel,
    };
    if (!payload.claim || !payload.match_value || !payload.resource_id) {
      ruleError = t('oidcProviderDetail.rule.error.required');
      return;
    }
    ruleSubmitting = true;
    ruleError = null;
    const result = await providerDetail.createRule(id, payload);
    ruleSubmitting = false;
    if (!result.ok) {
      ruleError = result.errorMessage;
      return;
    }
    ruleDialogOpen = false;
  }

  let deleteRuleConfirmId = $state<number | null>(null);
  let ruleListError = $state<string | null>(null);

  async function confirmDeleteRule(ruleId: number): Promise<void> {
    ruleListError = null;
    const result = await providerDetail.deleteRule(id, ruleId);
    if (!result.ok) {
      ruleListError = result.errorMessage;
    }
    deleteRuleConfirmId = null;
  }

  function formatDate(iso: string): string {
    return formatAbsolute(iso);
  }
</script>

<div class="detail-page">
  <div class="page-header">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/oidc-providers')}
      aria-label={t('oidcProviderDetail.backAriaLabel')}
    >
      {t('oidcProviderDetail.back')}
    </button>
    {#if providerDetail.provider}
      <div class="header-info">
        <h1 class="page-title">{providerDetail.provider.name}</h1>
      </div>
    {:else if providerDetail.status === 'loading'}
      <div class="spinner" role="status" aria-label={t('common.loading')}></div>
    {/if}
  </div>

  {#if providerDetail.status === 'error'}
    <div class="page-error" role="alert">{providerDetail.errorMessage}</div>
  {/if}

  {#if providerDetail.status === 'ready' && providerDetail.provider}
    <!-- Provider info -->
    <div class="section">
      <h2 class="section-title">{t('oidcProviderDetail.info.title')}</h2>
      <dl class="info-grid">
        <dt>{t('oidcProviderDetail.info.issuer')}</dt>
        <dd class="mono">{providerDetail.provider.issuer}</dd>
        <dt>{t('oidcProviderDetail.info.clientId')}</dt>
        <dd class="mono">{providerDetail.provider.client_id}</dd>
        <dt>{t('oidcProviderDetail.info.scopes')}</dt>
        <dd class="mono">{providerDetail.provider.scopes.join(', ') || t('common.none')}</dd>
        <dt>{t('oidcProviderDetail.info.created')}</dt>
        <dd>{formatDate(providerDetail.provider.created_at)}</dd>
      </dl>
    </div>

    <!-- authz_trusted -->
    <div class="section">
      <h2 class="section-title">{t('oidcProviderDetail.trust.title')}</h2>
      <p class="section-desc">{t('oidcProviderDetail.trust.description')}</p>

      {#if superAdminState.isSuperAdmin}
        <div class="trust-toggle" role="radiogroup" aria-label={t('oidcProviderDetail.trust.title')}>
          <button
            type="button"
            class="mode-btn"
            class:active={providerDetail.provider.authz_trusted}
            role="radio"
            aria-checked={providerDetail.provider.authz_trusted}
            onclick={() => void setTrusted(true)}
            disabled={trustSubmitting}
          >
            {t('oidcProviderDetail.trust.trusted')}
          </button>
          <button
            type="button"
            class="mode-btn"
            class:active={!providerDetail.provider.authz_trusted}
            role="radio"
            aria-checked={!providerDetail.provider.authz_trusted}
            onclick={() => void setTrusted(false)}
            disabled={trustSubmitting}
          >
            {t('oidcProviderDetail.trust.untrusted')}
          </button>
        </div>
        {#if trustError}
          <p class="form-error" role="alert">{trustError}</p>
        {/if}
      {:else}
        <div class="trust-readonly">
          {#if providerDetail.provider.authz_trusted}
            <span class="badge badge-trusted">{t('oidcProviderDetail.trust.trusted')}</span>
          {:else}
            <span class="badge badge-untrusted">{t('oidcProviderDetail.trust.untrusted')}</span>
          {/if}
          <p class="field-hint">{t('oidcProviderDetail.trust.superadminOnly')}</p>
        </div>
      {/if}
    </div>

    <!-- Claim allowlist -->
    <div class="section">
      <h2 class="section-title">{t('oidcProviderDetail.allowlist.title')}</h2>
      <p class="section-desc">{t('oidcProviderDetail.allowlist.description')}</p>

      {#if allowlistError}
        <p class="form-error" role="alert">{allowlistError}</p>
      {/if}

      <div class="claim-list">
        {#each providerDetail.allowlist as entry (entry.claim)}
          <span class="claim-chip">
            <span class="mono">{entry.claim}</span>
            <button
              type="button"
              class="chip-remove"
              aria-label={t('oidcProviderDetail.allowlist.removeAriaLabel', { claim: entry.claim })}
              onclick={() => void handleRemoveClaim(entry.claim)}
              disabled={removingClaim === entry.claim}
            >
              &#10005;
            </button>
          </span>
        {:else}
          <p class="empty-state">{t('oidcProviderDetail.allowlist.empty')}</p>
        {/each}
      </div>

      <form class="inline-add-form" onsubmit={handleAddClaim} novalidate>
        <input
          type="text"
          class="input input-mono"
          placeholder={t('oidcProviderDetail.allowlist.addPlaceholder')}
          bind:value={newClaim}
          disabled={allowlistSubmitting}
          aria-label={t('oidcProviderDetail.allowlist.addPlaceholder')}
        />
        <button type="submit" class="btn-secondary" disabled={allowlistSubmitting || !newClaim.trim()}>
          {allowlistSubmitting ? t('oidcProviderDetail.allowlist.adding') : t('oidcProviderDetail.allowlist.add')}
        </button>
      </form>
    </div>

    <!-- Claim-mapping rules -->
    <div class="section">
      <div class="section-header">
        <h2 class="section-title">{t('oidcProviderDetail.rules.title')}</h2>
        <button type="button" class="btn-primary" onclick={openRuleDialog}>
          {t('oidcProviderDetail.rules.new')}
        </button>
      </div>
      <p class="section-desc">{t('oidcProviderDetail.rules.description')}</p>

      {#if ruleListError}
        <p class="form-error" role="alert">{ruleListError}</p>
      {/if}

      <div class="table-wrapper">
        <table class="table">
          <thead>
            <tr>
              <th>{t('oidcProviderDetail.rules.table.claim')}</th>
              <th>{t('oidcProviderDetail.rules.table.matchValue')}</th>
              <th>{t('oidcProviderDetail.rules.table.resource')}</th>
              <th>{t('oidcProviderDetail.rules.table.level')}</th>
              <th>{t('oidcProviderDetail.rules.table.status')}</th>
              <th>{t('oidcProviderDetail.rules.table.created')}</th>
              <th class="col-actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each providerDetail.rules as rule (rule.id)}
              <tr class="table-row">
                <td class="mono">{rule.claim}</td>
                <td class="mono">{rule.match_value}</td>
                <td class="mono">{rule.resource_kind}:{rule.resource_id}</td>
                <td class="mono">{rule.level}</td>
                <td>
                  {#if rule.orphaned}
                    <span class="badge badge-danger">{t('oidcProviderDetail.rules.badge.orphaned')}</span>
                  {:else if !rule.author_authority_valid}
                    <span class="badge badge-danger">{t('oidcProviderDetail.rules.badge.authorityInvalid')}</span>
                  {:else}
                    <span class="badge badge-trusted">{t('oidcProviderDetail.rules.badge.valid')}</span>
                  {/if}
                </td>
                <td>{formatDate(rule.created_at)}</td>
                <td class="col-actions">
                  {#if deleteRuleConfirmId === rule.id}
                    <div class="inline-confirm">
                      <span class="confirm-label">{t('oidcProviderDetail.rules.deleteConfirm')}</span>
                      <button
                        type="button"
                        class="btn-danger-sm"
                        onclick={() => void confirmDeleteRule(rule.id)}
                      >
                        {t('oidcProviderDetail.rules.confirm')}
                      </button>
                      <button
                        type="button"
                        class="btn-ghost-sm"
                        onclick={() => { deleteRuleConfirmId = null; }}
                      >
                        {t('common.cancel')}
                      </button>
                    </div>
                  {:else}
                    <button
                      type="button"
                      class="btn-ghost-sm"
                      onclick={() => { deleteRuleConfirmId = rule.id; ruleListError = null; }}
                    >
                      {t('oidcProviderDetail.rules.delete')}
                    </button>
                  {/if}
                </td>
              </tr>
            {:else}
              <tr>
                <td colspan="7" class="empty-row">{t('oidcProviderDetail.rules.empty')}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
</div>

<!-- New rule dialog -->
<Dialog bind:open={ruleDialogOpen} title={t('oidcProviderDetail.rule.dialogTitle')} width="520px">
  <form class="create-form" onsubmit={handleCreateRule} novalidate>
    <div class="field">
      <label for="rule-claim" class="label">{t('oidcProviderDetail.rule.claim')}</label>
      <input
        id="rule-claim"
        type="text"
        class="input input-mono"
        placeholder={t('oidcProviderDetail.rule.claimPlaceholder')}
        required
        bind:value={ruleClaim}
        disabled={ruleSubmitting}
      />
    </div>

    <div class="field">
      <label for="rule-match" class="label">{t('oidcProviderDetail.rule.matchValue')}</label>
      <input
        id="rule-match"
        type="text"
        class="input input-mono"
        placeholder={t('oidcProviderDetail.rule.matchValuePlaceholder')}
        required
        bind:value={ruleMatchValue}
        disabled={ruleSubmitting}
      />
    </div>

    <div class="field">
      <span class="label" id="rule-kind-label">{t('oidcProviderDetail.rule.resourceKind')}</span>
      <div class="target-mode-toggle" role="radiogroup" aria-labelledby="rule-kind-label">
        {#each RESOURCE_KINDS as kind (kind)}
          <button
            type="button"
            class="mode-btn"
            class:active={ruleResourceKind === kind}
            role="radio"
            aria-checked={ruleResourceKind === kind}
            onclick={() => onResourceKindChange(kind)}
            disabled={ruleSubmitting}
          >
            {t(`oidcProviderDetail.rule.resourceKind.${kind}`)}
          </button>
        {/each}
      </div>
    </div>

    <div class="field">
      <label for="rule-resource-id" class="label">{t('oidcProviderDetail.rule.resourceId')}</label>
      <input
        id="rule-resource-id"
        type="text"
        class="input input-mono"
        placeholder={t('oidcProviderDetail.rule.resourceIdPlaceholder')}
        required
        bind:value={ruleResourceId}
        disabled={ruleSubmitting}
      />
    </div>

    <div class="field">
      <label for="rule-level" class="label">{t('oidcProviderDetail.rule.level')}</label>
      <select id="rule-level" class="input" bind:value={ruleLevel} disabled={ruleSubmitting}>
        {#each ruleLevelOptions as level (level)}
          <option value={level}>{level}</option>
        {/each}
      </select>
    </div>

    {#if ruleError}
      <p class="form-error" role="alert">{ruleError}</p>
    {/if}

    <div class="form-actions">
      <button
        type="button"
        class="btn-secondary"
        onclick={() => { ruleDialogOpen = false; }}
        disabled={ruleSubmitting}
      >
        {t('common.cancel')}
      </button>
      <button
        type="submit"
        class="btn-primary"
        disabled={ruleSubmitting || !ruleClaim.trim() || !ruleMatchValue.trim() || !ruleResourceId.trim()}
      >
        {ruleSubmitting ? t('oidcProviderDetail.rule.creating') : t('oidcProviderDetail.rule.create')}
      </button>
    </div>
  </form>
</Dialog>

<style>
  .detail-page {
    max-width: 1000px;
  }

  .page-header {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-05);
    margin-bottom: var(--spacing-07);
    flex-wrap: wrap;
  }

  .back-btn {
    background: none;
    border: none;
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    padding: var(--spacing-02) 0;
    flex-shrink: 0;
    margin-top: 4px;
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
  }
  .back-btn:hover {
    opacity: 0.8;
  }

  .header-info {
    flex: 1;
    min-width: 0;
  }

  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0;
    word-break: break-all;
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    flex-shrink: 0;
    margin-top: 6px;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  .page-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
    margin-bottom: var(--spacing-06);
  }

  .section {
    margin-bottom: var(--spacing-08);
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-03);
  }

  .section-title {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    color: var(--text-primary);
    margin: 0 0 var(--spacing-02);
  }

  .section-desc {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-04);
    max-width: 640px;
  }

  .info-grid {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--spacing-02) var(--spacing-05);
    margin: 0;
  }
  .info-grid dt {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }
  .info-grid dd {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  /* Trust toggle */
  .trust-toggle {
    display: flex;
    gap: var(--spacing-02);
  }
  .trust-readonly {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .mode-btn {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    color: var(--text-secondary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .mode-btn:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .mode-btn.active {
    background: var(--interactive);
    color: var(--text-on-color);
    border-color: var(--interactive);
  }
  .mode-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .target-mode-toggle {
    display: flex;
    gap: var(--spacing-02);
    margin-bottom: var(--spacing-02);
  }

  .field-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
  }

  .badge {
    display: inline-block;
    padding: 1px var(--spacing-02);
    font-size: var(--type-helper-text-01-size);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.02em;
    border-radius: var(--radius-sm);
    white-space: nowrap;
  }
  .badge-trusted {
    color: var(--support-success);
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
  }
  .badge-untrusted {
    color: var(--text-secondary);
    background: var(--layer-02);
  }
  .badge-danger {
    color: var(--support-error);
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
  }

  /* Claim allowlist */
  .claim-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    margin-bottom: var(--spacing-04);
  }

  .claim-chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
  }

  .chip-remove {
    background: none;
    border: none;
    color: var(--text-secondary);
    cursor: pointer;
    font-size: 12px;
    line-height: 1;
    padding: 0;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .chip-remove:hover:not(:disabled) {
    color: var(--support-error);
  }
  .chip-remove:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .inline-add-form {
    display: flex;
    gap: var(--spacing-03);
    max-width: 480px;
  }
  .inline-add-form .input {
    flex: 1;
  }

  /* Table */
  .table-wrapper {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    overflow: hidden;
    overflow-x: auto;
  }

  .table {
    width: 100%;
    border-collapse: collapse;
  }

  .table thead tr {
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .table th {
    text-align: left;
    padding: var(--spacing-03) var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
    background: var(--layer-01);
  }

  .table-row {
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .table-row:last-child {
    border-bottom: none;
  }
  .table-row:hover {
    background: var(--layer-02);
  }

  .table td {
    padding: var(--spacing-03) var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    vertical-align: middle;
    white-space: nowrap;
  }

  .col-actions { text-align: right; }

  .empty-row {
    padding: var(--spacing-07) var(--spacing-05) !important;
    text-align: center;
    color: var(--text-helper);
    white-space: normal !important;
  }

  .empty-state {
    color: var(--text-helper);
    font-size: var(--type-body-01-size);
    margin: 0;
  }

  .inline-confirm {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    justify-content: flex-end;
  }

  .confirm-label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    white-space: nowrap;
  }

  /* Forms */
  .label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .input {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }
  .input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .input-mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
  }

  .form-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }

  .create-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-04);
    padding-top: var(--spacing-02);
  }

  /* Buttons */
  .btn-primary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-secondary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--layer-02);
    color: var(--text-primary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger-sm {
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--support-error);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    cursor: pointer;
    border: none;
    white-space: nowrap;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }
  .btn-danger-sm:hover {
    filter: brightness(0.9);
  }

  .btn-ghost-sm {
    padding: var(--spacing-01) var(--spacing-03);
    background: none;
    color: var(--text-secondary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
    white-space: nowrap;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .btn-ghost-sm:hover {
    background: var(--layer-02);
  }
</style>
