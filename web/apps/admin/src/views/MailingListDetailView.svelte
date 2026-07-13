<script lang="ts">
  import {
    mlistDetail,
    type CreateMemberPayload,
    type MailingListMemberStateFilter,
    type MailingListMemberModeFilter,
  } from '../lib/mlists/mlist-detail.svelte';
  import { router } from '../lib/router/router.svelte';
  import Dialog from '../lib/ui/Dialog.svelte';
  import { formatDateOnly, formatAbsolute } from '../lib/format';
  import { t } from '../lib/i18n/i18n.svelte';

  interface Props {
    id: string;
  }
  let { id }: Props = $props();

  $effect(() => {
    void mlistDetail.load(id);
  });

  // ── Config edit form ─────────────────────────────────────────────────
  let editDisplayName = $state('');
  let editSubjectTag = $state('');
  let editArcSeal = $state(true);
  let editMaxMessageSize = $state('');
  let editOwnerPrincipalId = $state('');
  let configError = $state<string | null>(null);
  let configSaving = $state(false);
  let configSaved = $state(false);

  $effect(() => {
    const list = mlistDetail.list;
    if (list) {
      editDisplayName = list.display_name;
      editSubjectTag = list.subject_tag ?? '';
      editArcSeal = list.arc_seal;
      editMaxMessageSize = list.max_message_size_bytes ? String(list.max_message_size_bytes) : '';
      editOwnerPrincipalId = String(list.owner_principal_id);
    }
  });

  async function saveConfig(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (configSaving) return;
    configError = null;
    configSaved = false;

    if (!editDisplayName.trim()) {
      configError = t('mlistDetail.config.error.displayNameRequired');
      return;
    }
    const ownerId = Number(editOwnerPrincipalId.trim());
    if (!Number.isFinite(ownerId) || ownerId <= 0) {
      configError = t('mlistDetail.config.error.invalidOwnerId');
      return;
    }
    let maxSize = 0;
    if (editMaxMessageSize.trim()) {
      maxSize = Number(editMaxMessageSize.trim());
      if (!Number.isFinite(maxSize) || maxSize < 0) {
        configError = t('mlistDetail.config.error.invalidMaxSize');
        return;
      }
    }

    configSaving = true;
    const result = await mlistDetail.updateList(id, {
      display_name: editDisplayName.trim(),
      subject_tag: editSubjectTag.trim(),
      arc_seal: editArcSeal,
      max_message_size_bytes: maxSize,
      owner_principal_id: ownerId,
    });
    configSaving = false;

    if (!result.ok) {
      configError = result.errorMessage;
      return;
    }
    configSaved = true;
  }

  // ── Delete list (danger zone) ────────────────────────────────────────
  let deleteConfirmAddress = $state('');
  let deleteError = $state<string | null>(null);
  let deleteSubmitting = $state(false);

  async function deleteList(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (deleteSubmitting) return;
    const list = mlistDetail.list;
    if (!list) return;
    if (deleteConfirmAddress !== list.posting_address) {
      deleteError = t('mlistDetail.danger.addressMismatch');
      return;
    }
    deleteSubmitting = true;
    deleteError = null;
    const result = await mlistDetail.deleteList(id);
    deleteSubmitting = false;
    if (!result.ok) {
      deleteError = result.errorMessage;
    } else {
      router.navigate('/lists');
    }
  }

  // ── Roster filters ───────────────────────────────────────────────────
  const stateFilterOptions: { value: MailingListMemberStateFilter; labelKey: string }[] = [
    { value: '', labelKey: 'mlistDetail.roster.state.all' },
    { value: 'active', labelKey: 'mlistDetail.roster.state.active' },
    { value: 'suspended', labelKey: 'mlistDetail.roster.state.suspended' },
    { value: 'unsubscribed', labelKey: 'mlistDetail.roster.state.unsubscribed' },
    { value: 'pending', labelKey: 'mlistDetail.roster.state.pending' },
  ];
  const modeFilterOptions: { value: MailingListMemberModeFilter; labelKey: string }[] = [
    { value: '', labelKey: 'mlistDetail.roster.mode.all' },
    { value: 'each', labelKey: 'mlistDetail.roster.mode.each' },
    { value: 'nomail', labelKey: 'mlistDetail.roster.mode.nomail' },
  ];

  function onStateFilterChange(e: Event): void {
    mlistDetail.stateFilter = (e.currentTarget as HTMLSelectElement).value as MailingListMemberStateFilter;
    void mlistDetail.loadMembers(id);
  }
  function onModeFilterChange(e: Event): void {
    mlistDetail.deliveryModeFilter = (e.currentTarget as HTMLSelectElement).value as MailingListMemberModeFilter;
    void mlistDetail.loadMembers(id);
  }

  // ── Per-row member edits ─────────────────────────────────────────────
  let memberRowError = $state<string | null>(null);

  async function onMemberStateChange(memberId: number, e: Event): Promise<void> {
    const state = (e.currentTarget as HTMLSelectElement).value;
    memberRowError = null;
    const result = await mlistDetail.updateMember(id, memberId, { state });
    if (!result.ok) memberRowError = result.errorMessage;
  }
  async function onMemberModeChange(memberId: number, e: Event): Promise<void> {
    const delivery_mode = (e.currentTarget as HTMLSelectElement).value;
    memberRowError = null;
    const result = await mlistDetail.updateMember(id, memberId, { delivery_mode });
    if (!result.ok) memberRowError = result.errorMessage;
  }

  let removeConfirmId = $state<number | null>(null);
  async function confirmRemove(memberId: number): Promise<void> {
    memberRowError = null;
    const result = await mlistDetail.removeMember(id, memberId);
    if (!result.ok) memberRowError = result.errorMessage;
    removeConfirmId = null;
  }

  function memberAddress(m: { principal_id?: number; external_address?: string }): string {
    if (m.external_address) return m.external_address;
    if (m.principal_id !== undefined) return `#${m.principal_id}`;
    return '';
  }

  // ── Add member dialog ────────────────────────────────────────────────
  type AddMemberMode = 'principal' | 'external';
  let addOpen = $state(false);
  let addMode = $state<AddMemberMode>('principal');
  let addPrincipalSearch = $state('');
  let addPrincipalId = $state<number | null>(null);
  let addPrincipalEmail = $state('');
  let addExternalAddress = $state('');
  let addError = $state<string | null>(null);
  let addSubmitting = $state(false);

  const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
  function isValidExternalAddress(addr: string): boolean {
    return EMAIL_PATTERN.test(addr.trim());
  }

  function openAdd(): void {
    addMode = 'principal';
    addPrincipalSearch = '';
    addPrincipalId = null;
    addPrincipalEmail = '';
    addExternalAddress = '';
    addError = null;
    principalOptions = [];
    principalDropdownOpen = false;
    addOpen = true;
  }

  function setAddMode(mode: AddMemberMode): void {
    addMode = mode;
    addError = null;
  }

  interface PrincipalOption {
    id: number;
    email: string;
  }
  let principalOptions = $state<PrincipalOption[]>([]);
  let principalSearching = $state(false);
  let principalDropdownOpen = $state(false);
  let principalSearchTimer: ReturnType<typeof setTimeout> | null = null;

  function onPrincipalInput(): void {
    addPrincipalId = null;
    addPrincipalEmail = '';
    principalDropdownOpen = false;
    if (principalSearchTimer !== null) {
      clearTimeout(principalSearchTimer);
    }
    if (!addPrincipalSearch.trim()) {
      principalOptions = [];
      return;
    }
    principalSearchTimer = setTimeout(() => {
      void fetchPrincipals(addPrincipalSearch.trim());
    }, 200);
  }

  async function fetchPrincipals(query: string): Promise<void> {
    principalSearching = true;
    try {
      const res = await fetch(`/api/v1/principals?limit=20`, {
        credentials: 'include',
        headers: { Accept: 'application/json' },
      });
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
    addPrincipalId = opt.id;
    addPrincipalEmail = opt.email;
    addPrincipalSearch = opt.email;
    principalDropdownOpen = false;
  }

  async function handleAddMember(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (addSubmitting) return;

    const payload: CreateMemberPayload = {};
    if (addMode === 'principal') {
      if (!addPrincipalId) {
        addError = t('mlistDetail.add.error.selectPrincipal');
        return;
      }
      payload.principal_id = addPrincipalId;
    } else {
      const addr = addExternalAddress.trim().toLowerCase();
      if (!isValidExternalAddress(addr)) {
        addError = t('mlistDetail.add.error.invalidAddress');
        return;
      }
      payload.external_address = addr;
    }

    addError = null;
    addSubmitting = true;
    const result = await mlistDetail.addMember(id, payload);
    addSubmitting = false;

    if (!result.ok) {
      addError = result.errorMessage;
      return;
    }
    addOpen = false;
  }

  // ── Bulk import dialog ───────────────────────────────────────────────
  let importOpen = $state(false);
  let importText = $state('');
  let importError = $state<string | null>(null);
  let importSubmitting = $state(false);
  let importResultSummary = $state<{ added: number; skipped: number; details: string[] } | null>(null);

  function openImport(): void {
    importText = '';
    importError = null;
    importResultSummary = null;
    importOpen = true;
  }

  function parseImportLines(text: string): CreateMemberPayload[] {
    return text
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line.length > 0)
      .map((line) => {
        if (line.startsWith('#')) {
          return { principal_id: Number(line.slice(1).trim()) };
        }
        return { external_address: line.toLowerCase() };
      });
  }

  async function handleImport(e: SubmitEvent): Promise<void> {
    e.preventDefault();
    if (importSubmitting) return;
    const members = parseImportLines(importText);
    if (members.length === 0) {
      importError = t('mlistDetail.import.error.empty');
      return;
    }
    importError = null;
    importResultSummary = null;
    importSubmitting = true;
    const result = await mlistDetail.importMembers(id, members);
    importSubmitting = false;

    if (!result.ok || !result.result) {
      importError = result.errorMessage;
      return;
    }
    importResultSummary = {
      added: result.result.added,
      skipped: result.result.skipped,
      details: result.result.results
        .filter((r) => r.status !== 'added')
        .map((r) => `#${r.index}: ${r.status}${r.detail ? ' - ' + r.detail : ''}`),
    };
  }

  // ── Export ────────────────────────────────────────────────────────────
  let exportError = $state<string | null>(null);
  let exporting = $state(false);

  async function handleExport(): Promise<void> {
    if (exporting) return;
    exportError = null;
    exporting = true;
    const result = await mlistDetail.exportMembers(id);
    exporting = false;
    if (!result.ok || !result.items) {
      exportError = result.errorMessage;
      return;
    }
    const lines = ['address_or_principal,state,delivery_mode,added_at'];
    for (const m of result.items) {
      const addr = m.external_address ?? `#${m.principal_id}`;
      lines.push([addr, m.state, m.delivery_mode, m.added_at].join(','));
    }
    const blob = new Blob([lines.join('\n')], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${mlistDetail.list?.posting_address ?? 'list'}-members.csv`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }
</script>

<div class="detail-page">
  <div class="page-header">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/lists')}
      aria-label={t('mlistDetail.backAriaLabel')}
    >
      {t('mlistDetail.back')}
    </button>
    {#if mlistDetail.list}
      <div class="header-info">
        <h1 class="page-title">{mlistDetail.list.display_name}</h1>
        <p class="page-subtitle mono">{mlistDetail.list.posting_address}</p>
      </div>
    {:else if mlistDetail.status === 'loading'}
      <div class="spinner" role="status" aria-label={t('common.loading')}></div>
    {/if}
  </div>

  {#if mlistDetail.status === 'error'}
    <div class="page-error" role="alert">{mlistDetail.errorMessage}</div>
  {/if}

  {#if mlistDetail.status === 'ready' && mlistDetail.list}
    {#if mlistDetail.list.dkim_key_missing}
      <div class="dkim-warning" role="alert">
        {t('mlistDetail.warning.dkimKeyMissing', { domain: mlistDetail.list.domain })}
      </div>
    {/if}

    <!-- Config section -->
    <div class="section">
      <div class="section-header">
        <h2 class="section-title">{t('mlistDetail.config.title')}</h2>
      </div>
      <form class="config-form" onsubmit={saveConfig} novalidate>
        <div class="field-row">
          <div class="field">
            <label for="ml-name" class="label">{t('mlistDetail.config.displayName')}</label>
            <input
              id="ml-name"
              type="text"
              class="input"
              bind:value={editDisplayName}
              disabled={configSaving}
            />
          </div>
          <div class="field">
            <label for="ml-owner" class="label">{t('mlistDetail.config.ownerPrincipalId')}</label>
            <input
              id="ml-owner"
              type="text"
              class="input input-mono"
              inputmode="numeric"
              bind:value={editOwnerPrincipalId}
              disabled={configSaving}
            />
          </div>
        </div>
        <div class="field-row">
          <div class="field">
            <label for="ml-tag" class="label">{t('mlistDetail.config.subjectTag')}</label>
            <input
              id="ml-tag"
              type="text"
              class="input"
              placeholder={t('mlistDetail.config.subjectTagPlaceholder')}
              bind:value={editSubjectTag}
              disabled={configSaving}
            />
          </div>
          <div class="field">
            <label for="ml-maxsize" class="label">{t('mlistDetail.config.maxMessageSize')}</label>
            <input
              id="ml-maxsize"
              type="text"
              class="input input-mono"
              inputmode="numeric"
              placeholder={t('mlistDetail.config.maxMessageSizePlaceholder')}
              bind:value={editMaxMessageSize}
              disabled={configSaving}
            />
          </div>
        </div>
        <div class="field">
          <label class="checkbox-row" for="ml-arcseal">
            <input id="ml-arcseal" type="checkbox" bind:checked={editArcSeal} disabled={configSaving} />
            {t('mlistDetail.config.arcSeal')}
          </label>
        </div>
        <p class="field-hint">
          {t('mlistDetail.config.policyHint', {
            posting: mlistDetail.list.posting_policy,
            subscribe: mlistDetail.list.subscribe_policy,
          })}
        </p>

        {#if configError}
          <p class="form-error" role="alert">{configError}</p>
        {/if}
        {#if configSaved}
          <p class="form-success">{t('common.saved')}</p>
        {/if}

        <div class="form-actions">
          <button type="submit" class="btn-primary" disabled={configSaving}>
            {configSaving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </form>
    </div>

    <!-- Roster section -->
    <div class="section">
      <div class="section-header">
        <h2 class="section-title">{t('mlistDetail.roster.title')}</h2>
        <div class="section-actions">
          <button type="button" class="btn-secondary" onclick={openImport}>
            {t('mlistDetail.roster.import')}
          </button>
          <button type="button" class="btn-secondary" onclick={() => void handleExport()} disabled={exporting}>
            {exporting ? t('common.loading') : t('mlistDetail.roster.export')}
          </button>
          <button type="button" class="btn-primary" onclick={openAdd}>
            {t('mlistDetail.roster.addMember')}
          </button>
        </div>
      </div>

      <div class="filter-bar">
        <select
          class="select"
          value={mlistDetail.stateFilter}
          onchange={onStateFilterChange}
          aria-label={t('mlistDetail.roster.stateFilterAriaLabel')}
        >
          {#each stateFilterOptions as opt (opt.value)}
            <option value={opt.value}>{t(opt.labelKey)}</option>
          {/each}
        </select>
        <select
          class="select"
          value={mlistDetail.deliveryModeFilter}
          onchange={onModeFilterChange}
          aria-label={t('mlistDetail.roster.modeFilterAriaLabel')}
        >
          {#each modeFilterOptions as opt (opt.value)}
            <option value={opt.value}>{t(opt.labelKey)}</option>
          {/each}
        </select>
      </div>

      {#if exportError}
        <div class="page-error" role="alert">{exportError}</div>
      {/if}
      {#if memberRowError}
        <div class="page-error" role="alert">{memberRowError}</div>
      {/if}
      {#if mlistDetail.membersErrorMessage && mlistDetail.membersStatus === 'error'}
        <div class="page-error" role="alert">{mlistDetail.membersErrorMessage}</div>
      {/if}

      <div class="table-wrapper">
        <table class="table">
          <thead>
            <tr>
              <th class="col-address">{t('mlistDetail.roster.table.address')}</th>
              <th class="col-state">{t('mlistDetail.roster.table.state')}</th>
              <th class="col-mode">{t('mlistDetail.roster.table.mode')}</th>
              <th class="col-added">{t('mlistDetail.roster.table.added')}</th>
              <th class="col-actions"></th>
            </tr>
          </thead>
          <tbody>
            {#each mlistDetail.members as member (member.id)}
              <tr class="table-row">
                <td class="col-address"><span class="mono">{memberAddress(member)}</span></td>
                <td class="col-state">
                  <select
                    class="select select-sm"
                    value={member.state}
                    onchange={(e) => void onMemberStateChange(member.id, e)}
                    aria-label={t('mlistDetail.roster.memberStateAriaLabel')}
                  >
                    {#each stateFilterOptions.filter((o) => o.value !== '') as opt (opt.value)}
                      <option value={opt.value}>{t(opt.labelKey)}</option>
                    {/each}
                  </select>
                </td>
                <td class="col-mode">
                  <select
                    class="select select-sm"
                    value={member.delivery_mode}
                    onchange={(e) => void onMemberModeChange(member.id, e)}
                    aria-label={t('mlistDetail.roster.memberModeAriaLabel')}
                  >
                    {#each modeFilterOptions.filter((o) => o.value !== '') as opt (opt.value)}
                      <option value={opt.value}>{t(opt.labelKey)}</option>
                    {/each}
                  </select>
                </td>
                <td class="col-added">{formatAbsolute(member.added_at, undefined)}</td>
                <td class="col-actions">
                  {#if removeConfirmId === member.id}
                    <div class="inline-confirm">
                      <span class="confirm-label">{t('mlistDetail.roster.removeConfirm')}</span>
                      <button type="button" class="btn-danger-sm" onclick={() => void confirmRemove(member.id)}>
                        {t('mlistDetail.roster.confirm')}
                      </button>
                      <button type="button" class="btn-ghost-sm" onclick={() => { removeConfirmId = null; }}>
                        {t('common.cancel')}
                      </button>
                    </div>
                  {:else}
                    <button type="button" class="btn-ghost-sm" onclick={() => { removeConfirmId = member.id; memberRowError = null; }}>
                      {t('mlistDetail.roster.remove')}
                    </button>
                  {/if}
                </td>
              </tr>
            {:else}
              <tr>
                <td colspan="5" class="empty-row">{t('mlistDetail.roster.empty')}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      {#if mlistDetail.membersHasMore}
        <div class="load-more">
          <button
            type="button"
            class="btn-secondary"
            onclick={() => void mlistDetail.loadMoreMembers(id)}
            disabled={mlistDetail.membersStatus === 'loading'}
          >
            {mlistDetail.membersStatus === 'loading' ? t('common.loading') : t('mlistDetail.roster.loadMore')}
          </button>
        </div>
      {/if}
    </div>

    <!-- Delete list danger zone -->
    <div class="danger-zone">
      <h3 class="danger-title">{t('mlistDetail.danger.title')}</h3>
      <p class="danger-desc">{t('mlistDetail.danger.description')}</p>
      <form class="danger-form" onsubmit={deleteList} novalidate>
        <label for="ml-confirm" class="label">
          {t('mlistDetail.danger.confirmLabel')} <strong class="mono">{mlistDetail.list.posting_address}</strong>
        </label>
        <div class="input-row">
          <input
            id="ml-confirm"
            type="text"
            class="input input-mono"
            placeholder={mlistDetail.list.posting_address}
            bind:value={deleteConfirmAddress}
            disabled={deleteSubmitting}
            autocomplete="off"
          />
          <button
            type="submit"
            class="btn-danger"
            disabled={deleteSubmitting || deleteConfirmAddress !== mlistDetail.list.posting_address}
          >
            {deleteSubmitting ? t('mlistDetail.danger.deleting') : t('mlistDetail.danger.delete')}
          </button>
        </div>
        {#if deleteError}
          <p class="form-error" role="alert">{deleteError}</p>
        {/if}
      </form>
    </div>
  {/if}
</div>

<!-- Add member dialog -->
<Dialog bind:open={addOpen} title={t('mlistDetail.add.dialogTitle')} width="520px">
  <form class="add-form" onsubmit={handleAddMember} novalidate>
    <div class="field">
      <span class="label" id="am-target-label">{t('mlistDetail.add.targetLabel')}</span>
      <div class="target-mode-toggle" role="radiogroup" aria-labelledby="am-target-label">
        <button
          type="button"
          class="mode-btn"
          class:active={addMode === 'principal'}
          role="radio"
          aria-checked={addMode === 'principal'}
          onclick={() => setAddMode('principal')}
          disabled={addSubmitting}
        >
          {t('mlistDetail.add.modePrincipal')}
        </button>
        <button
          type="button"
          class="mode-btn"
          class:active={addMode === 'external'}
          role="radio"
          aria-checked={addMode === 'external'}
          onclick={() => setAddMode('external')}
          disabled={addSubmitting}
        >
          {t('mlistDetail.add.modeExternal')}
        </button>
      </div>

      {#if addMode === 'principal'}
        <div class="autocomplete-wrapper">
          <input
            id="am-principal"
            type="text"
            class="input"
            placeholder={t('mlistDetail.add.searchPlaceholder')}
            autocomplete="off"
            bind:value={addPrincipalSearch}
            oninput={onPrincipalInput}
            disabled={addSubmitting}
          />
          {#if principalSearching}
            <div class="ac-spinner" aria-label={t('mlistDetail.add.searching')}></div>
          {/if}
          {#if principalDropdownOpen && principalOptions.length > 0}
            <ul class="ac-dropdown" role="listbox" aria-label={t('mlistDetail.add.optionsAriaLabel')}>
              {#each principalOptions as opt (opt.id)}
                <li
                  class="ac-option"
                  role="option"
                  tabindex="0"
                  aria-selected={addPrincipalId === opt.id}
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
        {#if addPrincipalId}
          <p class="field-hint">
            {t('mlistDetail.add.selected', { email: addPrincipalEmail, id: addPrincipalId })}
          </p>
        {/if}
      {:else}
        <input
          id="am-external"
          type="email"
          class="input"
          placeholder={t('mlistDetail.add.externalPlaceholder')}
          autocomplete="off"
          bind:value={addExternalAddress}
          disabled={addSubmitting}
        />
      {/if}
    </div>

    {#if addError}
      <p class="form-error" role="alert">{addError}</p>
    {/if}

    <div class="form-actions">
      <button type="button" class="btn-secondary" onclick={() => { addOpen = false; }} disabled={addSubmitting}>
        {t('common.cancel')}
      </button>
      <button
        type="submit"
        class="btn-primary"
        disabled={addSubmitting ||
          (addMode === 'principal' ? !addPrincipalId : !isValidExternalAddress(addExternalAddress))}
      >
        {addSubmitting ? t('mlistDetail.add.adding') : t('mlistDetail.add.add')}
      </button>
    </div>
  </form>
</Dialog>

<!-- Bulk import dialog -->
<Dialog bind:open={importOpen} title={t('mlistDetail.import.dialogTitle')} width="560px">
  <form class="import-form" onsubmit={handleImport} novalidate>
    <div class="field">
      <label for="im-text" class="label">{t('mlistDetail.import.label')}</label>
      <p class="field-hint">{t('mlistDetail.import.hint')}</p>
      <textarea
        id="im-text"
        class="textarea"
        rows="8"
        placeholder={'someone@example.com\n#42'}
        bind:value={importText}
        disabled={importSubmitting}
      ></textarea>
    </div>

    {#if importError}
      <p class="form-error" role="alert">{importError}</p>
    {/if}
    {#if importResultSummary}
      <div class="import-summary">
        <p>{t('mlistDetail.import.result', { added: importResultSummary.added, skipped: importResultSummary.skipped })}</p>
        {#if importResultSummary.details.length > 0}
          <ul class="import-details">
            {#each importResultSummary.details as detail (detail)}
              <li>{detail}</li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}

    <div class="form-actions">
      <button type="button" class="btn-secondary" onclick={() => { importOpen = false; }} disabled={importSubmitting}>
        {t('common.close')}
      </button>
      <button type="submit" class="btn-primary" disabled={importSubmitting || !importText.trim()}>
        {importSubmitting ? t('mlistDetail.import.importing') : t('mlistDetail.import.submit')}
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
  }

  .page-subtitle {
    margin: var(--spacing-01) 0 0;
    color: var(--text-secondary);
    font-size: var(--type-code-01-size);
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
    margin-bottom: var(--spacing-05);
  }

  .dkim-warning {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-warning);
    margin-bottom: var(--spacing-05);
  }

  .section {
    margin-bottom: var(--spacing-08);
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-05);
    flex-wrap: wrap;
  }

  .section-actions {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .section-title {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .config-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 640px;
  }

  .field-row {
    display: flex;
    gap: var(--spacing-05);
    flex-wrap: wrap;
  }

  .field-row .field {
    flex: 1;
    min-width: 200px;
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

  .input, .textarea {
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
  .textarea {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    resize: vertical;
  }
  .input:focus, .textarea:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    border-color: var(--interactive);
  }
  .input:disabled, .textarea:disabled {
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

  .form-success {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-success);
    margin: 0;
  }

  .form-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-04);
    padding-top: var(--spacing-02);
  }

  .filter-bar {
    display: flex;
    gap: var(--spacing-04);
    margin-bottom: var(--spacing-04);
    flex-wrap: wrap;
  }

  .select {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    cursor: pointer;
  }
  .select-sm {
    min-height: unset;
    padding: 2px var(--spacing-02);
    font-size: var(--type-helper-text-01-size);
  }
  .select:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
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

  .col-address { width: 35%; }
  .col-state { width: 20%; }
  .col-mode { width: 20%; }
  .col-added { width: 15%; white-space: nowrap; }
  .col-actions { width: 10%; text-align: right; }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }

  .empty-row {
    padding: var(--spacing-07) var(--spacing-05) !important;
    text-align: center;
    color: var(--text-helper);
  }

  .load-more {
    margin-top: var(--spacing-05);
    text-align: center;
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

  .input-row {
    display: flex;
    gap: var(--spacing-04);
    align-items: center;
  }

  /* Add-member / alias-style target toggle */
  .add-form, .import-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }

  .target-mode-toggle {
    display: flex;
    gap: var(--spacing-02);
    margin-bottom: var(--spacing-02);
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

  .import-summary {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .import-details {
    margin: var(--spacing-02) 0 0;
    padding-left: var(--spacing-05);
    max-height: 160px;
    overflow-y: auto;
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
