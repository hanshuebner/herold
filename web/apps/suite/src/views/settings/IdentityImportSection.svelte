<script lang="ts">
  /**
   * Receiving (IMAP import) section for the per-identity edit page.
   *
   * REQ-SET-IMAPIMP-01..04 / REQ-IMAP-IMP-61 / REQ-IMAP-IMP-90..96 /
   * REQ-IMAP-IMP-100..104.
   *
   * Rendered ONLY for external-domain identities (determined by the
   * caller: show when external submission is configured and verified)
   * and only when the JMAP session advertises the imap-import capability
   * (`hasIMAPImport()` = true). These gates are enforced in the parent
   * IdentityEditPage.svelte; this component does not re-check them.
   *
   * States this section can be in:
   *   1. Loading   — spinner while IMAPImport/get is in flight.
   *   2. Unconfigured — "Set up mail import" call-to-action + collapsed form.
   *   3. Configured / showing status — summary card with state, last sync,
   *      last error, backfill horizon; an edit toggle opens the form.
   *   4. Form open — fields for host/port/TLS/username/auth/credential/
   *      horizon; "Save" calls IMAPImport/set create (new) or update
   *      (edit); probe errors render inline.
   *   5. Error loading — inline error with Retry.
   */

  import { mail } from '../../lib/mail/store.svelte';
  import { imapImportStore } from '../../lib/jmap/imap-import-store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import type { Identity } from '../../lib/mail/types';
  import type {
    IMAPImportTLSMode,
    IMAPImportAuthMethod,
    BackfillHorizonPreset,
  } from '../../lib/jmap/imap-import';

  interface Props {
    identity: Identity;
    /** Called after a successful save or removal so the parent can refresh. */
    onchange?: () => void;
  }

  let { identity, onchange }: Props = $props();

  // ── Store handle ──────────────────────────────────────────────────────────

  const accountId = $derived(mail.mailAccountId ?? '');
  const handle = $derived(
    accountId ? imapImportStore.forIdentity(identity.id, accountId) : null,
  );

  $effect(() => {
    if (handle) void handle.load();
  });

  // ── UI state ──────────────────────────────────────────────────────────────

  /** Whether the set-up / edit form is expanded. */
  let formOpen = $state(false);
  /** Whether we're in "edit existing" mode (vs creating new). */
  let isEditing = $state(false);

  // Form fields
  let host = $state('');
  let port = $state(993);
  let tlsMode = $state<IMAPImportTLSMode>('implicit');
  let username = $state('');
  let authMethod = $state<IMAPImportAuthMethod>('app_password');
  let credential = $state('');
  let horizonPreset = $state<BackfillHorizonPreset>('90d');
  let horizonCustomDate = $state('');

  // Save state
  let saving = $state(false);
  let saveError = $state<string | null>(null);
  let validationErrors = $state<Record<string, string>>({});

  // Remove import state
  let showRemoveDialog = $state(false);
  let removeSaving = $state(false);
  let removeChoice = $state<'keep' | 'delete'>('keep');
  let removeError = $state<string | null>(null);

  // Complete migration state
  let showMigrateConfirm = $state(false);
  let migrating = $state(false);
  let migrateError = $state<string | null>(null);

  // ── Derived helpers ───────────────────────────────────────────────────────

  const account = $derived(handle?.account ?? null);
  const loadStatus = $derived(handle?.status ?? 'idle');

  /**
   * Look up the provenance label in the mailbox list to get the count
   * of imported messages (REQ-IMAP-IMP-100..101, REQ-SET-IMAPIMP-04).
   */
  const provenanceMailbox = $derived(
    account
      ? Array.from(mail.mailboxes.values()).find(
          (m) => m.name === account.accountName,
        )
      : null,
  );

  const importedCount = $derived(provenanceMailbox?.totalEmails ?? null);

  // ── Form initialization ───────────────────────────────────────────────────

  function openSetupForm(): void {
    isEditing = false;
    formOpen = true;
    saveError = null;
    validationErrors = {};
    // Pre-fill host/username from the submission config when known.
    // (The submission store is not directly accessible here without
    // a prop — the parent can pass defaults if needed; for now we leave
    // the fields empty and the user fills them in.)
    host = '';
    port = 993;
    tlsMode = 'implicit';
    username = identity.email; // good default for most providers
    authMethod = 'app_password';
    credential = '';
    horizonPreset = '90d';
    horizonCustomDate = '';
  }

  function openEditForm(): void {
    if (!account) return;
    isEditing = true;
    formOpen = true;
    saveError = null;
    validationErrors = {};
    host = account.host;
    port = account.port;
    tlsMode = account.tlsMode;
    username = account.username;
    authMethod = account.authMethod;
    credential = ''; // write-only — never pre-filled
    // Reverse-map horizon to preset
    if (account.backfillHorizon === 'all') {
      horizonPreset = 'all';
      horizonCustomDate = '';
    } else {
      horizonPreset = 'custom';
      horizonCustomDate = account.backfillHorizon;
    }
  }

  function cancelForm(): void {
    formOpen = false;
    saveError = null;
    validationErrors = {};
  }

  // ── Validation ────────────────────────────────────────────────────────────

  function validate(): boolean {
    const errs: Record<string, string> = {};
    if (!host.trim()) errs['host'] = t('settings.import.hostRequired');
    if (!username.trim()) errs['username'] = t('settings.import.usernameRequired');
    if (!isEditing && !credential.trim()) {
      errs['credential'] = t('settings.import.credentialRequired');
    }
    if (horizonPreset === 'custom' && !horizonCustomDate.trim()) {
      errs['horizon'] = t('settings.import.horizonRequired');
    }
    validationErrors = errs;
    return Object.keys(errs).length === 0;
  }

  function resolvedHorizon(): string {
    if (horizonPreset === 'custom') return horizonCustomDate.trim();
    return horizonPreset;
  }

  // ── Save ──────────────────────────────────────────────────────────────────

  async function save(): Promise<void> {
    if (!handle || saving) return;
    if (!validate()) return;
    saving = true;
    saveError = null;
    try {
      if (isEditing && account) {
        const patch: Parameters<typeof handle.update>[1] = {
          host: host.trim(),
          port,
          tlsMode,
          username: username.trim(),
          authMethod,
          backfillHorizon: resolvedHorizon(),
        };
        if (credential.trim()) {
          patch.credential = credential.trim();
        }
        await handle.update(account.id, patch);
      } else {
        await handle.create({
          accountName: identity.email,
          host: host.trim(),
          port,
          tlsMode,
          username: username.trim(),
          authMethod,
          backfillHorizon: resolvedHorizon(),
          credential: credential.trim(),
        });
      }
      formOpen = false;
      toast.show({ message: t('settings.import.toast.saved'), timeoutMs: 4000 });
      onchange?.();
    } catch (err) {
      saveError = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  // ── Remove import ─────────────────────────────────────────────────────────

  function showRemove(): void {
    removeChoice = 'keep';
    removeError = null;
    showRemoveDialog = true;
  }

  function cancelRemove(): void {
    showRemoveDialog = false;
    removeError = null;
  }

  async function confirmRemove(): Promise<void> {
    if (!handle || !account || removeSaving) return;
    removeSaving = true;
    removeError = null;
    try {
      await handle.destroy(account.id, removeChoice === 'delete');
      showRemoveDialog = false;
      toast.show({ message: t('settings.import.toast.removed'), timeoutMs: 4000 });
      onchange?.();
    } catch (err) {
      removeError = err instanceof Error ? err.message : String(err);
    } finally {
      removeSaving = false;
    }
  }

  // ── Complete migration (REQ-IMAP-IMP-90) ─────────────────────────────────

  function showMigrate(): void {
    migrateError = null;
    showMigrateConfirm = true;
  }

  function cancelMigrate(): void {
    showMigrateConfirm = false;
    migrateError = null;
  }

  async function confirmMigrate(): Promise<void> {
    if (!handle || !account || migrating) return;
    migrating = true;
    migrateError = null;
    try {
      await handle.update(account.id, { state: 'migrating' as const });
      showMigrateConfirm = false;
      toast.show({ message: t('settings.import.toast.migrationStarted'), timeoutMs: 5000 });
      onchange?.();
    } catch (err) {
      migrateError = err instanceof Error ? err.message : String(err);
    } finally {
      migrating = false;
    }
  }

  // ── Formatting helpers ────────────────────────────────────────────────────

  function formatHorizon(horizon: string): string {
    if (horizon === 'all') return t('settings.import.horizonAllLabel');
    return horizon; // already an absolute date string "YYYY-MM-DD"
  }

  function formatDate(iso: string): string {
    try {
      return new Intl.DateTimeFormat(undefined, {
        day: 'numeric',
        month: 'short',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      }).format(new Date(iso));
    } catch {
      return iso;
    }
  }

  function stateLabel(state: string): string {
    const key = `settings.import.status.${state}` as Parameters<typeof t>[0];
    return t(key);
  }

  function stateClass(state: string): string {
    if (state === 'enabled') return 'state-ok';
    if (state === 'migrating' || state === 'migrated') return 'state-info';
    if (state === 'errored') return 'state-error';
    return 'state-disabled';
  }

  function portForTLS(mode: IMAPImportTLSMode): number {
    return mode === 'implicit' ? 993 : 143;
  }

  function onTLSChange(): void {
    port = portForTLS(tlsMode);
  }
</script>

<div class="import-section" data-testid="identity-import-section">
  <h4 class="section-title">{t('settings.import.sectionTitle')}</h4>
  <p class="section-hint">{t('settings.import.sectionHint')}</p>

  {#if loadStatus === 'idle' || loadStatus === 'loading'}
    <div
      class="spinner"
      role="status"
      aria-label={t('settings.import.loadingAriaLabel')}
    ></div>

  {:else if loadStatus === 'error'}
    <p class="form-error" role="alert">{handle?.error}</p>
    <Button
      variant="secondary"
      compact
      onclick={() => void handle?.refresh()}
    >
      Retry
    </Button>

  {:else if account === null && !formOpen}
    <!-- Unconfigured state: call-to-action. -->
    <Button
      variant="secondary"
      testid="import-setup-btn"
      onclick={openSetupForm}
    >
      {t('settings.import.setupBtn')}
    </Button>

  {:else if account !== null && !formOpen}
    <!-- Configured state: status summary card. -->
    <div class="status-card" data-testid="import-status-card">
      <div class="status-row">
        <span
          class="state-badge {stateClass(account.state)}"
          data-testid="import-state-badge"
        >
          {stateLabel(account.state)}
        </span>
        <span class="account-name">{account.accountName}</span>
      </div>

      <div class="status-detail">
        <span class="detail-item">
          {account.host}:{account.port}
          ({account.tlsMode === 'implicit' ? 'TLS' : 'STARTTLS'})
        </span>
        <span class="detail-item">
          {t('settings.import.backfillHorizon', {
            horizon: formatHorizon(account.backfillHorizon),
          })}
        </span>
        {#if account.lastSuccessAt}
          <span class="detail-item">
            {t('settings.import.lastSuccess', {
              date: formatDate(account.lastSuccessAt),
            })}
          </span>
        {:else if importedCount !== null && importedCount > 0}
          <span class="detail-item muted">{t('settings.import.syncInProgress')}</span>
        {:else}
          <span class="detail-item muted">{t('settings.import.neverSynced')}</span>
        {/if}
        {#if account.lastError}
          <span class="detail-item error-text" data-testid="import-last-error">
            {t('settings.import.lastError', { error: account.lastError })}
          </span>
        {/if}
      </div>

      <div class="status-actions">
        <Button
          variant="secondary"
          compact
          testid="import-edit-btn"
          onclick={openEditForm}
        >
          Edit
        </Button>
        {#if account.state === 'enabled'}
          <Button
            variant="secondary"
            compact
            testid="import-migrate-btn"
            onclick={showMigrate}
          >
            {t('settings.import.migrateBtn')}
          </Button>
        {/if}
        <Button
          variant="danger"
          compact
          testid="import-remove-btn"
          onclick={showRemove}
        >
          {t('settings.import.removeBtn')}
        </Button>
      </div>
    </div>

  {/if}

  <!-- Set-up / edit form. -->
  {#if formOpen}
    <div class="import-form" data-testid="import-form">
      <h5 class="form-title">{t('settings.import.formTitle')}</h5>

      <!-- Host -->
      <div class="field">
        <label for="imap-host-{identity.id}" class="field-label">
          {t('settings.import.fieldHost')}
        </label>
        <input
          id="imap-host-{identity.id}"
          type="text"
          class="input"
          class:invalid={!!validationErrors['host']}
          placeholder="imap.gmail.com"
          bind:value={host}
          disabled={saving}
          autocomplete="off"
          spellcheck="false"
          data-testid="import-field-host"
        />
        {#if validationErrors['host']}
          <span class="field-error" role="alert">{validationErrors['host']}</span>
        {/if}
      </div>

      <!-- Port + TLS row -->
      <div class="field-row">
        <div class="field field-narrow">
          <label for="imap-port-{identity.id}" class="field-label">
            {t('settings.import.fieldPort')}
          </label>
          <input
            id="imap-port-{identity.id}"
            type="number"
            class="input"
            min="1"
            max="65535"
            bind:value={port}
            disabled={saving}
            data-testid="import-field-port"
          />
        </div>

        <div class="field field-grow">
          <label for="imap-tls-{identity.id}" class="field-label">
            {t('settings.import.fieldTLS')}
          </label>
          <select
            id="imap-tls-{identity.id}"
            class="select"
            bind:value={tlsMode}
            onchange={onTLSChange}
            disabled={saving}
            data-testid="import-field-tls"
          >
            <option value="implicit">{t('settings.import.tlsImplicit')}</option>
            <option value="starttls">{t('settings.import.tlsStarttls')}</option>
          </select>
        </div>
      </div>

      <!-- Username -->
      <div class="field">
        <label for="imap-user-{identity.id}" class="field-label">
          {t('settings.import.fieldUsername')}
        </label>
        <input
          id="imap-user-{identity.id}"
          type="text"
          class="input"
          class:invalid={!!validationErrors['username']}
          bind:value={username}
          disabled={saving}
          autocomplete="off"
          spellcheck="false"
          data-testid="import-field-username"
        />
        {#if validationErrors['username']}
          <span class="field-error" role="alert">{validationErrors['username']}</span>
        {/if}
      </div>

      <!-- Auth method -->
      <div class="field">
        <label for="imap-auth-{identity.id}" class="field-label">
          {t('settings.import.fieldAuthMethod')}
        </label>
        <select
          id="imap-auth-{identity.id}"
          class="select"
          bind:value={authMethod}
          disabled={saving}
          data-testid="import-field-auth"
        >
          <option value="password">{t('settings.import.authPassword')}</option>
          <option value="app_password">{t('settings.import.authAppPassword')}</option>
          <option value="xoauth2">{t('settings.import.authXoauth2')}</option>
        </select>
        {#if authMethod === 'xoauth2'}
          <span class="field-note">{t('settings.import.xoauth2Note')}</span>
        {/if}
      </div>

      <!-- Credential (write-only) -->
      {#if authMethod !== 'xoauth2'}
        <div class="field">
          <label for="imap-cred-{identity.id}" class="field-label">
            {t('settings.import.fieldCredential')}
            {#if isEditing}
              <span class="muted">{t('settings.import.credentialKeepExisting')}</span>
            {/if}
          </label>
          <input
            id="imap-cred-{identity.id}"
            type="password"
            class="input"
            class:invalid={!!validationErrors['credential']}
            autocomplete="new-password"
            bind:value={credential}
            disabled={saving}
            placeholder={isEditing ? '••••••••' : ''}
            data-testid="import-field-credential"
          />
          {#if validationErrors['credential']}
            <span class="field-error" role="alert">{validationErrors['credential']}</span>
          {/if}
        </div>
      {/if}

      <!-- Backfill horizon -->
      <div class="field">
        <label for="imap-horizon-{identity.id}" class="field-label">
          {t('settings.import.fieldHorizon')}
        </label>
        <select
          id="imap-horizon-{identity.id}"
          class="select"
          bind:value={horizonPreset}
          disabled={saving || isEditing}
          data-testid="import-field-horizon"
        >
          <option value="30d">{t('settings.import.horizon30d')}</option>
          <option value="90d">{t('settings.import.horizon90d')}</option>
          <option value="1y">{t('settings.import.horizon1y')}</option>
          <option value="all">{t('settings.import.horizonAll')}</option>
          <option value="custom">{t('settings.import.horizonCustom')}</option>
        </select>
        {#if isEditing}
          <span class="field-note">
            Horizon cannot be changed after creation. Use "all" for a complete re-sync.
          </span>
        {/if}
      </div>

      {#if horizonPreset === 'custom'}
        <div class="field">
          <label for="imap-horizon-date-{identity.id}" class="field-label">
            {t('settings.import.fieldHorizonDate')}
          </label>
          <input
            id="imap-horizon-date-{identity.id}"
            type="date"
            class="input"
            class:invalid={!!validationErrors['horizon']}
            bind:value={horizonCustomDate}
            disabled={saving}
            data-testid="import-field-horizon-date"
          />
          {#if validationErrors['horizon']}
            <span class="field-error" role="alert">{validationErrors['horizon']}</span>
          {/if}
        </div>
      {/if}

      {#if saveError}
        <p class="form-error" role="alert" data-testid="import-save-error">{saveError}</p>
      {/if}

      <div class="form-actions">
        <Button
          variant="secondary"
          onclick={cancelForm}
          disabled={saving}
        >
          {t('settings.import.cancelBtn')}
        </Button>
        <Button
          variant="primary"
          onclick={() => void save()}
          disabled={saving}
          testid="import-save-btn"
        >
          {saving ? t('settings.import.saving') : t('settings.import.saveBtn')}
        </Button>
      </div>
    </div>
  {/if}

  <!-- Complete migration confirm dialog (REQ-IMAP-IMP-90). -->
  {#if showMigrateConfirm}
    <div class="overlay-backdrop" role="presentation"></div>
    <div
      class="confirm-dialog"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="migrate-title"
      data-testid="import-migrate-dialog"
    >
      <h5 id="migrate-title" class="dialog-title">{t('settings.import.migrateTitle')}</h5>
      <p class="dialog-message">{t('settings.import.migrateMessage')}</p>
      {#if migrateError}
        <p class="form-error" role="alert">{migrateError}</p>
      {/if}
      <div class="dialog-actions">
        <Button
          variant="secondary"
          onclick={cancelMigrate}
          disabled={migrating}
        >
          {t('settings.import.migrateCancel')}
        </Button>
        <Button
          variant="primary"
          onclick={() => void confirmMigrate()}
          disabled={migrating}
          testid="import-migrate-confirm"
        >
          {migrating ? t('settings.import.saving') : t('settings.import.migrateConfirm')}
        </Button>
      </div>
    </div>
  {/if}

  <!-- Remove import dialog (REQ-SET-IMAPIMP-04, REQ-IMAP-IMP-102). -->
  {#if showRemoveDialog}
    <div class="overlay-backdrop" role="presentation"></div>
    <div
      class="confirm-dialog"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="remove-import-title"
      data-testid="import-remove-dialog"
    >
      <h5 id="remove-import-title" class="dialog-title">
        {t('settings.import.removeTitle')}
      </h5>

      <!-- Keep-or-delete choice (REQ-IMAP-IMP-102) -->
      <div class="choice-group">
        <label class="choice-option" class:chosen={removeChoice === 'keep'}>
          <input
            type="radio"
            name="remove-import-choice-{identity.id}"
            value="keep"
            bind:group={removeChoice}
          />
          <div class="choice-text">
            <span class="choice-title">{t('settings.import.removeKeepTitle')}</span>
            <span class="choice-desc">{t('settings.import.removeKeepDesc')}</span>
          </div>
        </label>

        <label
          class="choice-option choice-danger"
          class:chosen={removeChoice === 'delete'}
        >
          <input
            type="radio"
            name="remove-import-choice-{identity.id}"
            value="delete"
            bind:group={removeChoice}
          />
          <div class="choice-text">
            <span class="choice-title">{t('settings.import.removeDeleteTitle')}</span>
            <span class="choice-desc">
              {importedCount !== null
                ? t('settings.import.removeDeleteDesc', { count: String(importedCount) })
                : t('settings.import.removeDeleteDescUnknown')}
            </span>
          </div>
        </label>
      </div>

      {#if removeError}
        <p class="form-error" role="alert">{removeError}</p>
      {/if}

      <div class="dialog-actions">
        <Button
          variant="secondary"
          onclick={cancelRemove}
          disabled={removeSaving}
        >
          {t('settings.import.removeCancel')}
        </Button>
        <Button
          variant={removeChoice === 'delete' ? 'danger' : 'secondary'}
          onclick={() => void confirmRemove()}
          disabled={removeSaving}
          testid="import-remove-confirm"
        >
          {removeSaving
            ? t('settings.import.saving')
            : removeChoice === 'delete'
              ? t('settings.import.removeConfirmDelete')
              : t('settings.import.removeConfirmKeep')}
        </Button>
      </div>
    </div>
  {/if}
</div>

<style>
  .import-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding-top: var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
    margin-top: var(--spacing-04);
    position: relative;
  }

  .section-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .section-hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
    line-height: var(--type-body-compact-01-line);
  }

  /* Spinner */
  .spinner {
    width: 16px;
    height: 16px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  /* Status card */
  .status-card {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .status-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .state-badge {
    display: inline-flex;
    align-items: center;
    padding: 2px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: uppercase;
    flex-shrink: 0;
  }

  .state-ok {
    background: color-mix(in srgb, var(--support-success) 16%, transparent);
    color: color-mix(in srgb, var(--support-success) 80%, var(--text-primary));
  }

  .state-disabled {
    background: color-mix(in srgb, var(--text-helper) 16%, transparent);
    color: var(--text-helper);
  }

  .state-error {
    background: color-mix(in srgb, var(--support-error) 16%, transparent);
    color: var(--support-error);
  }

  .state-info {
    background: color-mix(in srgb, var(--interactive) 16%, transparent);
    color: var(--interactive);
  }

  .account-name {
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    word-break: break-all;
  }

  .status-detail {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .detail-item {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .error-text {
    color: var(--support-error);
  }

  .muted {
    color: var(--text-helper);
    font-weight: 400;
  }

  .status-actions {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  /* Form */
  .import-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .form-title {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .field-row {
    display: flex;
    gap: var(--spacing-03);
  }

  .field-narrow {
    flex: 0 0 100px;
  }

  .field-grow {
    flex: 1;
  }

  .field-label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
    display: flex;
    gap: var(--spacing-02);
    align-items: baseline;
    flex-wrap: wrap;
  }

  .field-note {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    line-height: var(--type-body-compact-01-line);
  }

  .field-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
  }

  .input {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--background);
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
    opacity: 0.6;
    cursor: not-allowed;
  }

  .input.invalid {
    border-color: var(--support-error);
  }

  .select {
    width: 100%;
    box-sizing: border-box;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--background);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
  }

  .select:disabled {
    opacity: 0.6;
    cursor: not-allowed;
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

  .form-actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  /* Confirm dialogs */
  .overlay-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    z-index: 800;
  }

  .confirm-dialog {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    z-index: 900;
    min-width: 340px;
    max-width: 520px;
    width: 90vw;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.32);
    padding: var(--spacing-06);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .dialog-title {
    margin: 0;
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-primary);
  }

  .dialog-message {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    line-height: var(--type-body-compact-01-line);
  }

  .dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  /* Keep-or-delete choice group */
  .choice-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .choice-option {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 2px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .choice-option input[type='radio'] {
    flex-shrink: 0;
    margin-top: 3px;
    accent-color: var(--interactive);
    width: 16px;
    height: 16px;
  }

  .choice-option.chosen {
    border-color: var(--interactive);
  }

  .choice-option.choice-danger.chosen {
    border-color: var(--support-error);
  }

  .choice-text {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .choice-title {
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .choice-danger.chosen .choice-title {
    color: var(--support-error);
  }

  .choice-desc {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    line-height: var(--type-body-compact-01-line);
  }
</style>
