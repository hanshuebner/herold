<script lang="ts">
  /**
   * Contact create/edit form — full JSContact Card editing (REQ-CONT-40..52).
   *
   * Props:
   *   contactId — existing contact id for edit mode; null/undefined for create.
   *
   * Covers:
   *   - Structured name (given, surname; more: prefix, middle, suffix, credential)
   *   - Kind selector (de-emphasised, defaults to individual)
   *   - Repeatable typed emails, phones, postal addresses, orgs, titles
   *   - Untyped repeatable: nicknames, links, notes, anniversaries
   *   - Client-side validation (REQ-CONT-50)
   *   - Optimistic save + SetError revert (REQ-CONT-51)
   *   - Dirty-form guard on navigate-away (REQ-CONT-52)
   *   - Saving with no changes issues no Contact/set (REQ-CONT-49)
   *
   * Unknown top-level Card properties are preserved via vmToPatch (REQ-CONT-01).
   * uid is never fabricated on create (REQ-CONT-02).
   *
   * Edit mode (an existing contactId) saves immediately: every field change
   * schedules a debounced patch to Contact/set, using the same shared
   * AutosaveController/SaveStatus indicator as the Settings identity editor
   * (Item 7) so the "Saving…/Saved" affordance is consistent across the
   * Suite. There is no Speichern/Abbrechen commit step — a failed patch
   * reverts the form to the server's last-known state (REQ-CONT-51) and
   * the indicator surfaces the error inline. Create mode has no id to
   * patch yet, so it keeps the explicit Erstellen/Abbrechen commit
   * (re #190).
   */

  import { onDestroy } from 'svelte';
  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { contacts } from '../lib/contacts/store.svelte';
  import { contactsListStore } from '../lib/contacts/list-store.svelte';
  import { router } from '../lib/router/router.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import ContactPhotoEditor from '../lib/contacts/ContactPhotoEditor.svelte';
  import TrashIcon from '../lib/icons/TrashIcon.svelte';
  import SaveStatus from './settings/SaveStatus.svelte';
  import { AutosaveController } from './settings/autosave.svelte';
  import {
    cardToVM,
    vmToCard,
    vmToPatch,
    validateVM,
    nextKey,
    vmMonogram,
    extractPhotoFromCard,
    buildMediaWithPhoto,
    buildMediaWithPhotoRemoved,
    type ContactEditVM,
    type EmailVM,
    type PhoneVM,
    type AddressVM,
    type OrgVM,
    type TitleVM,
    type NicknameVM,
    type LinkVM,
    type NoteVM,
    type AnniversaryVM,
    type ValidationError,
  } from '../lib/contacts/jscontact';

  interface Props {
    contactId?: string | null;
  }

  let { contactId = null }: Props = $props();

  const isCreate = $derived(!contactId);

  // ── Load state ────────────────────────────────────────────────────────────

  let loadStatus = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  let saving = $state(false);
  let vm = $state<ContactEditVM | null>(null);

  // Fallback initial for the photo editor monogram (derived from the live VM).
  let photoFallbackInitial = $derived(vm ? vmMonogram(vm) : '?');
  let originalRaw = $state<Record<string, unknown>>({});
  let showMoreName = $state(false);
  let validationErrors = $state<ValidationError[]>([]);

  // Dirty-form guard: track whether the form has unsaved changes.
  let isDirty = $state(false);

  // ── Autosave (edit mode only) ──────────────────────────────────────────────
  // Every field change (markDirty) resets this timer; when the form goes
  // idle for AUTOSAVE_IDLE_MS the pending edits are patched to Contact/set.
  // Create mode has no id to patch against and keeps the explicit
  // Erstellen/Abbrechen commit instead. The shared AutosaveController
  // drives the "Saving…/Saved" indicator (same class the Settings identity
  // editor uses — see views/settings/autosave.svelte.ts).
  const AUTOSAVE_IDLE_MS = 900;
  let autosaveTimer: ReturnType<typeof setTimeout> | null = null;
  // A change arriving while a save is already in flight is not dropped: it
  // re-runs autosaveNow once the in-flight save settles.
  let autosavePendingRetry = false;
  const autosave = new AutosaveController();
  onDestroy(() => autosave.dispose());

  // ── Photo state (REQ-CONT-60..61) ─────────────────────────────────────────
  // currentPhotoBlobId: the blobId already on the server for this contact.
  let currentPhotoBlobId = $state<string | null>(null);
  // photoAction: what to do with the photo on save.
  let photoAction = $state<'keep' | 'replace' | 'remove'>('keep');
  // photoPendingFile: cropped JPEG Blob to upload (when photoAction === 'replace').
  let photoPendingFile = $state<Blob | null>(null);
  let photoPendingMediaType = $state('');
  let photoPendingPreviewUrl = $state<string | null>(null);

  // Keyboard shortcut: Ctrl/Cmd+S saves the form. In create mode this runs
  // the explicit create; in edit mode it flushes any pending autosave
  // immediately instead of waiting out the idle timer.
  function handleKeydown(e: KeyboardEvent): void {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault();
      if (!vm) return;
      if (isCreate) {
        if (!saving) void handleSave();
      } else {
        flushAutosave();
      }
    }
  }

  $effect(() => {
    document.addEventListener('keydown', handleKeydown);
    return () => document.removeEventListener('keydown', handleKeydown);
  });

  // Dirty-form: warn on page unload.
  $effect(() => {
    if (isDirty) {
      const handler = (e: BeforeUnloadEvent) => {
        e.preventDefault();
      };
      window.addEventListener('beforeunload', handler);
      return () => window.removeEventListener('beforeunload', handler);
    }
  });

  // Load existing contact when contactId is set.
  $effect(() => {
    const id = contactId;
    if (!id) {
      // Create mode: start with a blank VM.
      vm = emptyVM();
      loadStatus = 'ready';
    } else {
      void loadContact(id);
    }
  });

  function emptyVM(): ContactEditVM {
    // Reset photo state for a new (create) form.
    currentPhotoBlobId = null;
    photoAction = 'keep';
    photoPendingFile = null;
    photoPendingMediaType = '';
    photoPendingPreviewUrl = null;
    return {
      id: null,
      kind: 'individual',
      name: { given: '', surname: '', middle: '', prefix: '', suffix: '', credential: '', full: '', sortAs: '', isOrdered: false },
      emails: [],
      phones: [],
      addresses: [],
      orgs: [],
      titles: [],
      nicknames: [],
      links: [],
      notes: [],
      anniversaries: [],
      rawCard: {},
    };
  }

  async function loadContact(id: string): Promise<void> {
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) { loadStatus = 'error'; return; }
    loadStatus = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Contact/get', { accountId, ids: [id] }, [Capability.Contacts]);
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') { loadStatus = 'error'; return; }
      const args = resp[1] as { list: unknown[] };
      const raw = args.list[0];
      if (!raw || typeof raw !== 'object') { loadStatus = 'error'; return; }
      const rawCard = raw as Record<string, unknown>;
      originalRaw = rawCard;
      vm = cardToVM(rawCard);
      // Extract current photo for the photo editor.
      const photo = extractPhotoFromCard(rawCard);
      currentPhotoBlobId = photo?.blobId ?? null;
      photoAction = 'keep';
      photoPendingFile = null;
      photoPendingMediaType = '';
      photoPendingPreviewUrl = null;
      isDirty = false;
      loadStatus = 'ready';
    } catch {
      loadStatus = 'error';
    }
  }

  // ── Save (create mode) ──────────────────────────────────────────────────────
  // Create mode has no id to patch against yet, so it keeps the explicit
  // Erstellen commit. Edit mode autosaves instead (see below).

  async function handleSave(): Promise<void> {
    if (!vm || !isCreate) return;
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) {
      toast.show({ message: t('contacts.edit.saveError'), kind: 'error' });
      return;
    }

    // Client-side validation (REQ-CONT-50).
    const errs = validateVM(vm);
    validationErrors = errs;
    if (errs.length > 0) return;

    saving = true;
    try {
      await createContact(accountId);
    } finally {
      saving = false;
    }
  }

  async function createContact(accountId: string): Promise<void> {
    if (!vm) return;
    const card = vmToCard(vm);

    // Upload photo if one was set (REQ-CONT-60).
    if (photoAction === 'replace' && photoPendingFile) {
      try {
        const result = await jmap.uploadBlob({
          accountId,
          body: photoPendingFile,
          type: photoPendingMediaType || 'image/jpeg',
        });
        card.media = buildMediaWithPhoto(undefined, result.blobId, result.type || photoPendingMediaType);
      } catch {
        toast.show({ message: t('contacts.edit.photo.uploadError'), kind: 'error' });
        return;
      }
    }

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Contact/set',
        { accountId, create: { tmp: card } },
        [Capability.Contacts],
      );
    });
    const resp = responses[0];
    const args = (resp?.[1] ?? {}) as {
      created?: Record<string, { id: string }>;
      notCreated?: Record<string, { type: string; description?: string }>;
    };
    const notCreated = args.notCreated?.['tmp'];
    if (notCreated) {
      const desc = notCreated.description ?? notCreated.type;
      toast.show({ message: `${t('contacts.edit.saveError')}: ${desc}`, kind: 'error' });
      return;
    }
    const created = args.created?.['tmp'];
    const newId = created?.id;
    toast.show({ message: t('contacts.edit.saveSuccess'), kind: 'info' });
    isDirty = false;
    void contacts.reload();
    void contactsListStore.init();
    if (newId) {
      router.navigate(`/contacts/${encodeURIComponent(newId)}`);
    } else {
      router.navigate('/contacts');
    }
  }

  // ── Autosave (edit mode) ─────────────────────────────────────────────────────
  //
  // markDirty schedules autosaveNow after AUTOSAVE_IDLE_MS of inactivity.
  // persistUpdate does the actual Contact/set patch and, on success, folds
  // the applied patch into originalRaw so the next diff compares against
  // the server's last-known state — the optimistic-save-with-revert
  // contract of REQ-CONT-51, run continuously instead of once per click.
  // persistUpdate throws on failure so autosave.run() (the same contract
  // IdentityEditPage's Reply-To/Bcc autosave uses) surfaces the error on
  // the shared SaveStatus indicator.

  /** Patch the current vm to the server. */
  async function persistUpdate(accountId: string): Promise<void> {
    if (!vm || !vm.id) return;
    const { changed, patch } = vmToPatch(originalRaw, vm);

    // Compute media patch for photo change (REQ-CONT-60).
    let mediaPatch: Record<string, unknown> | null | undefined = undefined; // undefined = no change
    if (photoAction === 'replace' && photoPendingFile) {
      let result;
      try {
        result = await jmap.uploadBlob({
          accountId,
          body: photoPendingFile,
          type: photoPendingMediaType || 'image/jpeg',
        });
      } catch {
        throw new Error(t('contacts.edit.photo.uploadError'));
      }
      mediaPatch = buildMediaWithPhoto(
        originalRaw.media as Record<string, unknown> | undefined,
        result.blobId,
        result.type || photoPendingMediaType,
      );
    } else if (photoAction === 'remove') {
      mediaPatch = buildMediaWithPhotoRemoved(
        originalRaw.media as Record<string, unknown> | undefined,
      );
    }

    // Apply media patch to the field patch.
    if (mediaPatch !== undefined) {
      patch.media = mediaPatch;
    }

    if (!changed && mediaPatch === undefined) {
      // No changes at all — nothing to send (REQ-CONT-49).
      isDirty = false;
      return;
    }

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Contact/set',
        { accountId, update: { [vm!.id!]: patch } },
        [Capability.Contacts],
      );
    });
    const resp = responses[0];
    const args = (resp?.[1] ?? {}) as {
      updated?: Record<string, unknown>;
      notUpdated?: Record<string, { type: string; description?: string }>;
    };
    const notUpdated = args.notUpdated?.[vm.id];
    if (notUpdated) {
      // Revert local state to originalRaw (REQ-CONT-51).
      vm = cardToVM(originalRaw);
      isDirty = false;
      throw new Error(notUpdated.description ?? notUpdated.type);
    }

    // Fold the applied patch into originalRaw (JSON Merge Patch semantics:
    // null deletes the key, otherwise the key is replaced wholesale) so the
    // next autosave diffs against what the server now has.
    for (const key of Object.keys(patch)) {
      const val = patch[key];
      if (val === null) {
        delete originalRaw[key];
      } else {
        originalRaw[key] = val;
      }
    }
    // The photo action was one-shot; reset it so a later, unrelated
    // autosave does not re-upload or re-delete the same photo.
    if (photoAction === 'replace') {
      currentPhotoBlobId = extractPhotoFromCard(originalRaw)?.blobId ?? currentPhotoBlobId;
      photoAction = 'keep';
      photoPendingFile = null;
      photoPendingMediaType = '';
      photoPendingPreviewUrl = null;
    } else if (photoAction === 'remove') {
      currentPhotoBlobId = null;
      photoAction = 'keep';
    }

    isDirty = false;
    void contacts.reload();
    void contactsListStore.init();
  }

  /** Validate and persist the current vm; re-runs itself if edits arrive mid-flight. */
  async function autosaveNow(): Promise<void> {
    if (!vm || isCreate || !vm.id) return;
    if (autosave.state === 'saving') {
      autosavePendingRetry = true;
      return;
    }
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) return;

    // Client-side validation (REQ-CONT-50): an invalid form is not sent —
    // the inline field errors say what to fix, and the shared save-status
    // indicator flags that nothing was saved (beforeunload/isDirty still
    // protects the unsent edit).
    const errs = validateVM(vm);
    validationErrors = errs;
    if (errs.length > 0) {
      autosave.state = 'error';
      autosave.errorMessage = t('contacts.edit.autosaveBlocked');
      return;
    }

    const { changed } = vmToPatch(originalRaw, vm);
    if (!changed && photoAction === 'keep') {
      // Nothing to send (REQ-CONT-49) — skip the "Saved" flash too.
      isDirty = false;
      return;
    }

    await autosave.run(() => persistUpdate(accountId));
    if (autosavePendingRetry) {
      autosavePendingRetry = false;
      void autosaveNow();
    }
  }

  /** Cancel the idle timer and save right away (Ctrl+S, back navigation). */
  function flushAutosave(): void {
    if (autosaveTimer !== null) {
      clearTimeout(autosaveTimer);
      autosaveTimer = null;
    }
    void autosaveNow();
  }

  function handleCancel(): void {
    if (isCreate) {
      if (isDirty) {
        if (!confirm(t('contacts.edit.discardGuard.message'))) return;
      }
      isDirty = false;
      router.navigate('/contacts');
      return;
    }
    // Edit mode autosaves as you type; flush any pending debounce so a
    // fast back-click right after typing is not lost to the idle timer.
    if (isDirty) flushAutosave();
    if (contactId) {
      router.navigate(`/contacts/${encodeURIComponent(contactId)}`);
    }
  }

  // ── Mutation helpers (mark dirty on every change) ─────────────────────────

  function markDirty(): void {
    isDirty = true;
    validationErrors = [];
    if (isCreate) return;
    if (autosaveTimer !== null) clearTimeout(autosaveTimer);
    autosaveTimer = setTimeout(() => {
      autosaveTimer = null;
      void autosaveNow();
    }, AUTOSAVE_IDLE_MS);
  }

  // Flush a pending autosave when the form unmounts by any navigation path
  // (sidebar click, browser back, etc.), not just the in-view back button.
  onDestroy(() => {
    if (!isCreate && autosaveTimer !== null) {
      clearTimeout(autosaveTimer);
      autosaveTimer = null;
      void autosaveNow();
    }
  });

  // The photo editor mutates photoAction/photoPendingFile directly via
  // bind:, with no discrete input/change event the form can hook — so a
  // photo-only edit (no other field touched) needs its own dirty trigger
  // to schedule the autosave (REQ-CONT-60/REQ-CONT-51 for photos).
  $effect(() => {
    const action = photoAction;
    const file = photoPendingFile;
    if (loadStatus !== 'ready' || isCreate) return;
    if (action === 'keep' && !file) return;
    markDirty();
  });

  // ── Email list ────────────────────────────────────────────────────────────

  function addEmail(): void {
    if (!vm) return;
    vm.emails = [...vm.emails, { key: nextKey(), address: '', contextHome: false, contextWork: false, contextOther: false, pref: false, label: '' }];
    markDirty();
  }

  function removeEmail(key: string): void {
    if (!vm) return;
    vm.emails = vm.emails.filter((e) => e.key !== key);
    markDirty();
  }

  function toggleEmailPref(key: string): void {
    if (!vm) return;
    vm.emails = vm.emails.map((e) => ({ ...e, pref: e.key === key ? !e.pref : false }));
    markDirty();
  }

  // ── Phone list ────────────────────────────────────────────────────────────

  function addPhone(): void {
    if (!vm) return;
    vm.phones = [...vm.phones, { key: nextKey(), number: '', contextHome: false, contextWork: false, contextOther: false, featureVoice: false, featureCell: false, featureFax: false, featureText: false, featureVideo: false, pref: false, label: '' }];
    markDirty();
  }

  function removePhone(key: string): void {
    if (!vm) return;
    vm.phones = vm.phones.filter((p) => p.key !== key);
    markDirty();
  }

  function togglePhonePref(key: string): void {
    if (!vm) return;
    vm.phones = vm.phones.map((p) => ({ ...p, pref: p.key === key ? !p.pref : false }));
    markDirty();
  }

  // ── Address list ──────────────────────────────────────────────────────────

  function addAddress(): void {
    if (!vm) return;
    vm.addresses = [...vm.addresses, { key: nextKey(), street: '', locality: '', region: '', postcode: '', country: '', countryCode: '', contextHome: false, contextWork: false, contextOther: false, pref: false, label: '', full: '', extraComponents: [] }];
    markDirty();
  }

  function removeAddress(key: string): void {
    if (!vm) return;
    vm.addresses = vm.addresses.filter((a) => a.key !== key);
    markDirty();
  }

  // ── Org list ──────────────────────────────────────────────────────────────

  function addOrg(): void {
    if (!vm) return;
    vm.orgs = [...vm.orgs, { key: nextKey(), name: '', units: [] }];
    markDirty();
  }

  function removeOrg(key: string): void {
    if (!vm) return;
    vm.orgs = vm.orgs.filter((o) => o.key !== key);
    // Remove titles bound to this org.
    vm.titles = vm.titles.filter((tt) => tt.orgId !== key);
    markDirty();
  }

  function addOrgUnit(orgKey: string): void {
    if (!vm) return;
    vm.orgs = vm.orgs.map((o) =>
      o.key === orgKey ? { ...o, units: [...o.units, ''] } : o,
    );
    markDirty();
  }

  function removeOrgUnit(orgKey: string, idx: number): void {
    if (!vm) return;
    vm.orgs = vm.orgs.map((o) =>
      o.key === orgKey ? { ...o, units: o.units.filter((_, i) => i !== idx) } : o,
    );
    markDirty();
  }

  // ── Title list ────────────────────────────────────────────────────────────

  function addTitle(): void {
    if (!vm) return;
    vm.titles = [...vm.titles, { key: nextKey(), name: '', kind: 'title', orgId: '' }];
    markDirty();
  }

  function removeTitle(key: string): void {
    if (!vm) return;
    vm.titles = vm.titles.filter((tt) => tt.key !== key);
    markDirty();
  }

  // ── Nickname list ─────────────────────────────────────────────────────────

  function addNickname(): void {
    if (!vm) return;
    vm.nicknames = [...vm.nicknames, { key: nextKey(), name: '' }];
    markDirty();
  }

  function removeNickname(key: string): void {
    if (!vm) return;
    vm.nicknames = vm.nicknames.filter((n) => n.key !== key);
    markDirty();
  }

  // ── Link list ─────────────────────────────────────────────────────────────

  function addLink(): void {
    if (!vm) return;
    vm.links = [...vm.links, { key: nextKey(), uri: '', kind: '', label: '' }];
    markDirty();
  }

  function removeLink(key: string): void {
    if (!vm) return;
    vm.links = vm.links.filter((l) => l.key !== key);
    markDirty();
  }

  // ── Note list ─────────────────────────────────────────────────────────────

  function addNote(): void {
    if (!vm) return;
    vm.notes = [...vm.notes, { key: nextKey(), note: '' }];
    markDirty();
  }

  function removeNote(key: string): void {
    if (!vm) return;
    vm.notes = vm.notes.filter((n) => n.key !== key);
    markDirty();
  }

  // ── Anniversary list ──────────────────────────────────────────────────────

  function addAnniversary(): void {
    if (!vm) return;
    vm.anniversaries = [...vm.anniversaries, { key: nextKey(), kind: 'birth', year: '', month: '', day: '' }];
    markDirty();
  }

  function removeAnniversary(key: string): void {
    if (!vm) return;
    vm.anniversaries = vm.anniversaries.filter((a) => a.key !== key);
    markDirty();
  }

  // ── Validation helpers ────────────────────────────────────────────────────

  function errorFor(field: string): string {
    return validationErrors.find((e) => e.field === field)?.message ?? '';
  }

  function hasError(field: string): boolean {
    return validationErrors.some((e) => e.field.startsWith(field));
  }
</script>

<div class="edit-view">
  <div class="toolbar">
    <button type="button" class="back-btn" onclick={handleCancel}>
      {isCreate ? t('contacts.list.title') : t('contacts.detail.backToDetail')}
    </button>
    {#if !isCreate && loadStatus === 'ready'}
      <SaveStatus state={autosave.state} errorMessage={autosave.errorMessage} />
    {/if}
  </div>

  {#if loadStatus === 'loading'}
    <p class="state-msg">{t('contact.view.loading')}</p>
  {:else if loadStatus === 'error'}
    <p class="state-msg error">{t('contact.view.couldNotLoad')}</p>
  {:else if loadStatus === 'ready' && vm}
    <form
      class="edit-form"
      onsubmit={(e) => {
        e.preventDefault();
        if (isCreate) { void handleSave(); } else { flushAutosave(); }
      }}
      novalidate
    >
      <h1 class="form-title">
        {isCreate ? t('contacts.edit.title.create') : t('contacts.edit.title.edit')}
      </h1>

      <!-- General validation error -->
      {#if hasError('general')}
        <div class="form-error" role="alert">{errorFor('general')}</div>
      {/if}

      <!-- ── Photo section (REQ-CONT-60..61) ────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.photo')}</legend>
        <ContactPhotoEditor
          currentBlobId={currentPhotoBlobId}
          fallbackInitial={photoFallbackInitial}
          disabled={isCreate && saving}
          bind:action={photoAction}
          bind:pendingFile={photoPendingFile}
          bind:pendingMediaType={photoPendingMediaType}
          bind:pendingPreviewUrl={photoPendingPreviewUrl}
        />
      </fieldset>

      <!-- ── Name section ──────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.name')}</legend>

        <div class="field-row">
          <label class="field-block">
            <span class="field-label">{t('contacts.edit.name.given')}</span>
            <input
              class="field-input"
              type="text"
              bind:value={vm.name.given}
              oninput={markDirty}
              autocomplete="given-name"
              disabled={isCreate && saving}
            />
          </label>
          <label class="field-block">
            <span class="field-label">{t('contacts.edit.name.surname')}</span>
            <input
              class="field-input"
              type="text"
              bind:value={vm.name.surname}
              oninput={markDirty}
              autocomplete="family-name"
              disabled={isCreate && saving}
            />
          </label>
        </div>

        {#if showMoreName}
          <div class="field-row">
            <label class="field-block">
              <span class="field-label">{t('contacts.edit.name.prefix')}</span>
              <input class="field-input" type="text" bind:value={vm.name.prefix} oninput={markDirty} disabled={isCreate && saving} />
            </label>
            <label class="field-block">
              <span class="field-label">{t('contacts.edit.name.middle')}</span>
              <input class="field-input" type="text" bind:value={vm.name.middle} oninput={markDirty} disabled={isCreate && saving} />
            </label>
          </div>
          <div class="field-row">
            <label class="field-block">
              <span class="field-label">{t('contacts.edit.name.credential')}</span>
              <input class="field-input" type="text" bind:value={vm.name.credential} oninput={markDirty} disabled={isCreate && saving} />
            </label>
            <label class="field-block">
              <span class="field-label">{t('contacts.edit.name.suffix')}</span>
              <input class="field-input" type="text" bind:value={vm.name.suffix} oninput={markDirty} disabled={isCreate && saving} />
            </label>
          </div>
          <label class="field-block">
            <span class="field-label">{t('contacts.edit.name.full')}</span>
            <input class="field-input" type="text" bind:value={vm.name.full} oninput={markDirty} disabled={isCreate && saving} placeholder="Derived from components when blank" />
          </label>
        {/if}

        <button
          type="button"
          class="toggle-btn"
          onclick={() => { showMoreName = !showMoreName; }}
        >
          {showMoreName ? t('contacts.edit.name.less') : t('contacts.edit.name.more')}
        </button>
      </fieldset>

      <!-- ── Emails ─────────────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.email')}</legend>
        {#each vm.emails as email, i (email.key)}
          <div class="entry-block" class:entry-error={hasError(`email.${i}`)}>
            <div class="entry-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.email.address')}</span>
                <input
                  class="field-input"
                  class:input-error={hasError(`email.${i}`)}
                  type="email"
                  bind:value={email.address}
                  oninput={markDirty}
                  autocomplete="off"
                  disabled={isCreate && saving}
                />
                {#if hasError(`email.${i}`)}
                  <span class="input-error-msg">{errorFor(`email.${i}`)}</span>
                {/if}
              </label>
              <label class="field-block">
                <span class="field-label">{t('contacts.edit.email.label')}</span>
                <input class="field-input narrow" type="text" bind:value={email.label} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removeEmail(email.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
            <div class="chips-row">
              <button type="button" class="chip" class:chip-on={email.contextHome} onclick={() => { email.contextHome = !email.contextHome; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.email.context.home')}
              </button>
              <button type="button" class="chip" class:chip-on={email.contextWork} onclick={() => { email.contextWork = !email.contextWork; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.email.context.work')}
              </button>
              <button type="button" class="chip" class:chip-on={email.contextOther} onclick={() => { email.contextOther = !email.contextOther; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.email.context.other')}
              </button>
              <label class="pref-label">
                <input type="checkbox" bind:checked={email.pref} onchange={() => { toggleEmailPref(email.key); }} disabled={isCreate && saving} />
                {t('contacts.edit.email.pref')}
              </label>
            </div>
            {#if hasError('emails.pref')}
              <span class="input-error-msg">{errorFor('emails.pref')}</span>
            {/if}
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addEmail} disabled={isCreate && saving}>
          + {t('contacts.edit.addEmail')}
        </button>
      </fieldset>

      <!-- ── Phones ─────────────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.phone')}</legend>
        {#each vm.phones as phone (phone.key)}
          <div class="entry-block">
            <div class="entry-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.phone.number')}</span>
                <input
                  class="field-input"
                  type="tel"
                  bind:value={phone.number}
                  oninput={markDirty}
                  autocomplete="off"
                  disabled={isCreate && saving}
                />
              </label>
              <label class="field-block">
                <span class="field-label">{t('contacts.edit.phone.label')}</span>
                <input class="field-input narrow" type="text" bind:value={phone.label} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removePhone(phone.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
            <div class="chips-row">
              <button type="button" class="chip" class:chip-on={phone.contextHome} onclick={() => { phone.contextHome = !phone.contextHome; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.phone.context.home')}
              </button>
              <button type="button" class="chip" class:chip-on={phone.contextWork} onclick={() => { phone.contextWork = !phone.contextWork; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.phone.context.work')}
              </button>
              <button type="button" class="chip" class:chip-on={phone.featureCell} onclick={() => { phone.featureCell = !phone.featureCell; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.phone.feature.cell')}
              </button>
              <button type="button" class="chip" class:chip-on={phone.featureVoice} onclick={() => { phone.featureVoice = !phone.featureVoice; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.phone.feature.voice')}
              </button>
              <button type="button" class="chip" class:chip-on={phone.featureFax} onclick={() => { phone.featureFax = !phone.featureFax; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.phone.feature.fax')}
              </button>
              <label class="pref-label">
                <input type="checkbox" bind:checked={phone.pref} onchange={() => { togglePhonePref(phone.key); }} disabled={isCreate && saving} />
                {t('contacts.edit.phone.pref')}
              </label>
            </div>
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addPhone} disabled={isCreate && saving}>
          + {t('contacts.edit.addPhone')}
        </button>
      </fieldset>

      <!-- ── Addresses ──────────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.address')}</legend>
        {#each vm.addresses as addr (addr.key)}
          <div class="entry-block">
            <div class="entry-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.address.street')}</span>
                <input class="field-input" type="text" bind:value={addr.street} oninput={markDirty} autocomplete="off" disabled={isCreate && saving} />
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removeAddress(addr.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
            <div class="field-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.address.locality')}</span>
                <input class="field-input" type="text" bind:value={addr.locality} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <label class="field-block narrow">
                <span class="field-label">{t('contacts.edit.address.postcode')}</span>
                <input class="field-input" type="text" bind:value={addr.postcode} oninput={markDirty} disabled={isCreate && saving} />
              </label>
            </div>
            <div class="field-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.address.region')}</span>
                <input class="field-input" type="text" bind:value={addr.region} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.address.country')}</span>
                <input class="field-input" type="text" bind:value={addr.country} oninput={markDirty} disabled={isCreate && saving} />
              </label>
            </div>
            <div class="chips-row">
              <button type="button" class="chip" class:chip-on={addr.contextHome} onclick={() => { addr.contextHome = !addr.contextHome; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.address.context.home')}
              </button>
              <button type="button" class="chip" class:chip-on={addr.contextWork} onclick={() => { addr.contextWork = !addr.contextWork; markDirty(); }} disabled={isCreate && saving}>
                {t('contacts.edit.address.context.work')}
              </button>
              <label class="pref-label">
                <input type="checkbox" bind:checked={addr.pref} onchange={() => { if (addr.pref) { vm!.addresses.forEach((a) => { if (a.key !== addr.key) a.pref = false; }); } markDirty(); }} disabled={isCreate && saving} />
                {t('contacts.edit.address.pref')}
              </label>
            </div>
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addAddress} disabled={isCreate && saving}>
          + {t('contacts.edit.addAddress')}
        </button>
      </fieldset>

      <!-- ── Organizations ─────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.org')}</legend>
        {#each vm.orgs as org (org.key)}
          <div class="entry-block">
            <div class="entry-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.org.name')}</span>
                <input class="field-input" type="text" bind:value={org.name} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removeOrg(org.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
            {#each org.units as unit, i (i)}
              <div class="entry-row">
                <label class="field-block flex-grow">
                  <span class="field-label">{t('contacts.edit.org.unit')}</span>
                  <input class="field-input" type="text" bind:value={org.units[i]} oninput={markDirty} disabled={isCreate && saving} />
                </label>
                <button
                  type="button"
                  class="remove-btn self-end"
                  aria-label={t('contacts.edit.removeEntry')}
                  title={t('contacts.edit.removeEntry')}
                  onclick={() => removeOrgUnit(org.key, i)}
                  disabled={isCreate && saving}
                >
                  <TrashIcon size={16} />
                </button>
              </div>
            {/each}
            <button type="button" class="add-btn small" onclick={() => addOrgUnit(org.key)} disabled={isCreate && saving}>
              + {t('contacts.edit.org.addUnit')}
            </button>
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addOrg} disabled={isCreate && saving}>
          + {t('contacts.edit.addOrg')}
        </button>
      </fieldset>

      <!-- ── Titles ─────────────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.title')}</legend>
        {#each vm.titles as title (title.key)}
          <div class="entry-block">
            <div class="entry-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.title.name')}</span>
                <input class="field-input" type="text" bind:value={title.name} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <label class="field-block narrow">
                <span class="field-label">{t('contacts.edit.section.title')}</span>
                <select class="field-input" bind:value={title.kind} onchange={markDirty} disabled={isCreate && saving}>
                  <option value="title">{t('contacts.edit.title.kind.title')}</option>
                  <option value="role">{t('contacts.edit.title.kind.role')}</option>
                </select>
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removeTitle(title.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
            {#if vm.orgs.length > 0}
              <label class="field-block">
                <span class="field-label">{t('contacts.edit.title.org')}</span>
                <select class="field-input" bind:value={title.orgId} onchange={markDirty} disabled={isCreate && saving}>
                  <option value="">—</option>
                  {#each vm.orgs as org (org.key)}
                    <option value={org.key}>{org.name || '(unnamed org)'}</option>
                  {/each}
                </select>
              </label>
            {/if}
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addTitle} disabled={isCreate && saving}>
          + {t('contacts.edit.addTitle')}
        </button>
      </fieldset>

      <!-- ── Nicknames ─────────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.nickname')}</legend>
        {#each vm.nicknames as nn (nn.key)}
          <div class="entry-row">
            <label class="field-block flex-grow">
              <span class="field-label">{t('contacts.edit.nickname.name')}</span>
              <input class="field-input" type="text" bind:value={nn.name} oninput={markDirty} disabled={isCreate && saving} />
            </label>
            <button
              type="button"
              class="remove-btn self-end"
              aria-label={t('contacts.edit.removeEntry')}
              title={t('contacts.edit.removeEntry')}
              onclick={() => removeNickname(nn.key)}
              disabled={isCreate && saving}
            >
              <TrashIcon size={16} />
            </button>
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addNickname} disabled={isCreate && saving}>
          + {t('contacts.edit.addNickname')}
        </button>
      </fieldset>

      <!-- ── Links / URLs ──────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.url')}</legend>
        {#each vm.links as link (link.key)}
          <div class="entry-block">
            <div class="entry-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.url.uri')}</span>
                <input class="field-input" type="url" bind:value={link.uri} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <label class="field-block narrow">
                <span class="field-label">{t('contacts.edit.url.label')}</span>
                <input class="field-input" type="text" bind:value={link.label} oninput={markDirty} disabled={isCreate && saving} />
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removeLink(link.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addLink} disabled={isCreate && saving}>
          + {t('contacts.edit.addUrl')}
        </button>
      </fieldset>

      <!-- ── Notes ─────────────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.note')}</legend>
        {#each vm.notes as note (note.key)}
          <div class="entry-block">
            <div class="entry-row">
              <label class="field-block flex-grow">
                <span class="field-label">{t('contacts.edit.note.text')}</span>
                <textarea
                  class="field-input textarea"
                  bind:value={note.note}
                  oninput={markDirty}
                  disabled={isCreate && saving}
                  rows="3"
                ></textarea>
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removeNote(note.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addNote} disabled={isCreate && saving}>
          + {t('contacts.edit.addNote')}
        </button>
      </fieldset>

      <!-- ── Anniversaries ─────────────────────────────────────────────── -->
      <fieldset class="form-section">
        <legend class="section-legend">{t('contacts.edit.section.anniversary')}</legend>
        {#each vm.anniversaries as ann (ann.key)}
          <div class="entry-block">
            <div class="entry-row">
              <label class="field-block narrow">
                <span class="field-label">{t('contacts.edit.anniversary.kind')}</span>
                <select class="field-input" bind:value={ann.kind} onchange={markDirty} disabled={isCreate && saving}>
                  <option value="birth">{t('contacts.edit.anniversary.kind.birth')}</option>
                  <option value="wedding">{t('contacts.edit.anniversary.kind.wedding')}</option>
                  <option value="other">{t('contacts.edit.anniversary.kind.other')}</option>
                </select>
              </label>
              <label class="field-block narrow">
                <span class="field-label">{t('contacts.edit.anniversary.day')}</span>
                <input class="field-input" type="number" min="1" max="31" bind:value={ann.day} oninput={markDirty} disabled={isCreate && saving} placeholder="1-31" />
              </label>
              <label class="field-block narrow">
                <span class="field-label">{t('contacts.edit.anniversary.month')}</span>
                <input class="field-input" type="number" min="1" max="12" bind:value={ann.month} oninput={markDirty} disabled={isCreate && saving} placeholder="1-12" />
              </label>
              <label class="field-block narrow">
                <span class="field-label">{t('contacts.edit.anniversary.year')}</span>
                <input class="field-input" type="number" min="1" max="9999" bind:value={ann.year} oninput={markDirty} disabled={isCreate && saving} placeholder={t('contacts.edit.anniversary.year')} />
              </label>
              <button
                type="button"
                class="remove-btn self-end"
                aria-label={t('contacts.edit.removeEntry')}
                title={t('contacts.edit.removeEntry')}
                onclick={() => removeAnniversary(ann.key)}
                disabled={isCreate && saving}
              >
                <TrashIcon size={16} />
              </button>
            </div>
          </div>
        {/each}
        <button type="button" class="add-btn" onclick={addAnniversary} disabled={isCreate && saving}>
          + {t('contacts.edit.addAnniversary')}
        </button>
      </fieldset>

      <!-- ── Kind (de-emphasised) ──────────────────────────────────────── -->
      <details class="kind-details">
        <summary class="kind-summary">{t('contacts.edit.kind.label')}</summary>
        <label class="field-block">
          <select class="field-input" bind:value={vm.kind} onchange={markDirty} disabled={isCreate && saving}>
            <option value="individual">{t('contacts.edit.kind.individual')}</option>
            <option value="group">{t('contacts.edit.kind.group')}</option>
            <option value="org">{t('contacts.edit.kind.org')}</option>
            <option value="location">{t('contacts.edit.kind.location')}</option>
          </select>
        </label>
      </details>

      {#if isCreate}
        <!-- ── Form actions (create mode only — no id exists yet to autosave
             against, so the commit stays explicit) ────────────────────── -->
        <div class="form-actions">
          <Button type="submit" variant="primary" disabled={isCreate && saving}>
            {saving ? t('contacts.edit.saving') : t('contacts.edit.save')}
          </Button>
          <Button variant="secondary" disabled={isCreate && saving} onclick={handleCancel}>
            {t('contacts.edit.cancel')}
          </Button>
        </div>
      {/if}
    </form>
  {/if}
</div>

<style>
  .edit-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: auto;
    padding: var(--spacing-05);
    box-sizing: border-box;
  }

  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
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

  .state-msg {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    margin: 0;
  }

  .state-msg.error {
    color: var(--support-error);
  }

  .edit-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-06);
    max-width: 640px;
  }

  .form-title {
    font-size: var(--type-heading-03-size);
    margin: 0;
    color: var(--text-primary);
  }

  .form-error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    padding: var(--spacing-03);
    border: 1px solid var(--support-error);
    border-radius: var(--radius-md);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
  }

  /* Fieldset / section */
  .form-section {
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-04);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .section-legend {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 0 var(--spacing-02);
  }

  /* Field layout */
  .field-row {
    display: flex;
    gap: var(--spacing-04);
    flex-wrap: wrap;
  }

  .field-block {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    flex: 1 1 120px;
  }

  .field-block.flex-grow {
    flex: 2 1 180px;
  }

  .field-block.narrow {
    flex: 1 1 80px;
    max-width: 160px;
  }

  .field-label {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .field-input {
    height: 36px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    width: 100%;
    box-sizing: border-box;
    font-family: inherit;
  }

  .field-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .field-input:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .field-input.input-error {
    border-color: var(--support-error);
  }

  .field-input.textarea {
    height: auto;
    padding: var(--spacing-02) var(--spacing-03);
    resize: vertical;
  }

  .input-error-msg {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
  }

  /* Entry blocks (repeatable items) */
  .entry-block {
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    background: var(--layer-02);
  }

  .entry-block.entry-error {
    border-color: var(--support-error);
  }

  .entry-row {
    display: flex;
    gap: var(--spacing-03);
    align-items: flex-start;
    flex-wrap: wrap;
  }

  /* Chips (context/feature toggles) */
  .chips-row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    align-items: center;
  }

  .chip {
    padding: 2px var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: 999px;
    background: var(--field-01);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    line-height: 1.4;
  }

  .chip:hover {
    background: var(--layer-hover-01);
  }

  .chip.chip-on {
    background: var(--interactive);
    color: var(--text-on-color);
    border-color: var(--interactive);
  }

  .chip:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .pref-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .pref-label input {
    accent-color: var(--interactive);
  }

  /* Buttons */
  .add-btn {
    align-self: flex-start;
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    background: none;
    border: none;
    cursor: pointer;
    padding: 0;
    font-weight: 500;
  }

  .add-btn:hover {
    text-decoration: underline;
  }

  .add-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .add-btn.small {
    font-size: var(--type-body-compact-01-size);
    padding-left: var(--spacing-04);
  }

  /* Row/card remove control — icon-only, right-aligned to the enclosing
     card via flex order (last child of a full-width row). */
  .remove-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    align-self: center;
    width: 32px;
    height: 32px;
    flex-shrink: 0;
    color: var(--text-secondary);
    background: none;
    border: none;
    border-radius: var(--radius-pill);
    cursor: pointer;
    padding: 0;
    margin-left: auto;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .remove-btn:hover {
    background: var(--layer-hover-01);
    color: var(--support-error);
  }

  .remove-btn.self-end {
    align-self: flex-end;
  }

  .remove-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .toggle-btn {
    align-self: flex-start;
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    background: none;
    border: none;
    cursor: pointer;
    padding: 0;
  }

  .toggle-btn:hover {
    text-decoration: underline;
  }

  /* Kind details/summary */
  .kind-details {
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-04);
  }

  .kind-summary {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
    user-select: none;
  }

  .kind-details[open] .kind-summary {
    margin-bottom: var(--spacing-03);
  }

  /* Form actions */
  .form-actions {
    display: flex;
    gap: var(--spacing-03);
    align-items: center;
    padding-bottom: var(--spacing-06);
  }
</style>
