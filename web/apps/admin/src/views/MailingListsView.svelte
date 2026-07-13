<script lang="ts">
  import { mlists, type CreateMailingListPayload } from '../lib/mlists/mlists.svelte';
  import { router } from '../lib/router/router.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';
  import { formatDateOnly } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  let createOpen = $state(false);
  let createPostingAddress = $state('');
  let createDisplayName = $state('');
  let createOwnerPrincipalId = $state('');
  let createSubjectTag = $state('');
  let createArcSeal = $state(true);
  let createMaxMessageSize = $state('');
  let createError = $state<string | null>(null);
  let createSubmitting = $state(false);

  $effect(() => {
    if (mlists.status === 'idle') {
      void mlists.load();
    }
  });

  function openCreate(): void {
    createPostingAddress = '';
    createDisplayName = '';
    createOwnerPrincipalId = '';
    createSubjectTag = '';
    createArcSeal = true;
    createMaxMessageSize = '';
    createError = null;
    createOpen = true;
  }

  async function handleCreate(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (createSubmitting) return;
    createError = null;

    const payload: CreateMailingListPayload = {
      posting_address: createPostingAddress.trim().toLowerCase(),
      display_name: createDisplayName.trim(),
      arc_seal: createArcSeal,
    };
    if (createOwnerPrincipalId.trim()) {
      const id = Number(createOwnerPrincipalId.trim());
      if (!Number.isFinite(id) || id <= 0) {
        createError = t('mlists.create.error.invalidOwnerId');
        return;
      }
      payload.owner_principal_id = id;
    }
    if (createSubjectTag.trim()) {
      payload.subject_tag = createSubjectTag.trim();
    }
    if (createMaxMessageSize.trim()) {
      const size = Number(createMaxMessageSize.trim());
      if (!Number.isFinite(size) || size < 0) {
        createError = t('mlists.create.error.invalidMaxSize');
        return;
      }
      payload.max_message_size_bytes = size;
    }

    createSubmitting = true;
    const result = await mlists.create(payload);
    createSubmitting = false;

    if (!result.ok) {
      createError = result.errorMessage;
      return;
    }

    createOpen = false;
    if (result.id !== undefined) {
      router.navigate(`/lists/${result.id}`);
    }
  }
</script>

<div class="mlists-page">
  <div class="page-header">
    <div class="page-header-left">
      <h1 class="page-title">{t('mlists.title')}</h1>
      {#if mlists.status === 'loading'}
        <div class="spinner" role="status" aria-label={t('common.loading')}></div>
      {/if}
    </div>
    <div class="page-header-right">
      <button type="button" class="btn-primary" onclick={openCreate}>
        {t('mlists.new')}
      </button>
    </div>
  </div>

  {#if mlists.errorMessage && mlists.status === 'error'}
    <div class="page-error" role="alert">{mlists.errorMessage}</div>
  {/if}

  {#if mlists.status === 'ready' || mlists.items.length > 0}
    <div class="table-wrapper">
      <table class="table">
        <thead>
          <tr>
            <th class="col-name">{t('mlists.table.displayName')}</th>
            <th class="col-address">{t('mlists.table.postingAddress')}</th>
            <th class="col-owner">{t('mlists.table.owner')}</th>
            <th class="col-created">{t('mlists.table.created')}</th>
          </tr>
        </thead>
        <tbody>
          {#each mlists.items as list (list.id)}
            <tr class="table-row" onclick={() => router.navigate(`/lists/${list.id}`)}>
              <td class="col-name">{list.display_name}</td>
              <td class="col-address"><span class="mono">{list.posting_address}</span></td>
              <td class="col-owner"><span class="mono">#{list.owner_principal_id}</span></td>
              <td class="col-created">{formatDateOnly(list.created_at)}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="4" class="empty-row">{t('mlists.empty')}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if mlists.hasMore}
      <div class="load-more">
        <button
          type="button"
          class="btn-secondary"
          onclick={() => void mlists.loadMore()}
          disabled={mlists.status === 'loading'}
        >
          {mlists.status === 'loading' ? t('common.loading') : t('mlists.loadMore')}
        </button>
      </div>
    {/if}
  {:else if mlists.status !== 'loading' && mlists.status !== 'idle'}
    <p class="empty-state">{t('mlists.empty')}</p>
  {/if}
</div>

<Dialog bind:open={createOpen} title={t('mlists.create.title')} width="520px">
  <form class="create-form" onsubmit={handleCreate} novalidate>
    <div class="field">
      <label for="cl-address" class="label">{t('mlists.create.postingAddress')}</label>
      <input
        id="cl-address"
        type="email"
        class="input input-mono"
        placeholder="announce@example.com"
        autocomplete="off"
        required
        bind:value={createPostingAddress}
        disabled={createSubmitting}
      />
    </div>

    <div class="field">
      <label for="cl-name" class="label">{t('mlists.create.displayName')}</label>
      <input
        id="cl-name"
        type="text"
        class="input"
        placeholder={t('mlists.create.displayNamePlaceholder')}
        autocomplete="off"
        required
        bind:value={createDisplayName}
        disabled={createSubmitting}
      />
    </div>

    <div class="field">
      <label for="cl-owner" class="label">{t('mlists.create.ownerPrincipalId')}</label>
      <input
        id="cl-owner"
        type="text"
        class="input input-mono"
        placeholder={t('mlists.create.ownerPrincipalIdPlaceholder')}
        autocomplete="off"
        inputmode="numeric"
        bind:value={createOwnerPrincipalId}
        disabled={createSubmitting}
      />
      <p class="field-hint">{t('mlists.create.ownerHint')}</p>
    </div>

    <div class="field">
      <label for="cl-tag" class="label">{t('mlists.create.subjectTag')}</label>
      <input
        id="cl-tag"
        type="text"
        class="input"
        placeholder={t('mlists.create.subjectTagPlaceholder')}
        autocomplete="off"
        bind:value={createSubjectTag}
        disabled={createSubmitting}
      />
    </div>

    <div class="field">
      <label class="checkbox-row" for="cl-arcseal">
        <input id="cl-arcseal" type="checkbox" bind:checked={createArcSeal} disabled={createSubmitting} />
        {t('mlists.create.arcSeal')}
      </label>
    </div>

    <div class="field">
      <label for="cl-maxsize" class="label">{t('mlists.create.maxMessageSize')}</label>
      <input
        id="cl-maxsize"
        type="text"
        class="input input-mono"
        placeholder={t('mlists.create.maxMessageSizePlaceholder')}
        autocomplete="off"
        inputmode="numeric"
        bind:value={createMaxMessageSize}
        disabled={createSubmitting}
      />
    </div>

    {#if createError}
      <p class="form-error" role="alert">{createError}</p>
    {/if}

    <div class="form-actions">
      <button
        type="button"
        class="btn-secondary"
        onclick={() => { createOpen = false; }}
        disabled={createSubmitting}
      >
        {t('common.cancel')}
      </button>
      <button
        type="submit"
        class="btn-primary"
        disabled={createSubmitting || !createPostingAddress.trim() || !createDisplayName.trim()}
      >
        {createSubmitting ? t('mlists.create.submitting') : t('mlists.create.submit')}
      </button>
    </div>
  </form>
</Dialog>

<style>
  .mlists-page {
    max-width: 1000px;
  }

  .page-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-05);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-06);
  }

  .page-header-left {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .page-header-right {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }

  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    flex-shrink: 0;
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

  .table-wrapper {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    overflow: hidden;
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
    cursor: pointer;
    border-bottom: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
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
  }

  .col-name { width: 30%; }
  .col-address { width: 35%; }
  .col-owner { width: 15%; }
  .col-created { width: 20%; white-space: nowrap; }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .empty-row {
    padding: var(--spacing-07) var(--spacing-05) !important;
    text-align: center;
    color: var(--text-helper);
  }

  .empty-state {
    color: var(--text-helper);
    font-size: var(--type-body-01-size);
  }

  .load-more {
    margin-top: var(--spacing-05);
    text-align: center;
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

  .label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-secondary);
  }

  .checkbox-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    cursor: pointer;
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

  .field-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
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
    justify-content: flex-end;
    gap: var(--spacing-04);
    padding-top: var(--spacing-02);
  }

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
</style>
