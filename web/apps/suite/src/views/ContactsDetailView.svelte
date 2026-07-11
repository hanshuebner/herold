<script lang="ts">
  /**
   * Contact detail view — renders every populated section of a JSContact
   * Card fetched via Contact/get (REQ-CONT-30..34).
   *
   * Sections rendered (empty sections omitted per REQ-CONT-30):
   *   - Monogram avatar + display name + nicknames
   *   - Organizations + titles
   *   - Emails (actionable: compose)
   *   - Phones (actionable: tel:)
   *   - Addresses (actionable: map link)
   *   - Links / URLs (actionable: new tab)
   *   - Notes
   *   - Anniversaries (birthday, wedding, etc.)
   *
   * Actions: Edit, Delete (optimistic with confirmation), Export (stub),
   *          Merge duplicates (REQ-CONT-33), "All mail with this person" (REQ-CONT-31..33).
   */

  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { contacts } from '../lib/contacts/store.svelte';
  import { contactsListStore } from '../lib/contacts/list-store.svelte';
  import { groupsStore } from '../lib/contacts/groups.svelte';
  import { router } from '../lib/router/router.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import ContactAvatar from '../lib/contacts/ContactAvatar.svelte';
  import {
    cardToVM,
    vmDisplayName,
    vmMonogram,
    extractPhotoFromCard,
    formatAddress,
    formatAnniversary,
    type ContactEditVM,
  } from '../lib/contacts/jscontact';
  import {
    extractCandidate,
    clusterDuplicates,
    loadDismissedPairs,
  } from '../lib/contacts/dedup';

  // The contact id is the second route segment: /contacts/<id>.
  let contactId = $derived(decodeURIComponent(router.parts[1] ?? ''));

  let loadStatus = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  let raw = $state<Record<string, unknown> | null>(null);
  let vm = $state<ContactEditVM | null>(null);
  let notFound = $state(false);
  let deleting = $state(false);
  let showDeleteConfirm = $state(false);

  // ── Duplicate candidate IDs for this contact (REQ-CONT-33) ─────────────────
  // Set after the contact loads; used to show/hide the "Merge duplicates" button.
  let duplicateCandidateIds = $state<string[]>([]);

  // Derived display values.
  let displayName = $derived(vm ? vmDisplayName(vm) : '');
  let monogram = $derived(vm ? vmMonogram(vm) : '?');
  let photoBlobId = $derived(raw ? (extractPhotoFromCard(raw)?.blobId ?? null) : null);
  // Pre-computed filtered lists to avoid TypeScript narrowing issues inside callbacks.
  let standaloneOrgs = $derived(
    vm ? (vm.orgs.length === 0 ? vm.titles.filter((tt) => !tt.orgId) : []) : []
  );

  $effect(() => {
    const id = contactId;
    if (!id) return;
    void loadContact(id);
  });

  async function loadContact(id: string): Promise<void> {
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) {
      loadStatus = 'error';
      return;
    }
    loadStatus = 'loading';
    raw = null;
    vm = null;
    notFound = false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Contact/get', { accountId, ids: [id] }, [Capability.Contacts]);
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') {
        loadStatus = 'error';
        return;
      }
      const args = resp[1] as { list: unknown[]; notFound?: string[] };
      const rawCard = args.list[0];
      if (!rawCard || typeof rawCard !== 'object') {
        notFound = true;
        loadStatus = 'error';
        return;
      }
      raw = rawCard as Record<string, unknown>;
      vm = cardToVM(raw);
      loadStatus = 'ready';
      // Non-blocking: detect duplicate candidates for this contact.
      void detectDuplicatesForContact(id, accountId, rawCard as Record<string, unknown>);
    } catch {
      loadStatus = 'error';
    }
  }

  /**
   * After loading the contact, do a cheap duplicate check using the text filter
   * over its primary email / display name. If any other contacts match, expose
   * the "Merge duplicates" entry point (REQ-CONT-33).
   */
  async function detectDuplicatesForContact(
    id: string,
    accountId: string,
    card: Record<string, unknown>,
  ): Promise<void> {
    duplicateCandidateIds = [];
    const candidate = extractCandidate(card);
    // Build a set of query texts: primary email and display name.
    const queryTexts = new Set<string>();
    if (candidate.emails.length > 0) queryTexts.add(candidate.emails[0]!);
    if (candidate.displayName.trim()) queryTexts.add(candidate.displayName.trim());
    if (queryTexts.size === 0) return;

    const principalId = auth.principalId ?? 'unknown';
    const dismissed = loadDismissedPairs(principalId);

    // Run a Contact/query text filter for each query text and collect IDs.
    const allCandidates: Record<string, unknown>[] = [card]; // include self
    for (const text of queryTexts) {
      try {
        const { responses } = await jmap.batch((b) => {
          const q = b.call(
            'Contact/query',
            {
              accountId,
              filter: { text },
              sort: [{ property: 'displayName', isAscending: true }],
              position: 0,
              limit: 20,
            },
            [Capability.Contacts],
          );
          b.call(
            'Contact/get',
            {
              accountId,
              '#ids': q.ref('/ids'),
              properties: ['id', 'name', 'emails', 'phones'],
            },
            [Capability.Contacts],
          );
        });
        const gResp = responses[1];
        if (!gResp || gResp[0] === 'error') continue;
        const gArgs = gResp[1] as { list?: unknown[] };
        for (const c of gArgs.list ?? []) {
          if (typeof c !== 'object' || c === null) continue;
          const rc = c as Record<string, unknown>;
          const cid = String(rc.id ?? '');
          if (cid && !allCandidates.some((ex) => String(ex.id ?? '') === cid)) {
            allCandidates.push(rc);
          }
        }
      } catch {
        // Soft fail — duplicate check is non-critical.
      }
    }

    if (allCandidates.length < 2) return;

    const candidates = allCandidates.map(extractCandidate);
    const clusters = clusterDuplicates(candidates, dismissed);
    // Keep only clusters that contain this contact.
    const myCluster = clusters.find((cl) => cl.contacts.some((c) => c.id === id));
    if (!myCluster) return;

    duplicateCandidateIds = myCluster.contacts
      .filter((c) => c.id !== id)
      .map((c) => c.id);
  }

  function mergeDuplicates(): void {
    if (duplicateCandidateIds.length > 0) {
      // Navigate directly to merge with this contact + its candidates.
      const ids = [contactId, ...duplicateCandidateIds].join(',');
      router.navigate(`/contacts/merge?ids=${encodeURIComponent(ids)}`);
    } else {
      // Fall back to the full duplicates review.
      router.navigate('/contacts/duplicates');
    }
  }

  function editContact(): void {
    router.navigate(`/contacts/${encodeURIComponent(contactId)}/edit`);
  }

  function confirmDelete(): void {
    showDeleteConfirm = true;
  }

  function cancelDelete(): void {
    showDeleteConfirm = false;
  }

  async function performDelete(): Promise<void> {
    if (!vm?.id) return;
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) return;
    showDeleteConfirm = false;
    deleting = true;

    // Optimistic: navigate away immediately (REQ-CONT-34).
    const deletedId = vm.id;
    const savedVm = vm;
    router.navigate('/contacts');

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          { accountId, destroy: [deletedId] },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      const args = (resp?.[1] ?? {}) as { notDestroyed?: Record<string, unknown> };
      if (args.notDestroyed?.[deletedId]) {
        // Revert — navigate back to the contact and show error.
        toast.show({ message: t('contacts.detail.deleteReverted'), kind: 'error' });
        raw = savedVm.rawCard;
        vm = savedVm;
        router.navigate(`/contacts/${encodeURIComponent(deletedId)}`);
      } else {
        // Success: refresh both caches.
        void contacts.reload();
        void contactsListStore.init();
      }
    } catch {
      toast.show({ message: t('contacts.detail.deleteError'), kind: 'error' });
      raw = savedVm.rawCard;
      vm = savedVm;
      router.navigate(`/contacts/${encodeURIComponent(deletedId)}`);
    } finally {
      deleting = false;
    }
  }

  // ── Group membership ────────────────────────────────────────────────────────

  // Groups this contact belongs to (derived reactively from the groups store).
  let memberGroups = $derived(
    contactId ? groupsStore.getGroupsForContact(contactId) : [],
  );

  // Groups the contact is NOT a member of (available to add).
  let nonMemberGroups = $derived(
    groupsStore.groups.filter((g) => g.members[contactId ?? ''] !== true),
  );

  let addGroupBusy = $state(false);
  let showAddGroupSelect = $state(false);
  let addGroupSelectedId = $state('');

  function openAddGroupSelect(): void {
    addGroupSelectedId = nonMemberGroups[0]?.id ?? '';
    showAddGroupSelect = true;
  }

  function closeAddGroupSelect(): void {
    showAddGroupSelect = false;
    addGroupSelectedId = '';
  }

  async function handleAddToGroup(): Promise<void> {
    if (!contactId || !addGroupSelectedId) return;
    addGroupBusy = true;
    const ok = await groupsStore.addMember(addGroupSelectedId, contactId);
    addGroupBusy = false;
    closeAddGroupSelect();
    if (!ok) toast.show({ message: t('contacts.groups.memberError'), kind: 'error' });
  }

  async function handleRemoveFromGroup(groupId: string): Promise<void> {
    if (!contactId) return;
    const ok = await groupsStore.removeMember(groupId, contactId);
    if (!ok) toast.show({ message: t('contacts.groups.memberError'), kind: 'error' });
  }

  // ── Build "all mail with this person" URL. ──────────────────────────────
  function allMailUrl(): string {
    if (!vm) return '#/mail';
    const addrs = vm.emails.map((e) => e.address).filter(Boolean);
    if (addrs.length === 0) return '#/mail';
    const q = addrs.join(' OR ');
    return `#/mail/search?q=${encodeURIComponent(q)}`;
  }

  function contextLabel(ctx: { home: boolean; work: boolean; other: boolean }, prefix: string): string {
    if (ctx.home) return t(`${prefix}.home` as Parameters<typeof t>[0]);
    if (ctx.work) return t(`${prefix}.work` as Parameters<typeof t>[0]);
    if (ctx.other) return t(`${prefix}.other` as Parameters<typeof t>[0]);
    return '';
  }

  function anniversaryLabel(kind: string): string {
    if (kind === 'birth') return t('contacts.detail.anniversary.birth');
    if (kind === 'wedding') return t('contacts.detail.anniversary.wedding');
    return kind || t('contacts.detail.section.anniversary');
  }

  function mapUrl(addr: string): string {
    return `https://www.openstreetmap.org/search?query=${encodeURIComponent(addr)}`;
  }
</script>

<div class="detail-view">
  <!-- Toolbar -->
  <div class="toolbar">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/contacts')}
    >
      {t('contacts.list.title')}
    </button>
  </div>

  <!-- Loading / error states -->
  {#if loadStatus === 'loading'}
    <p class="state-msg">{t('contact.view.loading')}</p>
  {:else if loadStatus === 'error' && notFound}
    <p class="state-msg error">{t('contacts.detail.notFound')}</p>
    <button type="button" class="back-link" onclick={() => router.navigate('/contacts')}>
      {t('contacts.list.title')}
    </button>
  {:else if loadStatus === 'error'}
    <p class="state-msg error">{t('contact.view.couldNotLoad')}</p>
    <button type="button" class="back-link" onclick={() => router.navigate('/contacts')}>
      {t('contacts.list.title')}
    </button>
  {:else if loadStatus === 'ready' && vm}
    <!-- Delete confirmation dialog -->
    {#if showDeleteConfirm}
      <div class="overlay" role="dialog" aria-modal="true" aria-label={t('contacts.detail.deleteConfirm.title')}>
        <div class="dialog">
          <h2 class="dialog-title">{t('contacts.detail.deleteConfirm.title')}</h2>
          <p class="dialog-msg">{t('contacts.detail.deleteConfirm.message')}</p>
          <div class="dialog-actions">
            <Button variant="danger" onclick={() => void performDelete()}>
              {t('contacts.detail.deleteConfirm.confirm')}
            </Button>
            <Button variant="secondary" onclick={cancelDelete}>
              {t('contacts.detail.deleteConfirm.cancel')}
            </Button>
          </div>
        </div>
      </div>
    {/if}

    <div class="contact-card">
      <!-- Header: avatar + name + action buttons -->
      <div class="contact-header">
        <ContactAvatar
          blobId={photoBlobId}
          fallbackInitial={monogram}
          displayName={displayName}
          size={56}
        />
        <div class="header-body">
          <h1 class="contact-name">
            {displayName || t('contacts.list.unnamed')}
          </h1>
          <!-- nicknames -->
          {#if vm.nicknames.length > 0}
            <p class="nicknames">
              {vm.nicknames.map((n) => n.name).filter(Boolean).join(', ')}
            </p>
          {/if}
        </div>
        <div class="header-actions">
          <Button variant="secondary" compact onclick={editContact} disabled={deleting}>
            {t('contacts.detail.edit')}
          </Button>
          <Button variant="secondary" compact onclick={confirmDelete} disabled={deleting}>
            {t('contacts.detail.delete')}
          </Button>
          {#if duplicateCandidateIds.length > 0}
            <Button variant="secondary" compact onclick={mergeDuplicates} disabled={deleting}>
              {t('contacts.detail.merge')}
            </Button>
          {/if}
        </div>
      </div>

      <!-- Organizations + Titles -->
      {#if vm.orgs.length > 0 || vm.titles.length > 0}
        <section class="section" aria-label={t('contacts.detail.section.org')}>
          <h2 class="section-heading">{t('contacts.detail.section.org')}</h2>
          {#each vm.orgs as org (org.key)}
            <div class="org-block">
              <span class="org-name">{org.name}</span>
              {#if org.units.length > 0}
                <span class="org-units">{org.units.join(' · ')}</span>
              {/if}
              {#each vm.titles.filter((tt) => !tt.orgId || tt.orgId === org.key) as title (title.key)}
                <span class="title-name">
                  {title.name}
                  {#if title.kind}
                    <span class="title-kind">
                      ({title.kind === 'title' ? t('contacts.detail.title.title') : t('contacts.detail.title.role')})
                    </span>
                  {/if}
                </span>
              {/each}
            </div>
          {/each}
          <!-- Titles not bound to an org -->
          {#each standaloneOrgs as title (title.key)}
            <div class="org-block">
              <span class="title-name">{title.name}</span>
            </div>
          {/each}
        </section>
      {/if}

      <!-- Emails -->
      {#if vm.emails.length > 0}
        <section class="section" aria-label={t('contacts.detail.section.email')}>
          <h2 class="section-heading">{t('contacts.detail.section.email')}</h2>
          {#each vm.emails as email (email.key)}
            <div class="field-row">
              <a
                href="mailto:{email.address}"
                class="field-value link"
                onclick={(e) => { e.preventDefault(); router.navigate(`/compose?to=${encodeURIComponent(email.address)}`); }}
              >
                {email.address}
              </a>
              <span class="field-meta">
                {#if email.pref}<span class="badge">{t('contacts.detail.pref')}</span>{/if}
                {contextLabel({ home: email.contextHome, work: email.contextWork, other: email.contextOther }, 'contacts.detail.email')}
                {#if email.label}&nbsp;{email.label}{/if}
              </span>
            </div>
          {/each}
        </section>
      {/if}

      <!-- Phones -->
      {#if vm.phones.length > 0}
        <section class="section" aria-label={t('contacts.detail.section.phone')}>
          <h2 class="section-heading">{t('contacts.detail.section.phone')}</h2>
          {#each vm.phones as phone (phone.key)}
            <div class="field-row">
              <a href="tel:{phone.number}" class="field-value link">{phone.number}</a>
              <span class="field-meta">
                {#if phone.pref}<span class="badge">{t('contacts.detail.pref')}</span>{/if}
                {#if phone.featureCell}{t('contacts.detail.phone.mobile')}{:else if phone.featureFax}{t('contacts.detail.phone.fax')}{:else}{contextLabel({ home: phone.contextHome, work: phone.contextWork, other: phone.contextOther }, 'contacts.detail.phone')}{/if}
                {#if phone.label}&nbsp;{phone.label}{/if}
              </span>
            </div>
          {/each}
        </section>
      {/if}

      <!-- Addresses -->
      {#if vm.addresses.length > 0}
        <section class="section" aria-label={t('contacts.detail.section.address')}>
          <h2 class="section-heading">{t('contacts.detail.section.address')}</h2>
          {#each vm.addresses as addr (addr.key)}
            {@const formatted = formatAddress(addr)}
            {#if formatted}
              <div class="field-row addr-row">
                <div class="addr-lines">
                  {#if addr.street}<div>{addr.street}</div>{/if}
                  {#if addr.postcode || addr.locality}
                    <div>{[addr.postcode, addr.locality].filter(Boolean).join(' ')}</div>
                  {/if}
                  {#if addr.region}<div>{addr.region}</div>{/if}
                  {#if addr.country}<div>{addr.country}</div>{/if}
                  {#if !addr.street && !addr.locality && addr.full}<div>{addr.full}</div>{/if}
                </div>
                <span class="field-meta">
                  {#if addr.pref}<span class="badge">{t('contacts.detail.pref')}</span>{/if}
                  {contextLabel({ home: addr.contextHome, work: addr.contextWork, other: addr.contextOther }, 'contacts.detail.address')}
                  {#if addr.label}&nbsp;{addr.label}{/if}
                  &nbsp;
                  <a
                    href={mapUrl(formatted)}
                    target="_blank"
                    rel="noopener noreferrer"
                    class="map-link"
                  >{t('contacts.detail.mapLink')}</a>
                </span>
              </div>
            {/if}
          {/each}
        </section>
      {/if}

      <!-- Links / URLs -->
      {#if vm.links.length > 0}
        <section class="section" aria-label={t('contacts.detail.section.url')}>
          <h2 class="section-heading">{t('contacts.detail.section.url')}</h2>
          {#each vm.links as link (link.key)}
            {#if link.uri}
              <div class="field-row">
                <a
                  href={link.uri}
                  class="field-value link"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  {link.label || link.uri}
                </a>
                {#if link.kind}
                  <span class="field-meta">{link.kind}</span>
                {/if}
              </div>
            {/if}
          {/each}
        </section>
      {/if}

      <!-- Notes -->
      {#if vm.notes.length > 0}
        <section class="section" aria-label={t('contacts.detail.section.note')}>
          <h2 class="section-heading">{t('contacts.detail.section.note')}</h2>
          {#each vm.notes as note (note.key)}
            <p class="note-text">{note.note}</p>
          {/each}
        </section>
      {/if}

      <!-- Anniversaries -->
      {#if vm.anniversaries.length > 0}
        <section class="section" aria-label={t('contacts.detail.section.anniversary')}>
          <h2 class="section-heading">{t('contacts.detail.section.anniversary')}</h2>
          {#each vm.anniversaries as ann (ann.key)}
            {@const formatted = formatAnniversary(ann, 'en')}
            {#if formatted}
              <div class="field-row">
                <span class="field-value">{formatted}</span>
                <span class="field-meta">{anniversaryLabel(ann.kind)}</span>
              </div>
            {/if}
          {/each}
        </section>
      {/if}

      <!-- Groups (REQ-CONT-71) — shown for individual contacts only -->
      {#if vm.kind !== 'group'}
        <section class="section" aria-label={t('contacts.detail.section.groups')}>
          <h2 class="section-heading">{t('contacts.detail.section.groups')}</h2>
          <div class="groups-row">
            {#each memberGroups as group (group.id)}
              <span class="group-chip">
                <button
                  type="button"
                  class="group-chip-name"
                  onclick={() => {
                    contactsListStore.setGroup(group.id, Object.keys(group.members));
                    router.navigate('/contacts');
                  }}
                >
                  {group.name}
                </button>
                <button
                  type="button"
                  class="group-chip-remove"
                  aria-label={t('contacts.detail.groups.removeFromGroup', { name: group.name })}
                  title={t('contacts.detail.groups.removeFromGroup', { name: group.name })}
                  onclick={() => void handleRemoveFromGroup(group.id)}
                >
                  &times;
                </button>
              </span>
            {/each}
            {#if !showAddGroupSelect && nonMemberGroups.length > 0}
              <button
                type="button"
                class="add-group-btn"
                onclick={openAddGroupSelect}
              >
                + {t('contacts.detail.groups.addToGroup')}
              </button>
            {/if}
            {#if showAddGroupSelect}
              <div class="add-group-inline">
                <select
                  class="add-group-select"
                  bind:value={addGroupSelectedId}
                  aria-label={t('contacts.detail.groups.addToGroup')}
                >
                  {#each nonMemberGroups as g (g.id)}
                    <option value={g.id}>{g.name}</option>
                  {/each}
                </select>
                <button
                  type="button"
                  class="add-group-confirm"
                  onclick={() => void handleAddToGroup()}
                  disabled={addGroupBusy || !addGroupSelectedId}
                >
                  {t('contacts.groups.createDialog.confirm')}
                </button>
                <button
                  type="button"
                  class="add-group-cancel"
                  onclick={closeAddGroupSelect}
                  disabled={addGroupBusy}
                >
                  {t('contacts.groups.createDialog.cancel')}
                </button>
              </div>
            {/if}
          </div>
        </section>
      {/if}

      <!-- Footer actions -->
      <div class="footer-actions">
        <a href={allMailUrl()} class="footer-link">{t('contacts.detail.allMail')}</a>
        <button type="button" class="footer-link" onclick={() => toast.show({ message: t('contacts.detail.export'), kind: 'info' })}>
          {t('contacts.detail.export')}
        </button>
      </div>
    </div>
  {/if}
</div>

<style>
  .detail-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: auto;
    padding: var(--spacing-05);
    box-sizing: border-box;
  }

  .toolbar {
    margin-bottom: var(--spacing-05);
  }

  .back-btn {
    color: var(--interactive);
    font-weight: 500;
    font-size: var(--type-body-compact-01-size);
    padding: 0;
    background: none;
    border: none;
    cursor: pointer;
  }

  .back-btn::before {
    content: '\2190\00a0';
  }

  .back-link {
    color: var(--interactive);
    font-weight: 500;
    margin-top: var(--spacing-04);
    background: none;
    border: none;
    cursor: pointer;
    padding: 0;
    font-size: var(--type-body-compact-01-size);
  }

  .state-msg {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    margin: 0;
  }

  .state-msg.error {
    color: var(--support-error);
  }

  /* Delete confirmation overlay */
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

  .dialog-msg {
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
    margin: 0;
  }

  .dialog-actions {
    display: flex;
    gap: var(--spacing-03);
  }

  /* Contact card layout */
  .contact-card {
    max-width: 560px;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-06);
  }

  .contact-header {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-04);
  }

  .header-body {
    flex: 1 1 auto;
    min-width: 0;
  }

  .contact-name {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    margin: 0;
    color: var(--text-primary);
    word-break: break-word;
  }

  .nicknames {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    margin: var(--spacing-01) 0 0;
  }

  .header-actions {
    display: flex;
    gap: var(--spacing-02);
    flex-shrink: 0;
    align-items: flex-start;
  }

  /* Sections */
  .section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .section-heading {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  /* Field rows */
  .field-row {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .field-value {
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
  }

  .field-value.link {
    color: var(--interactive);
    text-decoration: none;
  }

  .field-value.link:hover {
    text-decoration: underline;
  }

  .field-meta {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    display: flex;
    gap: var(--spacing-02);
    align-items: center;
    flex-wrap: wrap;
  }

  .badge {
    font-size: var(--type-label-01-size, 0.7rem);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: 2px;
    padding: 0 var(--spacing-02);
    line-height: 1.4;
  }

  .map-link {
    color: var(--interactive);
    text-decoration: none;
    font-size: var(--type-body-compact-01-size);
  }

  .map-link:hover {
    text-decoration: underline;
  }

  /* Address block */
  .addr-row {
    align-items: flex-start;
  }

  .addr-lines {
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
    line-height: 1.5;
  }

  /* Organizations */
  .org-block {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .org-name {
    font-size: var(--type-body-01-size);
    font-weight: 500;
    color: var(--text-primary);
  }

  .org-units {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .title-name {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .title-kind {
    color: var(--text-helper);
  }

  /* Notes */
  .note-text {
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
  }

  /* Groups section */
  .groups-row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    align-items: center;
  }

  .group-chip {
    display: inline-flex;
    align-items: center;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    background: var(--layer-02);
    overflow: hidden;
  }

  .group-chip-name {
    padding: 2px var(--spacing-02) 2px var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--interactive);
    background: transparent;
    border: none;
    cursor: pointer;
    text-decoration: none;
    line-height: 1.4;
  }

  .group-chip-name:hover {
    text-decoration: underline;
  }

  .group-chip-name:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .group-chip-remove {
    padding: 2px var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    background: transparent;
    border: none;
    cursor: pointer;
    line-height: 1;
    border-left: 1px solid var(--border-subtle-01);
  }

  .group-chip-remove:hover {
    color: var(--support-error);
    background: var(--support-error-bg, rgba(218, 30, 40, 0.1));
  }

  .group-chip-remove:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .add-group-btn {
    padding: 2px var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--interactive);
    background: transparent;
    border: 1px dashed var(--interactive);
    border-radius: var(--radius-pill);
    cursor: pointer;
    line-height: 1.4;
  }

  .add-group-btn:hover {
    background: var(--layer-02);
  }

  .add-group-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .add-group-inline {
    display: flex;
    gap: var(--spacing-02);
    align-items: center;
    flex-wrap: wrap;
  }

  .add-group-select {
    height: 28px;
    padding: 0 var(--spacing-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
  }

  .add-group-confirm,
  .add-group-cancel {
    height: 28px;
    padding: 0 var(--spacing-03);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
  }

  .add-group-confirm {
    background: var(--interactive);
    color: var(--text-on-color);
    border: none;
  }

  .add-group-confirm:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .add-group-cancel {
    background: transparent;
    border: 1px solid var(--border-subtle-01);
    color: var(--text-primary);
  }

  /* Footer actions */
  .footer-actions {
    display: flex;
    gap: var(--spacing-04);
    padding-top: var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
    flex-wrap: wrap;
  }

  .footer-link {
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    text-decoration: none;
    background: none;
    border: none;
    cursor: pointer;
    padding: 0;
  }

  .footer-link:hover {
    text-decoration: underline;
  }
</style>
