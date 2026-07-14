<script lang="ts">
  /**
   * Contacts list view — browses, searches, and sorts the principal's contacts.
   *
   * Implements REQ-CONT-20..26, REQ-CONT-70..72:
   *   - Paged Contact/query + narrow Contact/get (virtualised via sentinel scroll).
   *   - Live search with 300 ms debounce.
   *   - Sort by displayName / created / updated; sort reflected in URL.
   *   - Address book scope selector (hidden with single book and no groups).
   *   - Group scope: groups shown alongside address books in the scope selector.
   *   - Group management: create, rename, delete groups inline.
   *   - Empty states for no-contacts and no-search-match.
   *   - Live updates from sync channel (Contact/changes).
   */

  import { onMount, onDestroy } from 'svelte';
  import { contactsListStore, type SortProp } from '../lib/contacts/list-store.svelte';
  import { deriveFallbackInitial } from '../lib/contacts/list-store.svelte';
  import { groupsStore } from '../lib/contacts/groups.svelte';
  import { router } from '../lib/router/router.svelte';
  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { confirm } from '../lib/dialog/confirm.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import ContactsIcon from '../lib/icons/ContactsIcon.svelte';
  import ContactAvatar from '../lib/contacts/ContactAvatar.svelte';
  import TrashIcon from '../lib/icons/TrashIcon.svelte';
  import CheckSquareIcon from '../lib/icons/CheckSquareIcon.svelte';
  import {
    buildExportArgs,
    triggerDownload,
    type ExportResponse,
    type UnrepresentableProperty,
  } from '../lib/contacts/vcard-import';
  import { shouldOfferWholeSet } from '../lib/list-selection/whole-set-selection';

  // ── URL sort param sync ───────────────────────────────────────────────────

  const VALID_SORTS: SortProp[] = ['displayName', 'created', 'updated'];

  function sortFromParam(): SortProp {
    const p = router.getParam('sort');
    return (VALID_SORTS as string[]).includes(p ?? '') ? (p as SortProp) : 'displayName';
  }

  // ── Keyboard nav ─────────────────────────────────────────────────────────

  let focusedIndex = $state(-1);

  function focusRow(index: number): void {
    const rows = document.querySelectorAll<HTMLButtonElement>('.contact-row');
    if (index < 0 || index >= rows.length) return;
    focusedIndex = index;
    rows[index]?.focus();
  }

  function handleListKeydown(e: KeyboardEvent): void {
    if (e.key === 'j' || e.key === 'ArrowDown') {
      e.preventDefault();
      focusRow(focusedIndex + 1);
    } else if (e.key === 'k' || e.key === 'ArrowUp') {
      e.preventDefault();
      focusRow(focusedIndex - 1);
    }
  }

  // ── Sentinel / infinite scroll ───────────────────────────────────────────

  let sentinelEl = $state<Element | null>(null);
  let observer: IntersectionObserver | null = null;

  function setupObserver(el: Element | null): void {
    observer?.disconnect();
    observer = null;
    if (!el) return;
    observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) {
          void contactsListStore.loadMore();
        }
      },
      { rootMargin: '200px' },
    );
    observer.observe(el);
  }

  $effect(() => {
    setupObserver(sentinelEl);
  });

  // ── Group management dialogs ─────────────────────────────────────────────

  let showCreateDialog = $state(false);
  let createGroupName = $state('');
  let createGroupBusy = $state(false);

  let showRenameDialog = $state(false);
  let renameGroupName = $state('');
  let renameGroupBusy = $state(false);

  let showDeleteDialog = $state(false);
  let deleteGroupBusy = $state(false);

  function openCreateDialog(): void {
    createGroupName = '';
    createGroupBusy = false;
    showCreateDialog = true;
    // Focus the input after DOM settles.
    setTimeout(() => {
      document.getElementById('create-group-name-input')?.focus();
    }, 0);
  }

  function closeCreateDialog(): void {
    showCreateDialog = false;
    createGroupName = '';
  }

  async function submitCreate(): Promise<void> {
    const name = createGroupName.trim();
    if (!name) return;
    const addressBookId = contactsListStore.addressBooks[0]?.id ?? '';
    if (!addressBookId) return;
    createGroupBusy = true;
    const newId = await groupsStore.createGroup(name, addressBookId);
    createGroupBusy = false;
    if (!newId) {
      toast.show({ message: t('contacts.groups.createError'), kind: 'error' });
      return;
    }
    closeCreateDialog();
    // Switch to the new group immediately.
    const newGroup = groupsStore.getGroup(newId);
    if (newGroup) {
      contactsListStore.setGroup(newId, Object.keys(newGroup.members));
    }
  }

  function openRenameDialog(): void {
    const group = groupsStore.getGroup(contactsListStore.activeGroupId ?? '');
    if (!group) return;
    renameGroupName = group.name;
    renameGroupBusy = false;
    showRenameDialog = true;
    setTimeout(() => {
      document.getElementById('rename-group-name-input')?.focus();
    }, 0);
  }

  function closeRenameDialog(): void {
    showRenameDialog = false;
    renameGroupName = '';
  }

  async function submitRename(): Promise<void> {
    const groupId = contactsListStore.activeGroupId;
    if (!groupId) return;
    const name = renameGroupName.trim();
    if (!name) return;
    renameGroupBusy = true;
    const ok = await groupsStore.renameGroup(groupId, name);
    renameGroupBusy = false;
    if (!ok) {
      toast.show({ message: t('contacts.groups.renameError'), kind: 'error' });
    }
    closeRenameDialog();
  }

  function openDeleteDialog(): void {
    deleteGroupBusy = false;
    showDeleteDialog = true;
  }

  function closeDeleteDialog(): void {
    showDeleteDialog = false;
  }

  async function submitDelete(): Promise<void> {
    const groupId = contactsListStore.activeGroupId;
    if (!groupId) return;
    deleteGroupBusy = true;
    const ok = await groupsStore.deleteGroup(groupId);
    deleteGroupBusy = false;
    if (!ok) {
      toast.show({ message: t('contacts.groups.deleteError'), kind: 'error' });
      closeDeleteDialog();
      return;
    }
    closeDeleteDialog();
    // Return to "all contacts" view.
    contactsListStore.setGroup(null, null);
  }

  // ── Scope selector ───────────────────────────────────────────────────────

  let scopeValue = $derived(
    contactsListStore.activeGroupId
      ? `group:${contactsListStore.activeGroupId}`
      : contactsListStore.activeBookId
        ? `book:${contactsListStore.activeBookId}`
        : '',
  );

  function handleScopeChange(e: Event): void {
    const select = e.target as HTMLSelectElement;
    const val = select.value;
    if (val === '') {
      contactsListStore.setAddressBook(null);
      contactsListStore.setGroup(null, null);
    } else if (val.startsWith('book:')) {
      contactsListStore.setAddressBook(val.slice(5));
    } else if (val.startsWith('group:')) {
      const groupId = val.slice(6);
      const group = groupsStore.getGroup(groupId);
      contactsListStore.setGroup(groupId, group ? Object.keys(group.members) : []);
    }
  }

  // ── Lifecycle ─────────────────────────────────────────────────────────────

  onMount(() => {
    const urlSort = sortFromParam();
    contactsListStore.sort = urlSort;
    void groupsStore.load().then(() => {
      void contactsListStore.init();
    });
  });

  onDestroy(() => {
    observer?.disconnect();
    observer = null;
    contactsListStore.destroy();
    groupsStore.destroy();
  });

  // ── Handlers ─────────────────────────────────────────────────────────────

  function handleSearchInput(e: Event): void {
    const input = e.target as HTMLInputElement;
    contactsListStore.setSearch(input.value);
  }

  function handleSortChange(e: Event): void {
    const select = e.target as HTMLSelectElement;
    const val = select.value as SortProp;
    contactsListStore.setSort(val);
    router.setParam('sort', val === 'displayName' ? null : val);
  }

  function clearSearch(): void {
    contactsListStore.setSearch('');
    contactsListStore.searchText = '';
  }

  function openContact(id: string): void {
    router.navigate(`/contacts/${encodeURIComponent(id)}`);
  }

  // ── Derived ───────────────────────────────────────────────────────────────

  // Capability check (REQ-CONT-03).
  let hasContactsCap = $derived(
    auth.status === 'ready' && jmap.hasCapability(Capability.Contacts),
  );

  // Show scope selector when there are multiple address books or any groups.
  let showScopeSelector = $derived(
    contactsListStore.addressBooks.length > 1 || groupsStore.groups.length > 0,
  );

  let isSearching = $derived(contactsListStore.searchText.trim().length > 0);

  // Filter group cards out of the normal "all contacts" view so they don't
  // appear as regular rows. In group mode, rows are already member contacts.
  let visibleRows = $derived(
    contactsListStore.activeGroupId !== null
      ? contactsListStore.rows
      : contactsListStore.rows.filter((r) => !groupsStore.groupIds.has(r.id)),
  );

  let isEmpty = $derived(
    contactsListStore.status === 'ready' && visibleRows.length === 0,
  );

  // The group currently selected in the scope selector.
  let activeGroup = $derived(
    contactsListStore.activeGroupId
      ? groupsStore.getGroup(contactsListStore.activeGroupId)
      : undefined,
  );

  let activeGroupForDelete = $derived(activeGroup?.name ?? '');

  // ── Export state (REQ-CONT-81, 83) ───────────────────────────────────────

  let exporting = $state(false);
  let exportWarnings = $state<UnrepresentableProperty[]>([]);
  let showExportWarnings = $state(false);

  /**
   * Export contacts as `.vcf`. With no `ids`, exports the current
   * address-book/group scope (REQ-CONT-81); with `ids`, exports just that
   * selection (bulk export, re #191) regardless of scope.
   */
  async function exportContacts(ids?: string[]): Promise<void> {
    const accountId =
      auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) return;

    exporting = true;
    exportWarnings = [];
    showExportWarnings = false;

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/export',
          ids
            ? buildExportArgs(accountId, { ids })
            : buildExportArgs(accountId, {
                addressBookId: contactsListStore.activeBookId ?? undefined,
              }),
          [Capability.Contacts],
        );
      });

      const resp = responses[0];
      if (!resp || resp[0] === 'error') {
        toast.show({ message: t(ids ? 'contacts.bulk.exportError' : 'contacts.export.error'), kind: 'error' });
        return;
      }

      const args = resp[1] as ExportResponse;
      const url = jmap.downloadUrl({
        accountId,
        blobId: args.blobId,
        type: 'text/vcard',
        name: 'contacts.vcf',
      });
      if (url) {
        triggerDownload(url, 'contacts.vcf');
      }

      if ((args.unrepresentable?.length ?? 0) > 0) {
        exportWarnings = args.unrepresentable ?? [];
        showExportWarnings = true;
      }
    } catch {
      toast.show({ message: t(ids ? 'contacts.bulk.exportError' : 'contacts.export.error'), kind: 'error' });
    } finally {
      exporting = false;
    }
  }

  // ── Bulk selection & actions (re #191) ───────────────────────────────────

  let selectedCount = $derived(contactsListStore.selectedIds.size);
  let visibleRowIds = $derived(visibleRows.map((r) => r.id));
  let allVisibleSelected = $derived(
    visibleRows.length > 0 && selectedCount === visibleRows.length,
  );
  let someVisibleSelected = $derived(selectedCount > 0 && !allVisibleSelected);
  let bulkDeleting = $state(false);

  /**
   * Offer "select all N matching" once every loaded row is checked and the
   * true total exceeds the loaded count (re #221, mirrors the mail store's
   * whole-mailbox banner, issue #149). Group scope has no further pages
   * beyond its member list, so `contactsListStore.total` there already
   * equals `visibleRows.length` and the banner naturally never offers.
   */
  let offerSelectAllMatching = $derived(
    !contactsListStore.wholeSetSelected &&
      shouldOfferWholeSet(visibleRowIds, contactsListStore.selectedIds, contactsListStore.total),
  );

  function toggleSelectAllVisible(): void {
    contactsListStore.toggleSelectAllVisible(visibleRowIds);
  }

  async function bulkDeleteSelected(): Promise<void> {
    const ids = await contactsListStore.resolveSelectionIds();
    if (ids.length === 0) return;
    const n = ids.length;
    const ok = await confirm.ask({
      title: t(n === 1 ? 'contacts.bulk.deleteConfirm.titleOne' : 'contacts.bulk.deleteConfirm.titleMany', {
        n: String(n),
      }),
      message: t(n === 1 ? 'contacts.bulk.deleteConfirm.messageOne' : 'contacts.bulk.deleteConfirm.messageMany', {
        n: String(n),
      }),
      confirmLabel: t('contacts.bulk.deleteConfirm.confirm'),
      cancelLabel: t('contacts.bulk.deleteConfirm.cancel'),
      kind: 'danger',
    });
    if (!ok) return;

    bulkDeleting = true;
    try {
      const failed = await contactsListStore.bulkDelete(ids);
      if (failed.length > 0) {
        toast.show({ message: t('contacts.bulk.deleteError', { n: String(failed.length) }), kind: 'error' });
      }
    } finally {
      bulkDeleting = false;
    }
  }

  async function bulkExportSelected(): Promise<void> {
    const ids = await contactsListStore.resolveSelectionIds();
    if (ids.length === 0) return;
    await exportContacts(ids);
  }
</script>

<!-- Group create dialog -->
{#if showCreateDialog}
  <div class="overlay" role="dialog" aria-modal="true" aria-label={t('contacts.groups.createDialog.title')}>
    <div class="dialog">
      <h2 class="dialog-title">{t('contacts.groups.createDialog.title')}</h2>
      <label class="dialog-label" for="create-group-name-input">
        {t('contacts.groups.createDialog.label')}
      </label>
      <input
        id="create-group-name-input"
        class="dialog-input"
        type="text"
        placeholder={t('contacts.groups.createDialog.placeholder')}
        bind:value={createGroupName}
        onkeydown={(e) => { if (e.key === 'Enter') void submitCreate(); if (e.key === 'Escape') closeCreateDialog(); }}
        autocomplete="off"
      />
      <div class="dialog-actions">
        <button
          type="button"
          class="dialog-btn primary"
          onclick={() => void submitCreate()}
          disabled={createGroupBusy || !createGroupName.trim()}
        >
          {t('contacts.groups.createDialog.confirm')}
        </button>
        <button type="button" class="dialog-btn secondary" onclick={closeCreateDialog} disabled={createGroupBusy}>
          {t('contacts.groups.createDialog.cancel')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Group rename dialog -->
{#if showRenameDialog}
  <div class="overlay" role="dialog" aria-modal="true" aria-label={t('contacts.groups.renameDialog.title')}>
    <div class="dialog">
      <h2 class="dialog-title">{t('contacts.groups.renameDialog.title')}</h2>
      <label class="dialog-label" for="rename-group-name-input">
        {t('contacts.groups.renameDialog.label')}
      </label>
      <input
        id="rename-group-name-input"
        class="dialog-input"
        type="text"
        bind:value={renameGroupName}
        onkeydown={(e) => { if (e.key === 'Enter') void submitRename(); if (e.key === 'Escape') closeRenameDialog(); }}
        autocomplete="off"
      />
      <div class="dialog-actions">
        <button
          type="button"
          class="dialog-btn primary"
          onclick={() => void submitRename()}
          disabled={renameGroupBusy || !renameGroupName.trim()}
        >
          {t('contacts.groups.renameDialog.confirm')}
        </button>
        <button type="button" class="dialog-btn secondary" onclick={closeRenameDialog} disabled={renameGroupBusy}>
          {t('contacts.groups.renameDialog.cancel')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Group delete confirmation -->
{#if showDeleteDialog}
  <div class="overlay" role="dialog" aria-modal="true" aria-label={t('contacts.groups.deleteDialog.title')}>
    <div class="dialog">
      <h2 class="dialog-title">{t('contacts.groups.deleteDialog.title')}</h2>
      <p class="dialog-msg">
        {t('contacts.groups.deleteDialog.message', { name: activeGroupForDelete })}
      </p>
      <div class="dialog-actions">
        <button
          type="button"
          class="dialog-btn danger"
          onclick={() => void submitDelete()}
          disabled={deleteGroupBusy}
        >
          {t('contacts.groups.deleteDialog.confirm')}
        </button>
        <button type="button" class="dialog-btn secondary" onclick={closeDeleteDialog} disabled={deleteGroupBusy}>
          {t('contacts.groups.deleteDialog.cancel')}
        </button>
      </div>
    </div>
  </div>
{/if}

<div class="contacts-list-view" role="main" aria-label={t('contacts.list.ariaLabel')}>
  {#if !hasContactsCap && auth.status === 'ready'}
    <!-- REQ-CONT-03: contacts capability absent — show unavailable state -->
    <div class="unavailable" role="status">
      <ContactsIcon size={48} />
      <p class="unavailable-msg">{t('contacts.unavailable')}</p>
    </div>
  {:else}
    <!-- Header: search + controls -->
    <div class="list-header">
      <div class="search-row">
        <label class="sr-only" for="contacts-search">{t('contacts.list.searchLabel')}</label>
        <input
          id="contacts-search"
          class="search-input"
          type="search"
          placeholder={t('contacts.list.searchPlaceholder')}
          value={contactsListStore.searchText}
          oninput={handleSearchInput}
          autocomplete="off"
          spellcheck={false}
        />
        <div class="header-controls">
          {#if showScopeSelector}
            <label class="sr-only" for="contacts-scope">{t('contacts.list.bookLabel')}</label>
              <select
              id="contacts-scope"
              class="filter-select"
              value={scopeValue}
              onchange={handleScopeChange}
              aria-label={t('contacts.list.bookLabel')}
            >
              <option value="">{t('contacts.list.allContacts')}</option>
              {#if contactsListStore.addressBooks.length > 1}
                <optgroup label={t('contacts.list.scopeBooks')}>
                  {#each contactsListStore.addressBooks as book (book.id)}
                    <option value={`book:${book.id}`}>{book.name}</option>
                  {/each}
                </optgroup>
              {/if}
              {#if groupsStore.groups.length > 0}
                <optgroup label={t('contacts.list.scopeGroups')}>
                  {#each groupsStore.groups as group (group.id)}
                    <option value={`group:${group.id}`}>{group.name}</option>
                  {/each}
                </optgroup>
              {/if}
            </select>
          {/if}
          <label class="sr-only" for="contacts-sort">{t('contacts.list.sortLabel')}</label>
          <select
            id="contacts-sort"
            class="filter-select"
            value={contactsListStore.sort}
            onchange={handleSortChange}
            aria-label={t('contacts.list.sortLabel')}
          >
            <option value="displayName">{t('contacts.list.sort.displayName')}</option>
            <option value="created">{t('contacts.list.sort.created')}</option>
            <option value="updated">{t('contacts.list.sort.updated')}</option>
          </select>
          <button
            type="button"
            class="new-group-btn"
            onclick={openCreateDialog}
            aria-label={t('contacts.list.newGroup')}
            title={t('contacts.list.newGroup')}
          >
            {t('contacts.list.newGroup')}
          </button>
          <button
            type="button"
            class="duplicates-btn"
            onclick={() => router.navigate('/contacts/duplicates')}
            title={t('contacts.list.duplicates')}
          >
            {t('contacts.list.duplicates')}
          </button>
          <button
            type="button"
            class="import-btn"
            onclick={() => router.navigate('/contacts/import')}
            title={t('contacts.list.import')}
          >
            {t('contacts.list.import')}
          </button>
          <button
            type="button"
            class="export-btn"
            onclick={() => void exportContacts()}
            disabled={exporting}
            title={t('contacts.list.export')}
          >
            {exporting ? t('contacts.export.exporting') : t('contacts.list.export')}
          </button>
        </div>
      </div>

      <!-- Export warnings dialog (REQ-CONT-83) -->
      {#if showExportWarnings}
        <div class="overlay" role="dialog" aria-modal="true" aria-label={t('contacts.export.warnings.heading')}>
          <div class="dialog">
            <h2 class="dialog-title">{t('contacts.export.warnings.heading')}</h2>
            <p class="dialog-msg">
              {exportWarnings.length === 1
                ? t('contacts.export.warnings.text', { n: exportWarnings.length })
                : t('contacts.export.warnings.text_many', { n: exportWarnings.length })}
            </p>
            <ul class="warnings-list">
              {#each exportWarnings as w, i (i)}
                <li class="warning-item">
                  <span class="warning-type">{w.type}</span>
                  {#if w.detail}
                    <span class="warning-detail">{w.detail}</span>
                  {/if}
                </li>
              {/each}
            </ul>
            <div class="dialog-actions">
              <button
                type="button"
                class="dialog-btn primary"
                onclick={() => { showExportWarnings = false; }}
              >
                {t('contacts.export.warnings.dismiss')}
              </button>
            </div>
          </div>
        </div>
      {/if}

      <!-- Group management toolbar (visible when a group is selected) -->
      {#if contactsListStore.activeGroupId && activeGroup}
        <div class="group-toolbar" role="toolbar" aria-label={activeGroup.name}>
          <span class="group-toolbar-name">{activeGroup.name}</span>
          <button
            type="button"
            class="group-action-btn"
            onclick={openRenameDialog}
          >
            {t('contacts.list.renameGroup')}
          </button>
          <button
            type="button"
            class="group-action-btn danger"
            onclick={openDeleteDialog}
          >
            {t('contacts.list.deleteGroup')}
          </button>
        </div>
      {/if}
    </div>

    <!-- List body -->
    {#if contactsListStore.status === 'loading'}
      <div class="state-msg" role="status" aria-live="polite">
        <span class="loading-spinner" aria-hidden="true"></span>
        {t('contacts.list.loading')}
      </div>
    {:else if contactsListStore.status === 'error'}
      <div class="state-msg error" role="alert">
        {t('contacts.list.loadError')}
      </div>
    {:else if isEmpty && isSearching}
      <!-- Search empty state (REQ-CONT-25) -->
      <div class="empty-state" role="status">
        <p class="empty-msg">
          {t('contacts.list.noMatch', { query: contactsListStore.searchText.trim() })}
        </p>
        <div class="empty-actions">
          <button
            type="button"
            class="empty-action-btn secondary"
            onclick={clearSearch}
          >
            {t('contacts.list.clearSearch')}
          </button>
          <button
            type="button"
            class="empty-action-btn primary"
            onclick={() => router.navigate('/contacts/new')}
          >
            {t('contacts.list.createFromSearch', { query: contactsListStore.searchText.trim() })}
          </button>
        </div>
      </div>
    {:else if isEmpty}
      <!-- No-contacts empty state (REQ-CONT-25) -->
      <div class="empty-state" role="status">
        <ContactsIcon size={48} />
        <p class="empty-msg">{t('contacts.list.empty')}</p>
        <div class="empty-actions">
          <button
            type="button"
            class="empty-action-btn primary"
            onclick={() => router.navigate('/contacts/new')}
          >
            {t('contacts.list.addContact')}
          </button>
          <button
            type="button"
            class="empty-action-btn secondary"
            onclick={() => router.navigate('/contacts/import')}
          >
            {t('contacts.list.import')}
          </button>
        </div>
      </div>
    {:else}
      <!-- Bulk-selection toolbar (re #191): select-all control plus, once at
           least one contact is selected, the count and bulk actions. -->
      <div class="bulk-toolbar" role="toolbar" aria-label={t('bulk.selected', { count: selectedCount })}>
        <button
          type="button"
          class="select-all-btn"
          aria-label={allVisibleSelected ? t('select.deselectAll') : t('select.selectAll')}
          aria-pressed={allVisibleSelected}
          title={allVisibleSelected ? t('select.deselectAll') : t('select.selectAll')}
          onclick={toggleSelectAllVisible}
        >
          <CheckSquareIcon size={18} checked={allVisibleSelected} indeterminate={someVisibleSelected} />
        </button>
        {#if selectedCount > 0}
          <span class="bulk-count">{t('bulk.selected', { count: selectedCount })}</span>
          <button
            type="button"
            class="icon-btn"
            aria-label={t('contacts.list.export')}
            title={t('contacts.list.export')}
            onclick={() => void bulkExportSelected()}
            disabled={exporting}
          >
            {t('contacts.list.export')}
          </button>
          <button
            type="button"
            class="icon-btn danger"
            aria-label={t('bulk.delete')}
            title={t('bulk.delete')}
            onclick={() => void bulkDeleteSelected()}
            disabled={bulkDeleting}
          >
            <TrashIcon size={16} />
            {t('bulk.delete')}
          </button>
        {/if}
      </div>

      <!-- Select-all-N-matching banner (re #221, mirrors the mail store's
           whole-mailbox banner, issue #149). Offer state: every loaded row
           is selected and more contacts match the current scope than are
           loaded. Active state: whole-set mode is engaged; bulk delete /
           export resolve to the full filter-scoped id set via
           `resolveSelectionIds`, not just the loaded window. -->
      {#if selectedCount > 0 && contactsListStore.total !== null && visibleRows.length > 0}
        {@const total = contactsListStore.total}
        {#if offerSelectAllMatching}
          <div class="whole-set-banner" role="status" aria-live="polite">
            <span class="banner-text">
              {t('contacts.select.allPageSelected', { count: String(visibleRows.length) })}
            </span>
            <button
              type="button"
              class="banner-btn"
              onclick={() => contactsListStore.selectAllMatching()}
            >
              {t('contacts.select.selectAllMatching', { total: String(total) })}
            </button>
          </div>
        {:else if contactsListStore.wholeSetSelected}
          <div class="whole-set-banner whole-set-banner--active" role="status" aria-live="polite">
            <span class="banner-text">
              {t('contacts.select.wholeSetActive', { total: String(total) })}
            </span>
            <button
              type="button"
              class="banner-btn banner-btn--secondary"
              onclick={() => contactsListStore.selectAllVisible(visibleRowIds)}
            >
              {t('contacts.select.clearWholeSet')}
            </button>
          </div>
        {/if}
      {/if}

      <!-- Contact rows -->
      <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
      <ul
        class="contact-list"
        role="listbox"
        aria-label={t('contacts.list.ariaLabel')}
        onkeydown={handleListKeydown}
      >
        {#each visibleRows as row, i (row.id)}
          <li role="option" aria-selected={contactsListStore.selectedIds.has(row.id)} class:selected={contactsListStore.selectedIds.has(row.id)}>
            <input
              type="checkbox"
              class="row-check"
              aria-label={t('contacts.list.selectRowAria')}
              checked={contactsListStore.selectedIds.has(row.id)}
              onclick={(e) => {
                e.stopPropagation();
                if (e.shiftKey) {
                  // Shift-click replaces native single-row toggling with a
                  // range select (re #202); suppress the native toggle (and
                  // the `change` event it would fire) so the plain-click
                  // path below stays untouched.
                  e.preventDefault();
                  contactsListStore.selectRowClick(
                    row.id,
                    true,
                    visibleRows.map((r) => r.id),
                  );
                }
              }}
              onchange={() => contactsListStore.toggleSelected(row.id)}
            />
            <button
              type="button"
              class="contact-row"
              aria-label={row.displayName || row.secondary}
              onclick={() => openContact(row.id)}
              onfocus={() => { focusedIndex = i; }}
            >
              <ContactAvatar
                blobId={row.photoBlobId}
                fallbackInitial={deriveFallbackInitial(row.displayName || row.secondary)}
                size={36}
              />
              <span class="row-text">
                <span class="row-name">
                  {row.displayName || row.secondary || t('contacts.list.unnamed')}
                </span>
                {#if row.secondary && row.secondary !== row.displayName}
                  <span class="row-secondary">{row.secondary}</span>
                {/if}
              </span>
            </button>
          </li>
        {/each}
      </ul>

      <!-- Sentinel for infinite scroll (REQ-CONT-21) — not shown in group mode -->
      {#if contactsListStore.hasMore && contactsListStore.activeGroupId === null}
        <div
          class="sentinel"
          bind:this={sentinelEl}
          aria-hidden="true"
        >
          {#if contactsListStore.status === 'loading-more'}
            <span class="loading-spinner" aria-hidden="true"></span>
            <span class="sr-only">{t('contacts.list.loadingMore')}</span>
          {/if}
        </div>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .contacts-list-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    background: var(--background);
  }

  /* ── Header ─────────────────────────────────────────────────────────── */

  .list-header {
    flex-shrink: 0;
    padding: var(--spacing-04);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }

  .search-row {
    display: flex;
    gap: var(--spacing-03);
    align-items: center;
    flex-wrap: wrap;
  }

  .search-input {
    flex: 1 1 180px;
    min-width: 0;
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
  }

  .search-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .header-controls {
    display: flex;
    gap: var(--spacing-02);
    align-items: center;
    flex-shrink: 0;
    flex-wrap: wrap;
  }

  .filter-select {
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
  }

  .filter-select:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .new-group-btn {
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    white-space: nowrap;
  }

  .new-group-btn:hover {
    background: var(--layer-02);
  }

  .new-group-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .duplicates-btn {
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    white-space: nowrap;
  }

  .duplicates-btn:hover {
    background: var(--layer-02);
  }

  .duplicates-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .import-btn {
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    white-space: nowrap;
  }

  .import-btn:hover {
    background: var(--layer-02);
  }

  .import-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .export-btn {
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    white-space: nowrap;
  }

  .export-btn:hover:not(:disabled) {
    background: var(--layer-02);
  }

  .export-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .export-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .warnings-list {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 200px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .warning-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-02);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
  }

  .warning-type {
    font-weight: 600;
    color: var(--text-primary);
  }

  .warning-detail {
    color: var(--text-secondary);
  }

  /* ── Group toolbar ───────────────────────────────────────────────────── */

  .group-toolbar {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding-top: var(--spacing-03);
    flex-wrap: wrap;
  }

  .group-toolbar-name {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .group-action-btn {
    height: 28px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: transparent;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .group-action-btn:hover {
    background: var(--layer-02);
  }

  .group-action-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .group-action-btn.danger {
    color: var(--support-error);
    border-color: var(--support-error);
  }

  .group-action-btn.danger:hover {
    background: var(--support-error-bg, rgba(218, 30, 40, 0.1));
  }

  /* ── Dialogs ─────────────────────────────────────────────────────────── */

  .overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 200;
  }

  .dialog {
    background: var(--layer-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-06);
    max-width: 360px;
    width: 100%;
    box-shadow: var(--shadow-lg);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .dialog-title {
    font-size: var(--type-heading-03-size);
    margin: 0;
    color: var(--text-primary);
  }

  .dialog-label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    display: block;
  }

  .dialog-input {
    width: 100%;
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    box-sizing: border-box;
  }

  .dialog-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .dialog-msg {
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .dialog-actions {
    display: flex;
    gap: var(--spacing-03);
  }

  .dialog-btn {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    cursor: pointer;
    border: 1px solid transparent;
    min-height: var(--touch-min);
  }

  .dialog-btn.primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border-color: var(--interactive);
  }

  .dialog-btn.primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .dialog-btn.secondary {
    background: transparent;
    color: var(--text-primary);
    border-color: var(--border-subtle-01);
  }

  .dialog-btn.secondary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .dialog-btn.danger {
    background: var(--support-error);
    color: #fff;
    border-color: var(--support-error);
  }

  .dialog-btn.danger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  /* ── List ────────────────────────────────────────────────────────────── */

  .contact-list {
    flex: 1 1 auto;
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .contact-list li {
    display: flex;
    align-items: center;
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .contact-list li.selected {
    background: var(--layer-02);
  }

  .row-check {
    flex-shrink: 0;
    margin: 0 0 0 var(--spacing-04);
    width: 16px;
    height: 16px;
    accent-color: var(--interactive);
    cursor: pointer;
  }

  .contact-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    width: 100%;
    padding: var(--spacing-03) var(--spacing-04);
    min-height: var(--touch-min);
    text-align: left;
    transition: background var(--duration-fast-01) var(--easing-productive-enter);
    background: transparent;
  }

  .contact-row:hover {
    background: var(--layer-02);
  }

  .contact-row:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
    background: var(--layer-03);
  }

  /* ── Bulk-selection toolbar (re #191) ─────────────────────────────────── */

  .bulk-toolbar {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }

  .select-all-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    color: var(--text-secondary);
    background: transparent;
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .select-all-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .bulk-count {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .bulk-toolbar .icon-btn {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    height: 32px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    white-space: nowrap;
  }

  .bulk-toolbar .icon-btn:hover:not(:disabled) {
    background: var(--layer-02);
  }

  .bulk-toolbar .icon-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .bulk-toolbar .icon-btn.danger {
    color: var(--support-error);
    border-color: var(--support-error);
  }

  .bulk-toolbar .icon-btn.danger:hover:not(:disabled) {
    background: var(--support-error-bg, rgba(218, 30, 40, 0.1));
  }

  /* ── Select-all-N-matching banner (re #221) ─────────────────────────── */

  .whole-set-banner {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    flex-shrink: 0;
  }

  .whole-set-banner--active {
    background: var(--layer-03);
  }

  .whole-set-banner .banner-text {
    flex: 1;
  }

  .whole-set-banner .banner-btn {
    white-space: nowrap;
    color: var(--interactive);
    background: transparent;
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    padding: var(--spacing-01) var(--spacing-02);
    border-radius: var(--radius-sm);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .whole-set-banner .banner-btn:hover {
    background: var(--layer-02);
    color: var(--interactive-hover);
    text-decoration: underline;
  }

  .whole-set-banner .banner-btn--secondary {
    color: var(--text-secondary);
    font-weight: 400;
  }

  .whole-set-banner .banner-btn--secondary:hover {
    color: var(--text-primary);
  }

  .row-text {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .row-name {
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .row-secondary {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* ── Sentinel ────────────────────────────────────────────────────────── */

  .sentinel {
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    padding: var(--spacing-03);
  }

  /* ── States ─────────────────────────────────────────────────────────── */

  .state-msg {
    padding: var(--spacing-06);
    text-align: center;
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    display: flex;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-03);
  }

  .state-msg.error {
    color: var(--support-error);
  }

  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-04);
    padding: var(--spacing-08);
    flex: 1 1 auto;
    color: var(--text-secondary);
  }

  .empty-msg {
    font-size: var(--type-body-01-size);
    text-align: center;
    color: var(--text-secondary);
    margin: 0;
  }

  .empty-actions {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
    justify-content: center;
  }

  .empty-action-btn {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .empty-action-btn.primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .empty-action-btn.primary:hover {
    filter: brightness(1.1);
  }

  .empty-action-btn.secondary {
    border: 1px solid var(--border-subtle-01);
    background: transparent;
    color: var(--text-primary);
  }

  .empty-action-btn.secondary:hover {
    background: var(--layer-02);
  }

  /* ── Unavailable (no capability) ─────────────────────────────────────── */

  .unavailable {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--spacing-04);
    padding: var(--spacing-08);
    flex: 1 1 auto;
    color: var(--text-secondary);
  }

  .unavailable-msg {
    font-size: var(--type-body-01-size);
    text-align: center;
    color: var(--text-secondary);
    margin: 0;
  }

  /* ── Loading spinner ─────────────────────────────────────────────────── */

  .loading-spinner {
    display: inline-block;
    width: 16px;
    height: 16px;
    border: 2px solid var(--border-subtle-01);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 0.7s linear infinite;
    flex-shrink: 0;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  /* ── A11y ────────────────────────────────────────────────────────────── */

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
</style>
