<script lang="ts">
  /**
   * Contact detail / edit view — reached via /contacts/<contactId>.
   *
   * Fetches the full JSContact card for the given id and renders a
   * summary with inline editing. The Edit button switches all fields to
   * form inputs; Save issues a Contact/set update via JMAP and reloads
   * the contacts suggestions cache so the hover card reflects the change
   * immediately (re #75).
   */

  import { jmap, strict } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { contacts } from '../lib/contacts/store.svelte';
  import { router } from '../lib/router/router.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { t } from '../lib/i18n/i18n.svelte';

  // The contact id is the second route segment: /contacts/<id>.
  let contactId = $derived(decodeURIComponent(router.parts[1] ?? ''));

  interface ContactCard {
    id: string;
    name: string;
    emails: string[];
    phones: { type: string; number: string }[];
  }

  let loadStatus = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  let contact = $state<ContactCard | null>(null);

  // Edit mode state.
  let editing = $state(false);
  let saving = $state(false);
  let editName = $state('');
  let editEmail = $state('');

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
    contact = null;
    editing = false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/get',
          { accountId, ids: [id] },
          [Capability.Contacts],
        );
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') {
        loadStatus = 'error';
        return;
      }
      const args = resp[1] as { list: unknown[] };
      const raw = args.list[0];
      if (!raw || typeof raw !== 'object') {
        loadStatus = 'error';
        return;
      }
      contact = parseContact(id, raw as Record<string, unknown>);
      loadStatus = 'ready';
    } catch {
      loadStatus = 'error';
    }
  }

  function parseContact(id: string, obj: Record<string, unknown>): ContactCard {
    const nameObj = obj.name as Record<string, unknown> | undefined;
    let name = '';
    if (nameObj && typeof nameObj.full === 'string' && nameObj.full.trim()) {
      name = nameObj.full.trim();
    } else if (nameObj && Array.isArray(nameObj.components)) {
      const parts: string[] = [];
      for (const c of nameObj.components) {
        if (typeof c === 'object' && c !== null) {
          const v = (c as Record<string, unknown>).value;
          if (typeof v === 'string') parts.push(v);
        }
      }
      name = parts.join(' ').trim();
    }

    const emailMap = obj.emails as Record<string, unknown> | undefined;
    const emails: string[] = [];
    if (emailMap) {
      for (const v of Object.values(emailMap)) {
        if (typeof v === 'object' && v !== null) {
          const addr = (v as Record<string, unknown>).address;
          if (typeof addr === 'string' && addr.includes('@')) emails.push(addr);
        }
      }
    }

    const phoneArr = obj.phones as Array<{ type?: string; number?: string }> | undefined;
    const phones: { type: string; number: string }[] = [];
    if (Array.isArray(phoneArr)) {
      for (const p of phoneArr) {
        if (typeof p?.number === 'string' && p.number.trim()) {
          phones.push({ type: p.type ?? '', number: p.number.trim() });
        }
      }
    }

    return { id, name, emails, phones };
  }

  function startEdit(): void {
    if (!contact) return;
    editName = contact.name;
    editEmail = contact.emails[0] ?? '';
    editing = true;
  }

  function cancelEdit(): void {
    editing = false;
  }

  async function saveContact(): Promise<void> {
    if (!contact) return;
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) {
      toast.show({ message: t('contact.view.saveError'), kind: 'error' });
      return;
    }
    saving = true;
    try {
      const trimmedName = editName.trim();
      const trimmedEmail = editEmail.trim();
      const patch: Record<string, unknown> = {};
      if (trimmedName !== contact.name) {
        patch['name'] = trimmedName
          ? { full: trimmedName, components: [{ type: 'personal', value: trimmedName }] }
          : null;
      }
      // Replace the primary email address while preserving other emails.
      // Use the opaque key 'primary' — same convention as the Add flow in
      // RecipientHoverCard.
      if (trimmedEmail && trimmedEmail !== contact.emails[0]) {
        patch['emails/primary'] = { address: trimmedEmail };
      }

      if (Object.keys(patch).length === 0) {
        // No changes detected — exit edit mode silently.
        editing = false;
        return;
      }

      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          {
            accountId,
            update: { [contact!.id]: patch },
          },
          [Capability.Contacts],
        );
      });
      strict(responses);
      const args = responses[0]![1] as {
        updated?: Record<string, unknown | null>;
        notUpdated?: Record<string, { type: string; description?: string } | null>;
      };
      const notUpdated = args.notUpdated?.[contact.id];
      if (notUpdated) {
        const desc = notUpdated.description ?? notUpdated.type;
        toast.show({ message: `${t('contact.view.saveError')}: ${desc}`, kind: 'error' });
        return;
      }

      // Reflect updated values in local state immediately.
      contact = {
        ...contact,
        name: trimmedName || contact.name,
        emails: trimmedEmail
          ? [trimmedEmail, ...contact.emails.slice(1)]
          : contact.emails,
      };
      editing = false;
      toast.show({ message: t('contact.view.saveSuccess'), kind: 'info' });
      // Reload suggestions cache so compose autocomplete and hover card
      // reflect the change without requiring a full page reload.
      void contacts.reload();
    } catch (err) {
      console.error('saveContact failed', err);
      toast.show({ message: t('contact.view.saveError'), kind: 'error' });
    } finally {
      saving = false;
    }
  }

  // Phone-row label localisation: map the JSContact type string to a
  // translation key. Unknown types fall back to the `contact.phone.other` label.
  function phoneLabel(type: string): string {
    const lc = type.trim().toLowerCase();
    const known: Record<string, string> = {
      mobile: 'contact.phone.mobile',
      cell: 'contact.phone.mobile',
      work: 'contact.phone.work',
      home: 'contact.phone.home',
      fax: 'contact.phone.fax',
    };
    const key = known[lc];
    if (key) return t(key);
    if (lc) return type;
    return t('contact.phone.other');
  }
</script>

<div class="contacts-view">
  <div class="toolbar">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/mail')}
    >
      {t('sidebar.inbox')}
    </button>
  </div>

  {#if loadStatus === 'loading'}
    <p class="state-msg">{t('contact.view.loading')}</p>
  {:else if loadStatus === 'error'}
    <p class="state-msg error">{t('contact.view.couldNotLoad')}</p>
    <button type="button" class="back-link" onclick={() => router.navigate('/mail')}>
      {t('sidebar.inbox')}
    </button>
  {:else if loadStatus === 'ready' && contact}
    <div class="card">
      {#if editing}
        <!-- Edit form -->
        <form
          class="edit-form"
          onsubmit={(e) => { e.preventDefault(); void saveContact(); }}
        >
          <div class="field">
            <label class="field-label" for="cv-name">{t('contact.view.name')}</label>
            <input
              id="cv-name"
              class="field-input"
              type="text"
              bind:value={editName}
              disabled={saving}
              autocomplete="off"
            />
          </div>
          <div class="field">
            <label class="field-label" for="cv-email">{t('contact.view.email')}</label>
            <input
              id="cv-email"
              class="field-input"
              type="email"
              bind:value={editEmail}
              disabled={saving}
              autocomplete="off"
            />
          </div>
          <div class="form-actions">
            <button
              type="submit"
              class="btn-primary"
              disabled={saving}
            >
              {t('contact.view.save')}
            </button>
            <button
              type="button"
              class="btn-secondary"
              disabled={saving}
              onclick={cancelEdit}
            >
              {t('contact.view.cancel')}
            </button>
          </div>
        </form>
      {:else}
        <!-- Read view with Edit button -->
        <div class="read-header">
          <h1 class="contact-name">{contact.name || contact.emails[0] || t('contact.view.title')}</h1>
          <button
            type="button"
            class="btn-edit"
            onclick={startEdit}
          >
            {t('contact.view.edit')}
          </button>
        </div>
        {#if contact.emails.length > 0}
          <section class="section">
            <h2 class="section-title">{t('contact.view.emailHeading')}</h2>
            <ul class="addr-list">
              {#each contact.emails as email (email)}
                <li><a href="mailto:{email}">{email}</a></li>
              {/each}
            </ul>
          </section>
        {/if}
        {#if contact.phones.length > 0}
          <section class="section">
            <h2 class="section-title">{t('contact.view.phoneHeading')}</h2>
            <ul class="addr-list">
              {#each contact.phones as p (`${p.type}:${p.number}`)}
                <li>
                  {#if p.type}<span class="phone-type">{phoneLabel(p.type)} &mdash; </span>{/if}
                  <a href="tel:{p.number}">{p.number}</a>
                </li>
              {/each}
            </ul>
          </section>
        {/if}
      {/if}
    </div>
  {/if}
</div>

<style>
  .contacts-view {
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
  }
  .back-btn::before {
    content: '\2190\00a0';
  }
  .state-msg {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
  }
  .state-msg.error {
    color: var(--support-error);
  }
  .card {
    max-width: 480px;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }
  .read-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }
  .contact-name {
    flex: 1 1 auto;
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    margin: 0;
    color: var(--text-primary);
  }
  .btn-edit {
    flex: 0 0 auto;
    height: 32px;
    padding: 0 var(--spacing-04);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    color: var(--text-secondary);
    background: transparent;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .btn-edit:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .section-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .addr-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .addr-list a {
    color: var(--text-primary);
    text-decoration: none;
    font-size: var(--type-body-01-size);
  }
  .addr-list a:hover {
    text-decoration: underline;
    color: var(--interactive);
  }
  .phone-type {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .back-link {
    color: var(--interactive);
    font-weight: 500;
    margin-top: var(--spacing-04);
  }

  /* Edit form */
  .edit-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-05);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
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
  }
  .field-input:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }
  .field-input:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .form-actions {
    display: flex;
    gap: var(--spacing-03);
    align-items: center;
  }
  .btn-primary {
    height: 36px;
    padding: 0 var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .btn-primary:hover:not(:disabled) {
    background: var(--interactive-hover, var(--interactive));
  }
  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
  .btn-secondary {
    height: 36px;
    padding: 0 var(--spacing-04);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    color: var(--text-secondary);
    background: transparent;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }
</style>
