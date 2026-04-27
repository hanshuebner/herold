<script lang="ts">
  import { domainDetail, type CreateAliasPayload } from '../lib/domains/domain-detail.svelte';
  import { router } from '../lib/router/router.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';

  interface Props {
    name: string;
  }
  let { name }: Props = $props();

  $effect(() => {
    void domainDetail.load(name);
  });

  // --- Delete domain ---
  let deleteConfirmName = $state('');
  let deleteError = $state<string | null>(null);
  let deleteSubmitting = $state(false);

  async function deleteDomain(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (deleteSubmitting) return;
    if (deleteConfirmName !== name) {
      deleteError = 'Domain name does not match.';
      return;
    }
    deleteSubmitting = true;
    deleteError = null;
    const result = await domainDetail.deleteDomain(name);
    deleteSubmitting = false;
    if (!result.ok) {
      deleteError = result.errorMessage;
    } else {
      router.navigate('/domains');
    }
  }

  // --- New alias dialog ---
  let aliasOpen = $state(false);
  let aliasLocal = $state('');
  let aliasPrincipalSearch = $state('');
  let aliasPrincipalId = $state<number | null>(null);
  let aliasPrincipalEmail = $state('');
  let aliasExpiresAt = $state('');
  let aliasError = $state<string | null>(null);
  let aliasSubmitting = $state(false);

  // Simple principal lookup for the autocomplete.
  interface PrincipalOption {
    id: number;
    email: string;
  }
  let principalOptions = $state<PrincipalOption[]>([]);
  let principalSearching = $state(false);
  let principalDropdownOpen = $state(false);

  function openAlias(): void {
    aliasLocal = '';
    aliasPrincipalSearch = '';
    aliasPrincipalId = null;
    aliasPrincipalEmail = '';
    aliasExpiresAt = '';
    aliasError = null;
    principalOptions = [];
    principalDropdownOpen = false;
    aliasOpen = true;
  }

  let principalSearchTimer: ReturnType<typeof setTimeout> | null = null;

  function onPrincipalInput(): void {
    aliasPrincipalId = null;
    aliasPrincipalEmail = '';
    principalDropdownOpen = false;
    if (principalSearchTimer !== null) {
      clearTimeout(principalSearchTimer);
    }
    if (!aliasPrincipalSearch.trim()) {
      principalOptions = [];
      return;
    }
    principalSearchTimer = setTimeout(() => {
      void fetchPrincipals(aliasPrincipalSearch.trim());
    }, 200);
  }

  async function fetchPrincipals(query: string): Promise<void> {
    principalSearching = true;
    try {
      const res = await fetch(
        `/api/v1/principals?limit=20`,
        { credentials: 'include', headers: { Accept: 'application/json' } },
      );
      if (!res.ok) {
        principalOptions = [];
        return;
      }
      const data = (await res.json()) as { items?: Array<{ id: number; email?: string; canonical_email?: string }> };
      const needle = query.toLowerCase();
      principalOptions = (data.items ?? [])
        .filter((p) => {
          const email = p.email ?? p.canonical_email ?? '';
          return email.toLowerCase().includes(needle);
        })
        .map((p) => ({ id: Number(p.id), email: p.email ?? p.canonical_email ?? String(p.id) }))
        .slice(0, 8);
      principalDropdownOpen = principalOptions.length > 0;
    } finally {
      principalSearching = false;
    }
  }

  function selectPrincipal(opt: PrincipalOption): void {
    aliasPrincipalId = opt.id;
    aliasPrincipalEmail = opt.email;
    aliasPrincipalSearch = opt.email;
    principalDropdownOpen = false;
  }

  async function handleCreateAlias(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (aliasSubmitting) return;
    if (!aliasPrincipalId) {
      aliasError = 'Select a principal from the list.';
      return;
    }
    aliasError = null;
    aliasSubmitting = true;

    const payload: CreateAliasPayload = {
      local: aliasLocal.trim().toLowerCase(),
      domain: name,
      target_principal_id: aliasPrincipalId,
    };
    if (aliasExpiresAt.trim()) {
      payload.expires_at = new Date(aliasExpiresAt).toISOString();
    }

    const result = await domainDetail.createAlias(payload);
    aliasSubmitting = false;

    if (!result.ok) {
      aliasError = result.errorMessage;
      return;
    }
    aliasOpen = false;
  }

  // --- Inline alias delete confirmation ---
  let deleteAliasConfirmId = $state<string | null>(null);
  let deleteAliasError = $state<string | null>(null);

  async function confirmDeleteAlias(id: string): Promise<void> {
    deleteAliasError = null;
    const result = await domainDetail.deleteAlias(id);
    if (!result.ok) {
      deleteAliasError = result.errorMessage;
    }
    deleteAliasConfirmId = null;
  }

  function formatDate(iso: string | null | undefined): string {
    if (!iso) return 'never';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
  }
</script>

<div class="detail-page">
  <div class="page-header">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/domains')}
      aria-label="Back to domains"
    >
      Back
    </button>
    {#if domainDetail.domain}
      <div class="header-info">
        <h1 class="page-title">{domainDetail.domain.name}</h1>
      </div>
      <button
        type="button"
        class="btn-danger-outline"
        onclick={() => { deleteConfirmName = ''; deleteError = null; }}
        aria-controls="delete-zone"
      >
        Delete domain
      </button>
    {:else if domainDetail.status === 'loading'}
      <div class="spinner" role="status" aria-label="Loading"></div>
    {/if}
  </div>

  {#if domainDetail.status === 'error'}
    <div class="page-error" role="alert">{domainDetail.errorMessage}</div>
  {/if}

  {#if domainDetail.status === 'ready' && domainDetail.domain}
    <!-- Aliases section -->
    <div class="section">
      <div class="section-header">
        <h2 class="section-title">Aliases</h2>
        <button type="button" class="btn-primary" onclick={openAlias}>
          New alias
        </button>
      </div>

      {#if deleteAliasError}
        <div class="page-error" role="alert">{deleteAliasError}</div>
      {/if}

      <div class="table-wrapper">
        <table class="table">
          <thead>
            <tr>
              <th class="col-local">Local part</th>
              <th class="col-target">Target principal ID</th>
              <th class="col-expires">Expires</th>
              <th class="col-actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each domainDetail.aliases as alias (alias.id)}
              <tr class="table-row">
                <td class="col-local">
                  <span class="mono">{alias.local}@{alias.domain}</span>
                </td>
                <td class="col-target">
                  <span class="mono">{alias.target_principal_id}</span>
                </td>
                <td class="col-expires">{formatDate(alias.expires_at)}</td>
                <td class="col-actions">
                  {#if deleteAliasConfirmId === alias.id}
                    <div class="inline-confirm">
                      <span class="confirm-label">Delete?</span>
                      <button
                        type="button"
                        class="btn-danger-sm"
                        onclick={() => void confirmDeleteAlias(alias.id)}
                      >
                        Confirm
                      </button>
                      <button
                        type="button"
                        class="btn-ghost-sm"
                        onclick={() => { deleteAliasConfirmId = null; }}
                      >
                        Cancel
                      </button>
                    </div>
                  {:else}
                    <button
                      type="button"
                      class="btn-ghost-sm"
                      onclick={() => { deleteAliasConfirmId = alias.id; deleteAliasError = null; }}
                    >
                      Delete
                    </button>
                  {/if}
                </td>
              </tr>
            {:else}
              <tr>
                <td colspan="4" class="empty-row">No aliases for this domain.</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>

    <!-- Delete domain zone -->
    <div class="danger-zone" id="delete-zone">
      <h3 class="danger-title">Delete this domain</h3>
      <p class="danger-desc">
        Permanently removes the domain and all its aliases. This cannot be undone.
      </p>
      <form class="danger-form" onsubmit={deleteDomain} novalidate>
        <label for="dd-confirm" class="label">
          Type the domain name to confirm: <strong class="mono">{domainDetail.domain.name}</strong>
        </label>
        <div class="input-row">
          <input
            id="dd-confirm"
            type="text"
            class="input input-mono"
            placeholder={domainDetail.domain.name}
            bind:value={deleteConfirmName}
            disabled={deleteSubmitting}
            autocomplete="off"
          />
          <button
            type="submit"
            class="btn-danger"
            disabled={deleteSubmitting || deleteConfirmName !== domainDetail.domain.name}
          >
            {deleteSubmitting ? 'Deleting...' : 'Delete'}
          </button>
        </div>
        {#if deleteError}
          <p class="form-error" role="alert">{deleteError}</p>
        {/if}
      </form>
    </div>
  {/if}
</div>

<!-- New alias dialog -->
<Dialog bind:open={aliasOpen} title="New alias" width="520px">
  <form class="create-form" onsubmit={handleCreateAlias} novalidate>
    <div class="field">
      <label for="ca-local" class="label">Local part</label>
      <div class="alias-address-row">
        <input
          id="ca-local"
          type="text"
          class="input input-mono"
          placeholder="postmaster"
          required
          bind:value={aliasLocal}
          disabled={aliasSubmitting}
        />
        <span class="at-domain">@{name}</span>
      </div>
    </div>

    <div class="field">
      <label for="ca-principal" class="label">Target principal</label>
      <div class="autocomplete-wrapper">
        <input
          id="ca-principal"
          type="text"
          class="input"
          placeholder="Search by email..."
          autocomplete="off"
          bind:value={aliasPrincipalSearch}
          oninput={onPrincipalInput}
          disabled={aliasSubmitting}
        />
        {#if principalSearching}
          <div class="ac-spinner" aria-label="Searching"></div>
        {/if}
        {#if principalDropdownOpen && principalOptions.length > 0}
          <ul class="ac-dropdown" role="listbox" aria-label="Principal options">
            {#each principalOptions as opt (opt.id)}
              <li
                class="ac-option"
                role="option"
                tabindex="0"
                aria-selected={aliasPrincipalId === opt.id}
                onclick={() => selectPrincipal(opt)}
                onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectPrincipal(opt); } }}
              >
                <span class="ac-email">{opt.email}</span>
                <span class="ac-id">#{opt.id}</span>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
      {#if aliasPrincipalId}
        <p class="field-hint">
          Selected: <span class="mono">{aliasPrincipalEmail}</span> (ID {aliasPrincipalId})
        </p>
      {/if}
    </div>

    <div class="field">
      <label for="ca-expires" class="label">Expires at (optional)</label>
      <input
        id="ca-expires"
        type="datetime-local"
        class="input"
        bind:value={aliasExpiresAt}
        disabled={aliasSubmitting}
      />
    </div>

    {#if aliasError}
      <p class="form-error" role="alert">{aliasError}</p>
    {/if}

    <div class="form-actions">
      <button
        type="button"
        class="btn-secondary"
        onclick={() => { aliasOpen = false; }}
        disabled={aliasSubmitting}
      >
        Cancel
      </button>
      <button type="submit" class="btn-primary" disabled={aliasSubmitting || !aliasLocal.trim() || !aliasPrincipalId}>
        {aliasSubmitting ? 'Creating...' : 'Create alias'}
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
    font-family: var(--font-mono);
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

  /* Section */
  .section {
    margin-bottom: var(--spacing-08);
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-05);
  }

  .section-title {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    color: var(--text-primary);
    margin: 0;
  }

  /* Table */
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
  }

  .col-local { width: 40%; }
  .col-target { width: 25%; }
  .col-expires { width: 20%; white-space: nowrap; }
  .col-actions { width: 15%; text-align: right; }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .empty-row {
    padding: var(--spacing-07) var(--spacing-05) !important;
    text-align: center;
    color: var(--text-helper);
  }

  /* Inline confirm */
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

  /* Danger zone */
  .danger-zone {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 520px;
    padding: var(--spacing-05);
    border: 1px solid var(--support-error);
    border-radius: var(--radius-lg);
    background: color-mix(in srgb, var(--support-error) 5%, transparent);
    margin-top: var(--spacing-08);
  }

  .danger-title {
    font-size: var(--type-heading-01-size);
    line-height: var(--type-heading-01-line);
    font-weight: var(--type-heading-01-weight);
    color: var(--support-error);
    margin: 0;
  }

  .danger-desc {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .danger-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

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

  .input-row {
    display: flex;
    gap: var(--spacing-04);
    align-items: center;
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

  /* Dialog form */
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

  .alias-address-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
  }

  .alias-address-row .input {
    flex: 1;
  }

  .at-domain {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-secondary);
    white-space: nowrap;
    flex-shrink: 0;
  }

  /* Autocomplete */
  .autocomplete-wrapper {
    position: relative;
  }

  .ac-spinner {
    position: absolute;
    right: var(--spacing-04);
    top: 50%;
    transform: translateY(-50%);
    width: 14px;
    height: 14px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
  }

  .ac-dropdown {
    position: absolute;
    top: calc(100% + 2px);
    left: 0;
    right: 0;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    list-style: none;
    margin: 0;
    padding: var(--spacing-01) 0;
    z-index: 200;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
    max-height: 220px;
    overflow-y: auto;
  }

  .ac-option {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
    padding: var(--spacing-02) var(--spacing-04);
    cursor: pointer;
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .ac-option:hover,
  .ac-option[aria-selected="true"] {
    background: var(--layer-02);
  }

  .ac-email {
    color: var(--text-primary);
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .ac-id {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-helper);
    flex-shrink: 0;
  }

  .field-hint {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    margin: 0;
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

  .btn-danger {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--support-error);
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
  .btn-danger:hover:not(:disabled) {
    filter: brightness(0.9);
  }
  .btn-danger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger-outline {
    padding: var(--spacing-02) var(--spacing-04);
    background: none;
    color: var(--support-error);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--support-error);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
    flex-shrink: 0;
    margin-top: 2px;
  }
  .btn-danger-outline:hover {
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
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
