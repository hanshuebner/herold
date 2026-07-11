<script lang="ts">
  /**
   * Assisted merge view — field-by-field merge for a confirmed duplicate cluster
   * (REQ-CONT-92, 93, 94).
   *
   * Reached via #/contacts/merge?ids=id1,id2,...
   *   or programmatically from ContactsDuplicatesView / ContactsDetailView.
   *
   * Presents:
   *   - Name radio: choose which contact's name to use.
   *   - Photo radio: choose which contact's photo (or none).
   *   - Email checkboxes: union, user can deselect.
   *   - Phone checkboxes: union, user can deselect.
   *   - Address checkboxes: union, user can deselect.
   *   - Survivor radio: which contact's ID to keep.
   *
   * Commit: single atomic Contact/set — update survivor + destroy others.
   * A server SetError leaves all pre-merge contacts intact (REQ-CONT-93).
   */

  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { contacts } from '../lib/contacts/store.svelte';
  import { contactsListStore } from '../lib/contacts/list-store.svelte';
  import { router } from '../lib/router/router.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import ContactAvatar from '../lib/contacts/ContactAvatar.svelte';
  import {
    cardToVM,
    vmDisplayName,
    extractPhotoFromCard,
    formatAddress,
    type ContactEditVM,
  } from '../lib/contacts/jscontact';
  import {
    extractCandidate,
    buildMergedVM,
    buildMergeSetArgs,
    defaultMergeChoices,
    type DuplicateCluster,
    type MergeChoices,
  } from '../lib/contacts/dedup';

  // ── Route params ─────────────────────────────────────────────────────────────

  // ids come from the query param: ?ids=id1,id2,...
  let rawIds = $derived(router.getParam('ids') ?? '');
  let contactIds = $derived(
    rawIds ? rawIds.split(',').map((s) => decodeURIComponent(s.trim())).filter(Boolean) : []
  );

  // ── State ─────────────────────────────────────────────────────────────────────

  let loadStatus = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  let cluster = $state<DuplicateCluster | null>(null);
  let choices = $state<MergeChoices | null>(null);
  let merging = $state(false);

  $effect(() => {
    const ids = contactIds;
    if (ids.length >= 2 && loadStatus === 'idle') {
      void loadContacts(ids);
    }
  });

  async function loadContacts(ids: string[]): Promise<void> {
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) { loadStatus = 'error'; return; }
    loadStatus = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Contact/get', { accountId, ids }, [Capability.Contacts]);
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') { loadStatus = 'error'; return; }
      const args = resp[1] as { list?: unknown[]; notFound?: string[] };
      const rawCards = (args.list ?? []).filter(
        (c): c is Record<string, unknown> => typeof c === 'object' && c !== null
      );
      if (rawCards.length < 2) { loadStatus = 'error'; return; }

      const clusterValue: DuplicateCluster = {
        contacts: rawCards.map((raw) => extractCandidate(raw)),
        reasons: [],
      };
      cluster = clusterValue;
      choices = defaultMergeChoices(clusterValue);
      loadStatus = 'ready';
    } catch {
      loadStatus = 'error';
    }
  }

  // ── Derived per-contact view model ────────────────────────────────────────────

  type ContactVM = ContactEditVM & { photoBlobId: string | null };

  let contactVMs = $derived<ContactVM[]>(
    cluster
      ? cluster.contacts.map((c) => ({
          ...cardToVM(c.raw),
          photoBlobId: extractPhotoFromCard(c.raw)?.blobId ?? null,
        }))
      : []
  );

  // ── Name radio helpers ─────────────────────────────────────────────────────────

  function nameLabel(idx: number): string {
    const vm = contactVMs[idx];
    if (!vm) return '';
    return vmDisplayName(vm) || t('contacts.list.unnamed');
  }

  // ── Email selection helpers ───────────────────────────────────────────────────

  function isEmailSelected(contactIndex: number, key: string): boolean {
    return (choices?.selectedEmails ?? []).some(
      (e) => e.contactIndex === contactIndex && e.key === key
    );
  }

  function toggleEmail(contactIndex: number, key: string): void {
    if (!choices) return;
    const selected = choices.selectedEmails;
    const idx = selected.findIndex((e) => e.contactIndex === contactIndex && e.key === key);
    if (idx >= 0) {
      choices = { ...choices, selectedEmails: selected.filter((_, i) => i !== idx) };
    } else {
      choices = { ...choices, selectedEmails: [...selected, { contactIndex, key }] };
    }
  }

  // ── Phone selection helpers ───────────────────────────────────────────────────

  function isPhoneSelected(contactIndex: number, key: string): boolean {
    return (choices?.selectedPhones ?? []).some(
      (p) => p.contactIndex === contactIndex && p.key === key
    );
  }

  function togglePhone(contactIndex: number, key: string): void {
    if (!choices) return;
    const selected = choices.selectedPhones;
    const idx = selected.findIndex((p) => p.contactIndex === contactIndex && p.key === key);
    if (idx >= 0) {
      choices = { ...choices, selectedPhones: selected.filter((_, i) => i !== idx) };
    } else {
      choices = { ...choices, selectedPhones: [...selected, { contactIndex, key }] };
    }
  }

  // ── Address selection helpers ─────────────────────────────────────────────────

  function isAddressSelected(contactIndex: number, key: string): boolean {
    return (choices?.selectedAddresses ?? []).some(
      (a) => a.contactIndex === contactIndex && a.key === key
    );
  }

  function toggleAddress(contactIndex: number, key: string): void {
    if (!choices) return;
    const selected = choices.selectedAddresses;
    const idx = selected.findIndex((a) => a.contactIndex === contactIndex && a.key === key);
    if (idx >= 0) {
      choices = { ...choices, selectedAddresses: selected.filter((_, i) => i !== idx) };
    } else {
      choices = { ...choices, selectedAddresses: [...selected, { contactIndex, key }] };
    }
  }

  // ── Merge commit ──────────────────────────────────────────────────────────────

  async function performMerge(): Promise<void> {
    if (!cluster || !choices) return;
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) return;

    merging = true;
    try {
      const mergedVM = buildMergedVM(cluster, choices);
      const { update, destroy } = buildMergeSetArgs(cluster, choices, mergedVM);

      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          { accountId, update, destroy },
          [Capability.Contacts],
        );
      });

      const resp = responses[0];
      const args = (resp?.[1] ?? {}) as {
        notUpdated?: Record<string, unknown>;
        notDestroyed?: Record<string, unknown>;
      };

      const survivorId = cluster.contacts[choices.survivorIndex]?.id ?? '';

      if (args.notUpdated?.[survivorId] || Object.keys(args.notDestroyed ?? {}).length > 0) {
        // SetError: all pre-merge contacts are intact (server atomicity).
        toast.show({ message: t('contacts.merge.error'), kind: 'error' });
      } else {
        toast.show({ message: t('contacts.merge.success'), kind: 'info' });
        void contacts.reload();
        void contactsListStore.init();
        router.navigate(`/contacts/${encodeURIComponent(survivorId)}`);
      }
    } catch {
      toast.show({ message: t('contacts.merge.error'), kind: 'error' });
    } finally {
      merging = false;
    }
  }
</script>

<div class="merge-view">
  <!-- Toolbar -->
  <div class="toolbar">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/contacts/duplicates')}
    >
      {t('contacts.duplicates.title')}
    </button>
  </div>

  <h1 class="page-title">{t('contacts.merge.title')}</h1>

  {#if loadStatus === 'loading' || loadStatus === 'idle'}
    <p class="state-msg">{t('contacts.duplicates.loading')}</p>
  {:else if loadStatus === 'error'}
    <p class="state-msg error">{t('contacts.duplicates.error')}</p>
  {:else if loadStatus === 'ready' && cluster && choices}
    <div class="merge-form" aria-label={t('contacts.merge.title')}>

      <!-- ── Survivor (which contact's ID to keep) ───────────────────────── -->
      <section class="field-section">
        <fieldset class="field-fieldset">
          <legend class="section-heading">{t('contacts.merge.survivorLabel')}</legend>
          <div class="radio-group">
            {#each cluster.contacts as contact, idx (contact.id)}
              <label class="radio-label">
                <input
                  type="radio"
                  name="survivor"
                  value={idx}
                  checked={choices.survivorIndex === idx}
                  onchange={() => { choices = choices ? { ...choices, survivorIndex: idx } : choices; }}
                />
                <ContactAvatar
                  blobId={contactVMs[idx]?.photoBlobId ?? null}
                  fallbackInitial={(contactVMs[idx]?.name?.given ?? contact.displayName).charAt(0).toUpperCase() || '?'}
                  displayName={nameLabel(idx)}
                  size={28}
                />
                <span class="radio-name">{nameLabel(idx)}</span>
              </label>
            {/each}
          </div>
        </fieldset>
      </section>

      <!-- ── Name ────────────────────────────────────────────────────────── -->
      <section class="field-section">
        <fieldset class="field-fieldset">
          <legend class="section-heading">{t('contacts.merge.chosenName')}</legend>
          <div class="radio-group">
            {#each cluster.contacts as contact, idx (contact.id)}
              {#if nameLabel(idx)}
                <label class="radio-label">
                  <input
                    type="radio"
                    name="chosenName"
                    value={idx}
                    checked={choices.nameContactIndex === idx}
                    onchange={() => { choices = choices ? { ...choices, nameContactIndex: idx } : choices; }}
                  />
                  <span class="radio-name">{nameLabel(idx)}</span>
                </label>
              {/if}
            {/each}
          </div>
        </fieldset>
      </section>

      <!-- ── Photo ───────────────────────────────────────────────────────── -->
      {#if cluster.contacts.some((c) => extractPhotoFromCard(c.raw))}
        <section class="field-section">
          <fieldset class="field-fieldset">
            <legend class="section-heading">{t('contacts.merge.chosenPhoto')}</legend>
            <div class="radio-group">
              <label class="radio-label">
                <input
                  type="radio"
                  name="chosenPhoto"
                  value={-1}
                  checked={choices.photoContactIndex === -1}
                  onchange={() => { choices = choices ? { ...choices, photoContactIndex: -1 } : choices; }}
                />
                <span class="radio-name">{t('contacts.merge.noPhoto')}</span>
              </label>
              {#each cluster.contacts as contact, idx (contact.id)}
                {#if extractPhotoFromCard(contact.raw)}
                  <label class="radio-label">
                    <input
                      type="radio"
                      name="chosenPhoto"
                      value={idx}
                      checked={choices.photoContactIndex === idx}
                      onchange={() => { choices = choices ? { ...choices, photoContactIndex: idx } : choices; }}
                    />
                    <ContactAvatar
                      blobId={contactVMs[idx]?.photoBlobId ?? null}
                      fallbackInitial={(contactVMs[idx]?.name?.given ?? contact.displayName).charAt(0).toUpperCase() || '?'}
                      displayName={nameLabel(idx)}
                      size={28}
                    />
                    <span class="radio-name">{nameLabel(idx)}</span>
                  </label>
                {/if}
              {/each}
            </div>
          </fieldset>
        </section>
      {/if}

      <!-- ── Emails ──────────────────────────────────────────────────────── -->
      {#if contactVMs.some((vm) => vm.emails.length > 0)}
        <section class="field-section">
          <h2 class="section-heading">{t('contacts.merge.emailsHeading')}</h2>
          <div class="check-group">
            {#each contactVMs as vm, ci (ci)}
              {#each vm.emails as email (email.key)}
                <label class="check-label">
                  <input
                    type="checkbox"
                    checked={isEmailSelected(ci, email.key)}
                    onchange={() => toggleEmail(ci, email.key)}
                  />
                  <span class="check-value">{email.address}</span>
                  <span class="check-source">{t('contacts.merge.from', { name: nameLabel(ci) })}</span>
                </label>
              {/each}
            {/each}
          </div>
        </section>
      {/if}

      <!-- ── Phones ──────────────────────────────────────────────────────── -->
      {#if contactVMs.some((vm) => vm.phones.length > 0)}
        <section class="field-section">
          <h2 class="section-heading">{t('contacts.merge.phonesHeading')}</h2>
          <div class="check-group">
            {#each contactVMs as vm, ci (ci)}
              {#each vm.phones as phone (phone.key)}
                <label class="check-label">
                  <input
                    type="checkbox"
                    checked={isPhoneSelected(ci, phone.key)}
                    onchange={() => togglePhone(ci, phone.key)}
                  />
                  <span class="check-value">{phone.number}</span>
                  <span class="check-source">{t('contacts.merge.from', { name: nameLabel(ci) })}</span>
                </label>
              {/each}
            {/each}
          </div>
        </section>
      {/if}

      <!-- ── Addresses ───────────────────────────────────────────────────── -->
      {#if contactVMs.some((vm) => vm.addresses.length > 0)}
        <section class="field-section">
          <h2 class="section-heading">{t('contacts.merge.addressesHeading')}</h2>
          <div class="check-group">
            {#each contactVMs as vm, ci (ci)}
              {#each vm.addresses as addr (addr.key)}
                {@const formatted = formatAddress(addr)}
                {#if formatted}
                  <label class="check-label">
                    <input
                      type="checkbox"
                      checked={isAddressSelected(ci, addr.key)}
                      onchange={() => toggleAddress(ci, addr.key)}
                    />
                    <span class="check-value">{formatted}</span>
                    <span class="check-source">{t('contacts.merge.from', { name: nameLabel(ci) })}</span>
                  </label>
                {/if}
              {/each}
            {/each}
          </div>
        </section>
      {/if}

      <!-- ── Actions ─────────────────────────────────────────────────────── -->
      <div class="form-actions">
        <Button
          variant="primary"
          onclick={() => void performMerge()}
          disabled={merging}
        >
          {merging ? t('contacts.merge.merging') : t('contacts.merge.confirm')}
        </Button>
        <Button
          variant="secondary"
          onclick={() => router.navigate('/contacts/duplicates')}
          disabled={merging}
        >
          {t('contacts.merge.cancel')}
        </Button>
      </div>
    </div>
  {/if}
</div>

<style>
  .merge-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: auto;
    padding: var(--spacing-05);
    box-sizing: border-box;
  }

  .toolbar {
    margin-bottom: var(--spacing-04);
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

  .page-title {
    font-size: var(--type-heading-04-size, 1.25rem);
    font-weight: 600;
    color: var(--text-primary);
    margin: 0 0 var(--spacing-05);
  }

  .state-msg {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    margin: 0;
  }

  .state-msg.error {
    color: var(--support-error);
  }

  .merge-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-06);
    max-width: 560px;
  }

  .field-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .field-fieldset {
    border: none;
    padding: 0;
    margin: 0;
  }

  .section-heading {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    margin: 0 0 var(--spacing-03);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .radio-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .radio-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    cursor: pointer;
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
  }

  .radio-label input[type="radio"] {
    accent-color: var(--interactive);
    width: 16px;
    height: 16px;
    flex-shrink: 0;
  }

  .radio-name {
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
  }

  .check-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .check-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    cursor: pointer;
    flex-wrap: wrap;
  }

  .check-label input[type="checkbox"] {
    accent-color: var(--interactive);
    width: 16px;
    height: 16px;
    flex-shrink: 0;
  }

  .check-value {
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
  }

  .check-source {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .form-actions {
    display: flex;
    gap: var(--spacing-03);
    padding-top: var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
  }
</style>
